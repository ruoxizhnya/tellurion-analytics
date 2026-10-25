package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Options 构造 Service。所有上游端口都可为 nil：
// 未接线的端口必须让对应端点 fail-closed（503 dependency_unavailable），
// 而不是 panic 或返回空报告。
type Options struct {
	Store      Store
	Results    ResultReader
	Benchmarks BenchmarkReader
	Holdings   HoldingsProvider
	Factors    FactorExposureProvider
	Queue      SensitivityQueue
	Executor   Executor
	// Now 可注入（测试固定时间）；零值取 time.Now。
	Now func() time.Time
}

// Service 是分析服务的领域门面：读上游 → 纯函数计算 → 写 analysis.*。
type Service struct {
	store      Store
	results    ResultReader
	benchmarks BenchmarkReader
	holdings   HoldingsProvider
	factors    FactorExposureProvider
	queue      SensitivityQueue
	executor   Executor
	now        func() time.Time
}

// New 构造 Service。
func New(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("analysis: store is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Executor == nil {
		opts.Executor = InlineExecutor{}
	}
	return &Service{
		store:      opts.Store,
		results:    opts.Results,
		benchmarks: opts.Benchmarks,
		holdings:   opts.Holdings,
		factors:    opts.Factors,
		queue:      opts.Queue,
		executor:   opts.Executor,
		now:        opts.Now,
	}, nil
}

// Metrics 计算绩效指标（F5：from_result 原样 + 只补算缺失项）。
func (s *Service) Metrics(ctx context.Context, runID string) (Report, error) {
	if strings.TrimSpace(runID) == "" {
		return Report{}, invalidf("run_id is required")
	}
	resp, err := s.loadResult(ctx, runID)
	if err != nil {
		return Report{}, err
	}
	paramsHash, err := hashJSON(struct{}{})
	if err != nil {
		return Report{}, err
	}
	inputHash, err := hashJSON(resp)
	if err != nil {
		return Report{}, err
	}
	if rep, ok, err := s.replay(ctx, runID, TypeMetrics, paramsHash, inputHash, nil); err != nil || ok {
		return rep, err
	}
	payload, err := buildMetricsPayload(resp)
	if err != nil {
		return Report{}, err
	}
	return s.finishSync(ctx, runID, TypeMetrics, paramsHash, inputHash, nil, payload)
}

// Benchmark 计算基准对比。
func (s *Service) Benchmark(ctx context.Context, req BenchmarkRequest) (Report, error) {
	if strings.TrimSpace(req.RunID) == "" {
		return Report{}, invalidf("run_id is required")
	}
	if strings.TrimSpace(req.BenchmarkCode) == "" {
		return Report{}, invalidf("benchmark_code is required")
	}
	window := req.Window
	if window == 0 {
		window = DefaultRollingWindow
	}
	if window < 0 {
		return Report{}, invalidf("window must be positive")
	}
	resp, err := s.loadResult(ctx, req.RunID)
	if err != nil {
		return Report{}, err
	}
	if len(resp.PortfolioValues) < 2 {
		return Report{}, &IncompleteError{Missing: "portfolio_values"}
	}
	ref, err := s.store.LatestBenchmark(ctx, req.BenchmarkCode)
	if err != nil {
		return Report{}, err
	}
	if s.benchmarks == nil {
		return Report{}, fmt.Errorf("%w: benchmark reader is not wired", ErrDependencyUnavailable)
	}
	series, err := s.benchmarks.GetSeries(ctx, req.BenchmarkCode, ref.AdjustmentBasis,
		resp.PortfolioValues[0].Date, resp.PortfolioValues[len(resp.PortfolioValues)-1].Date)
	if err != nil {
		if errors.Is(err, ErrBenchmarkNotRegistered) || errors.Is(err, ErrBenchmarkDataUnavailable) {
			return Report{}, err
		}
		return Report{}, fmt.Errorf("%w: %v", ErrBenchmarkDataUnavailable, err)
	}
	paramsHash, err := hashJSON(struct {
		BenchmarkCode string `json:"benchmark_code"`
		Window        int    `json:"window"`
	}{BenchmarkCode: req.BenchmarkCode, Window: window})
	if err != nil {
		return Report{}, err
	}
	resultHash, err := hashJSON(resp)
	if err != nil {
		return Report{}, err
	}
	inputHash := hashInputs(resultHash, ref.DatasetHash, ref.AdjustmentBasis)
	if rep, ok, err := s.replay(ctx, req.RunID, TypeBenchmark, paramsHash, inputHash, nil); err != nil || ok {
		return rep, err
	}
	payload, err := buildBenchmarkPayload(resp, series, ref, window)
	if err != nil {
		return Report{}, err
	}
	return s.finishSync(ctx, req.RunID, TypeBenchmark, paramsHash, inputHash, nil, payload)
}

// Attribution 计算归因（brinson / factor）。
func (s *Service) Attribution(ctx context.Context, req AttributionRequest) (Report, error) {
	if strings.TrimSpace(req.RunID) == "" {
		return Report{}, invalidf("run_id is required")
	}
	if req.Method != "brinson" && req.Method != "factor" {
		return Report{}, fmt.Errorf("%w: %q", ErrMethodNotSupported, req.Method)
	}
	resp, err := s.loadResult(ctx, req.RunID)
	if err != nil {
		return Report{}, err
	}
	paramsHash, err := hashJSON(struct {
		Method string `json:"method"`
	}{Method: req.Method})
	if err != nil {
		return Report{}, err
	}
	resultHash, err := hashJSON(resp)
	if err != nil {
		return Report{}, err
	}

	var payload AttributionPayload
	switch req.Method {
	case "brinson":
		if s.holdings == nil {
			return Report{}, fmt.Errorf("%w: holdings provider is not wired", ErrDependencyUnavailable)
		}
		snapshot, err := s.holdings.GetPortfolioSnapshot(ctx, req.RunID)
		if err != nil {
			return Report{}, err
		}
		// 契约校验必须在算 input_hash 之前：缺 dataset_hash 时同键会复用旧报告。
		if err := validatePortfolioSnapshot(snapshot); err != nil {
			return Report{}, err
		}
		inputHash := hashInputs(resultHash, snapshot.DatasetHash)
		if rep, ok, err := s.replay(ctx, req.RunID, TypeAttribution, paramsHash, inputHash, nil); err != nil || ok {
			return rep, err
		}
		payload, err = buildBrinsonAttribution(resp, snapshot)
		if err != nil {
			return Report{}, err
		}
		return s.finishAttribution(ctx, req.RunID, paramsHash, inputHash, payload)
	case "factor":
		if s.factors == nil {
			return Report{}, fmt.Errorf("%w: factor exposure provider is not wired", ErrDependencyUnavailable)
		}
		exposures, err := s.factors.GetFactorExposures(ctx, req.RunID)
		if err != nil {
			return Report{}, err
		}
		// 同 brinson：契约校验先于 input_hash，避免 artifact 换版复用旧报告。
		if err := validateFactorExposures(exposures); err != nil {
			return Report{}, err
		}
		inputHash := hashInputs(resultHash, exposures.DatasetHash)
		if rep, ok, err := s.replay(ctx, req.RunID, TypeAttribution, paramsHash, inputHash, nil); err != nil || ok {
			return rep, err
		}
		payload, err = buildFactorAttribution(resp, exposures)
		if err != nil {
			return Report{}, err
		}
		return s.finishAttribution(ctx, req.RunID, paramsHash, inputHash, payload)
	}
	return Report{}, fmt.Errorf("%w: %q", ErrMethodNotSupported, req.Method)
}

// SensitivitySubmission 是敏感性提交的回执：报告 + 上游父作业 id。
//
// 父作业 id 进 202 响应（可选字段），让调用方 / 运维能直接查
// `GET /api/runs/parents/{id}`；它同时落 `analysis.sensitivity_runs.parent_job_id`。
type SensitivitySubmission struct {
	Report      Report
	ParentJobID string
}

// Sensitivity 把参数网格提交给 X5（QT-44）并建 pending 报告。
// 队列未接线 ⇒ 503 dependency_unavailable（不在本仓起回测循环）。
func (s *Service) Sensitivity(ctx context.Context, runID string, grid []GridParam) (SensitivitySubmission, error) {
	if strings.TrimSpace(runID) == "" {
		return SensitivitySubmission{}, invalidf("run_id is required")
	}
	// 参数名归一化一次：展开顺序、params_hash、落库网格与提交给父作业的键必须
	// 是同一份文本，否则「点 ↔ 参数」会在两处不一致。
	normalized := make([]GridParam, len(grid))
	for i, p := range grid {
		normalized[i] = GridParam{Name: strings.TrimSpace(p.Name), Values: append([]float64(nil), p.Values...)}
	}
	grid = normalized
	if _, err := expandGrid(grid); err != nil {
		return SensitivitySubmission{}, err
	}
	resp, err := s.loadResult(ctx, runID)
	if err != nil {
		return SensitivitySubmission{}, err
	}
	resultHash, err := hashJSON(resp)
	if err != nil {
		return SensitivitySubmission{}, err
	}
	paramsHash, err := hashJSON(struct {
		Grid []GridParam `json:"param_grid"`
	}{Grid: grid})
	if err != nil {
		return SensitivitySubmission{}, err
	}
	inputHash := hashInputs(resultHash)
	if rep, ok, err := s.replay(ctx, runID, TypeSensitivity, paramsHash, inputHash, nil); err != nil || ok {
		sub := SensitivitySubmission{Report: rep}
		if ok {
			if rec, gerr := s.store.GetSensitivity(ctx, rep.ReportID); gerr == nil {
				sub.ParentJobID = rec.ParentJobID
			}
		}
		return sub, err
	}
	if s.queue == nil {
		return SensitivitySubmission{}, fmt.Errorf("%w: sensitivity job queue (QT-44) is not wired", ErrDependencyUnavailable)
	}
	reportID := ReportID(runID, TypeSensitivity, paramsHash, inputHash, nil, MetricsVersion)
	parentID, err := s.queue.Submit(ctx, SensitivityJob{ReportID: reportID, RunID: runID, Grid: grid})
	if err != nil {
		return SensitivitySubmission{}, fmt.Errorf("%w: %v", ErrDependencyUnavailable, err)
	}
	now := s.now().UTC()
	gridJSON, err := json.Marshal(grid)
	if err != nil {
		return SensitivitySubmission{}, err
	}
	// 先落网格行（pending 报告是它的外键），再落报告：中途失败时重放能续上，
	// 不会出现「报告存在但不知道父作业是谁」。
	if err := s.store.PutSensitivity(ctx, SensitivityRecord{
		ReportID:    reportID,
		RunID:       runID,
		Grid:        gridJSON,
		ParentJobID: parentID,
		CreatedAt:   now,
	}); err != nil {
		return SensitivitySubmission{}, err
	}
	rep := Report{
		ReportID:       reportID,
		RunID:          runID,
		AnalysisType:   TypeSensitivity,
		Status:         ReportPending,
		MetricsVersion: MetricsVersion,
		ParamsHash:     paramsHash,
		InputHash:      inputHash,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.PutReport(ctx, rep); err != nil {
		return SensitivitySubmission{}, err
	}
	return SensitivitySubmission{Report: rep, ParentJobID: parentID}, nil
}

// MonteCarlo 用显式种子做重抽样（异步执行，202 + pending）。
func (s *Service) MonteCarlo(ctx context.Context, req MonteCarloRequest) (Report, error) {
	if strings.TrimSpace(req.RunID) == "" {
		return Report{}, invalidf("run_id is required")
	}
	if req.Seed == nil {
		return Report{}, ErrSeedRequired
	}
	if req.N <= 0 || req.N > MaxMonteCarloN {
		return Report{}, invalidf("n must be in [1, %d]", MaxMonteCarloN)
	}
	method := req.Method
	if method == "" {
		method = MethodBootstrap
	}
	if method != MethodBootstrap && method != MethodShuffle {
		return Report{}, invalidf("unknown method %q", method)
	}
	resp, err := s.loadResult(ctx, req.RunID)
	if err != nil {
		return Report{}, err
	}
	paramsHash, err := hashJSON(struct {
		N      int    `json:"n"`
		Method string `json:"method"`
	}{N: req.N, Method: method})
	if err != nil {
		return Report{}, err
	}
	resultHash, err := hashJSON(resp)
	if err != nil {
		return Report{}, err
	}
	seed := *req.Seed
	inputHash := hashInputs(resultHash)
	if rep, ok, err := s.replay(ctx, req.RunID, TypeMonteCarlo, paramsHash, inputHash, &seed); err != nil || ok {
		return rep, err
	}

	now := s.now().UTC()
	reportID := ReportID(req.RunID, TypeMonteCarlo, paramsHash, inputHash, &seed, MetricsVersion)
	rep := Report{
		ReportID:       reportID,
		RunID:          req.RunID,
		AnalysisType:   TypeMonteCarlo,
		Status:         ReportPending,
		MetricsVersion: MetricsVersion,
		ParamsHash:     paramsHash,
		InputHash:      inputHash,
		Seed:           &seed,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.PutReport(ctx, rep); err != nil {
		return Report{}, err
	}

	s.executor.Submit(func() {
		s.runMonteCarlo(rep, resp, req.N, seed, method)
	})
	return rep, nil
}

// runMonteCarlo 在 Executor 里完成计算并推进报告状态。
func (s *Service) runMonteCarlo(rep Report, resp *BacktestResponse, n int, seed int64, method string) {
	payload, err := buildMonteCarloPayload(resp, n, seed, method)
	rep.UpdatedAt = s.now().UTC()
	if err != nil {
		rep.Status = ReportFailed
		rep.Error = err.Error()
		_ = s.store.PutReport(context.Background(), rep)
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		rep.Status = ReportFailed
		rep.Error = err.Error()
		_ = s.store.PutReport(context.Background(), rep)
		return
	}
	rep.Status = ReportCompleted
	rep.Body = body
	_ = s.store.PutReport(context.Background(), rep)
	_ = s.store.PutMonteCarlo(context.Background(), MonteCarloRecord{
		ReportID:  rep.ReportID,
		Seed:      seed,
		N:         n,
		Method:    method,
		InputHash: rep.InputHash,
		Summary:   body,
		CreatedAt: rep.UpdatedAt,
	})
}

// Correlation 计算多 run 的相关性矩阵（不返回部分矩阵）。
func (s *Service) Correlation(ctx context.Context, runIDs []string) (Report, error) {
	ids := dedupeIDs(runIDs)
	if len(ids) < 2 {
		return Report{}, invalidf("correlation requires at least 2 run_ids")
	}
	if len(ids) > MaxCorrelationIDs {
		return Report{}, invalidf("correlation accepts at most %d run_ids", MaxCorrelationIDs)
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return Report{}, invalidf("run_ids must not contain empty values")
		}
	}
	results := make(map[string]*BacktestResponse, len(ids))
	parts := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		resp, err := s.loadResult(ctx, id)
		if err != nil {
			if errors.Is(err, ErrRunNotFound) {
				return Report{}, fmt.Errorf("%w: %s", ErrRunNotFound, id)
			}
			return Report{}, err
		}
		h, err := hashJSON(resp)
		if err != nil {
			return Report{}, err
		}
		results[id] = resp
		parts = append(parts, id, h)
	}
	paramsHash, err := hashJSON(struct {
		RunIDs []string `json:"run_ids"`
	}{RunIDs: ids})
	if err != nil {
		return Report{}, err
	}
	inputHash := hashInputs(parts...)
	runID := strings.Join(ids, ",")
	if rep, ok, err := s.replay(ctx, runID, TypeCorrelation, paramsHash, inputHash, nil); err != nil || ok {
		return rep, err
	}
	payload, err := buildCorrelationPayload(ids, results)
	if err != nil {
		return Report{}, err
	}
	return s.finishSync(ctx, runID, TypeCorrelation, paramsHash, inputHash, nil, payload)
}

// GetReport 读报告（pending / running / failed 都返回，状态在 body 里说明）。
//
// 敏感性报告的 pending → completed/failed 由**读路径**推进：每次读都去看一眼
// 上游父作业；进程重启不丢进度，轮询天然幂等。其他类型不经过这一步。
func (s *Service) GetReport(ctx context.Context, reportID string) (Report, error) {
	if strings.TrimSpace(reportID) == "" {
		return Report{}, ErrReportNotFound
	}
	rep, err := s.store.GetReport(ctx, reportID)
	if err != nil {
		return Report{}, err
	}
	if rep.AnalysisType == TypeSensitivity && (rep.Status == ReportPending || rep.Status == ReportRunning) {
		return s.advanceSensitivity(ctx, rep)
	}
	return rep, nil
}

// loadResult 读上游结果并把三值映射成领域哨兵。
func (s *Service) loadResult(ctx context.Context, runID string) (*BacktestResponse, error) {
	if s.results == nil {
		return nil, fmt.Errorf("%w: result reader is not wired", ErrDependencyUnavailable)
	}
	resp, err := s.results.GetResult(ctx, runID)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, &IncompleteError{Missing: "run result"}
	}
	if err := requireResult(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// replay 返回已存的同键报告（幂等：不重复计算）。
func (s *Service) replay(ctx context.Context, runID string, typ AnalysisType, paramsHash, inputHash string, seed *int64) (Report, bool, error) {
	id := ReportID(runID, typ, paramsHash, inputHash, seed, MetricsVersion)
	rep, err := s.store.GetReport(ctx, id)
	if err == nil {
		return rep, true, nil
	}
	if errors.Is(err, ErrReportNotFound) {
		return Report{}, false, nil
	}
	return Report{}, false, err
}

// finishSync 落 completed 报告并返回。
func (s *Service) finishSync(ctx context.Context, runID string, typ AnalysisType, paramsHash, inputHash string, seed *int64, payload any) (Report, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Report{}, err
	}
	now := s.now().UTC()
	rep := Report{
		ReportID:       ReportID(runID, typ, paramsHash, inputHash, seed, MetricsVersion),
		RunID:          runID,
		AnalysisType:   typ,
		Status:         ReportCompleted,
		MetricsVersion: MetricsVersion,
		Body:           body,
		ParamsHash:     paramsHash,
		InputHash:      inputHash,
		Seed:           seed,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.PutReport(ctx, rep); err != nil {
		return Report{}, err
	}
	return rep, nil
}

// finishAttribution 落归因报告 + analysis.attribution 行（residual 必须落库）。
func (s *Service) finishAttribution(ctx context.Context, runID, paramsHash, inputHash string, payload AttributionPayload) (Report, error) {
	rep, err := s.finishSync(ctx, runID, TypeAttribution, paramsHash, inputHash, nil, payload)
	if err != nil {
		return Report{}, err
	}
	contributions, err := json.Marshal(payload.Contributions)
	if err != nil {
		return Report{}, err
	}
	if err := s.store.PutAttribution(ctx, AttributionRecord{
		ReportID:      rep.ReportID,
		Method:        payload.Method,
		Contributions: contributions,
		Residual:      payload.Residual,
		InputHash:     inputHash,
		CreatedAt:     rep.UpdatedAt,
	}); err != nil {
		return Report{}, err
	}
	return rep, nil
}

// dedupeIDs 去重并保留首次出现的顺序（compare / correlation 的既有口径）。
func dedupeIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
