// Package upstream 是本仓上游读取端口的 HTTP 适配器。
//
// 端口定义在 internal/analysis（消费端定义，不 import 上游实现）。本包只做
// 「HTTP → 领域 DTO + 哨兵」的映射，不放任何计算逻辑：
//
//   - ResultReader：core 的 R1 持久读面（`backtest_jobs.result_status` 为
//     status 的唯一权威，core B6-3 `8537ffd`；见 ResultReader 注释的偏差）；
//   - BenchmarkReader：tellurion-data 的 X1b `/datasets/resolve` +
//     `/datasets/{hash}`；
//   - HoldingsProvider / FactorExposureProvider：R2 artifact 面的 interim
//     adapter（形状未冻结，owner 未定；只做读取，不做倒推）。
//
// 未配置的 base URL 不要构造适配器：调用方保持端口为 nil，服务 fail-closed
// 成 503 dependency_unavailable（不伪造结果）。
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"tellurion-analytics/internal/analysis"
)

// maxResponseBytes 是单次上游响应的读取上限（净值序列可能很大；只防失控）。
const maxResponseBytes = 32 << 20

// defaultTimeout 是未注入 client 时的请求超时。
const defaultTimeout = 10 * time.Second

func clientOr(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: defaultTimeout}
}

func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

// applyAuth 给上游请求加服务间凭证；token 为空时不发头（回环 + 无 secret
// 的本地开发仍免密，对应 kit 启动闸的 open-access 形态）。
func applyAuth(req *http.Request, token string) {
	if token == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
}

// readErrorBody 取上游错误体的前 512 字节（只用于错误信息，不解析语义）。
func readErrorBody(r io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(r, 512))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ResultReader 读 core 的 R1 K9-correct 持久读面 `GET /api/backtest/{id}/report`。
//
// core B6-3（`8537ffd`）把 K9 结论持久化成 `backtest_jobs.result_status`，该端点以
// 这一列为 `status` 的唯一权威（列 → 结果体 → completed）：同步 / 异步两条写路径读
// 出来都是三值，「跑完但无效」（completed/invalid）返 200 不再 404。URL 与 DTO 都没
// 变，端口方法集不变（§9.7 R1 裁的就是这条）。
//
// 两条残留偏差在 core 侧，本仓不修也不猜：① 端点仍要求队列态 `status=="completed"`，
// 所以 pending / running 与引擎故障行（failed/failed）一律 404 ⇒ 映射成 ErrRunNotFound，
// K9 的 `failed` 经持久面到不了 409；② 迁移前的存量行按「旧代码把 invalid 写成 failed」
// 回填成 completed，那批行读不回 invalid。
//
// **绝不**解析 `"backtest produced an invalid run: ..."` 之类的错误串还原状态
// （docs/ARCHITECTURE.md §9.1 F1）。缺 status 一律 fail-closed：R1 之后这只可能是
// 没升级到 B6-3 的上游。
type ResultReader struct {
	base   string
	client *http.Client
	token  string
}

var _ analysis.ResultReader = (*ResultReader)(nil)

// NewResultReader 构造不带凭证的结果读取适配器（回环 + 无 secret 的开发态）。
func NewResultReader(base string, client *http.Client) *ResultReader {
	return NewResultReaderWithToken(base, client, "")
}

// NewResultReaderWithToken 构造带服务间凭证的结果读取适配器；base 是 core 的根 URL。
//
// core 的 `/api/backtest/{id}/report` 在鉴权闸后面（裁-15）：本仓作为服务
// 间调用方必须带 Bearer，否则上游配了 secret 时恒 401，所有 run 结果都读不到。
func NewResultReaderWithToken(base string, client *http.Client, token string) *ResultReader {
	return &ResultReader{
		base:   strings.TrimRight(base, "/"),
		client: clientOr(client),
		token:  strings.TrimSpace(token),
	}
}

// GetResult 按 run_id 读取结果并把 K9 三值映射成领域哨兵。
func (r *ResultReader) GetResult(ctx context.Context, runID string) (*analysis.BacktestResponse, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, analysis.ErrRunNotFound
	}
	endpoint := joinURL(r.base, "/api/backtest/"+url.PathEscape(runID)+"/report")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build result request: %v", analysis.ErrDependencyUnavailable, err)
	}
	applyAuth(req, r.token)
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: get run result %s: %v", analysis.ErrDependencyUnavailable, runID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, analysis.ErrRunNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: get run result %s: upstream %s: %s",
			analysis.ErrDependencyUnavailable, runID, resp.Status, readErrorBody(resp.Body))
	}

	var out analysis.BacktestResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: decode run result %s: %v", analysis.ErrDependencyUnavailable, runID, err)
	}
	switch out.Status {
	case analysis.ResultCompleted:
		return &out, nil
	case analysis.ResultInvalid:
		if len(out.InvalidReasons) > 0 {
			return nil, &analysis.InvalidReasonsError{Reasons: append([]string(nil), out.InvalidReasons...)}
		}
		return nil, analysis.ErrRunInvalid
	case analysis.ResultFailed:
		return nil, &analysis.FailedError{Message: out.Error}
	default:
		return nil, &analysis.IncompleteError{Missing: "status",
			Detail: "upstream report body carries no K9 status (upstream predates core B6-3)"}
	}
}

// BenchmarkReader 走 tellurion-data 的 X1b 数据集面（resolve + snapshot）。
//
// 口径纪律：请求带登记时钉住的 `adjustment`；返回的 metadata.adjustment_basis
// 必须与登记一致，否则 ErrBenchmarkDataUnavailable —— **不**用价格指数冒充
// 全收益（docs/dataflow.md §3）。
type BenchmarkReader struct {
	base   string
	client *http.Client
	token  string
}

var _ analysis.BenchmarkReader = (*BenchmarkReader)(nil)

// NewBenchmarkReader 构造不带凭证的基准读取适配器（回环 + 无 secret 的开发态）。
func NewBenchmarkReader(base string, client *http.Client) *BenchmarkReader {
	return NewBenchmarkReaderWithToken(base, client, "")
}

// NewBenchmarkReaderWithToken 构造带服务间凭证的基准读取适配器；base 是 tellurion-data 的根 URL。
//
// data 的 `/datasets/*` 要 viewer 角色（裁-15），service-to-service 必须带 Bearer。
func NewBenchmarkReaderWithToken(base string, client *http.Client, token string) *BenchmarkReader {
	return &BenchmarkReader{
		base:   strings.TrimRight(base, "/"),
		client: clientOr(client),
		token:  strings.TrimSpace(token),
	}
}

type x1bResolve struct {
	DatasetHash string `json:"dataset_hash"`
	Metadata    struct {
		AdjustmentBasis string `json:"adjustment_basis"`
	} `json:"metadata"`
}

type x1bBar struct {
	Symbol string    `json:"symbol"`
	Date   time.Time `json:"date"`
	Close  float64   `json:"close"`
}

type x1bDataset struct {
	Hash     string `json:"hash"`
	Metadata struct {
		AdjustmentBasis string `json:"adjustment_basis"`
	} `json:"metadata"`
	Bars map[string][]x1bBar `json:"bars"`
}

// GetSeries 读取基准序列（1d 收盘价）。
func (r *BenchmarkReader) GetSeries(ctx context.Context, code, basis string, start, end time.Time) (analysis.BenchmarkSeries, error) {
	code = strings.TrimSpace(code)
	basis = strings.TrimSpace(basis)
	if code == "" || basis == "" {
		return analysis.BenchmarkSeries{}, fmt.Errorf("%w: benchmark code and adjustment basis are required",
			analysis.ErrBenchmarkDataUnavailable)
	}
	if end.Before(start) {
		return analysis.BenchmarkSeries{}, fmt.Errorf("%w: end is before start", analysis.ErrBenchmarkDataUnavailable)
	}

	q := url.Values{}
	q.Set("symbols", code)
	q.Set("from", start.UTC().Format(time.RFC3339))
	q.Set("to", end.UTC().Format(time.RFC3339))
	q.Set("freq", "1d")
	q.Set("adjustment", basis)
	q.Set("as_of", end.UTC().Format(time.RFC3339))

	var resolved x1bResolve
	if err := r.getJSON(ctx, joinURL(r.base, "/datasets/resolve")+"?"+q.Encode(), &resolved); err != nil {
		return analysis.BenchmarkSeries{}, err
	}
	if resolved.DatasetHash == "" {
		return analysis.BenchmarkSeries{}, fmt.Errorf("%w: X1b resolve returned an empty dataset_hash",
			analysis.ErrBenchmarkDataUnavailable)
	}
	if resolved.Metadata.AdjustmentBasis != basis {
		return analysis.BenchmarkSeries{}, fmt.Errorf("%w: adjustment_basis mismatch (requested %q, got %q)",
			analysis.ErrBenchmarkDataUnavailable, basis, resolved.Metadata.AdjustmentBasis)
	}

	var dataset x1bDataset
	if err := r.getJSON(ctx, joinURL(r.base, "/datasets/"+url.PathEscape(resolved.DatasetHash)), &dataset); err != nil {
		return analysis.BenchmarkSeries{}, err
	}
	if dataset.Metadata.AdjustmentBasis != "" && dataset.Metadata.AdjustmentBasis != basis {
		return analysis.BenchmarkSeries{}, fmt.Errorf("%w: snapshot adjustment_basis mismatch (requested %q, got %q)",
			analysis.ErrBenchmarkDataUnavailable, basis, dataset.Metadata.AdjustmentBasis)
	}

	bars := dataset.Bars[code]
	points := make([]analysis.BenchmarkPoint, 0, len(bars))
	for _, b := range bars {
		if b.Close <= 0 {
			continue
		}
		points = append(points, analysis.BenchmarkPoint{Date: b.Date.UTC(), Value: b.Close})
	}
	if len(points) == 0 {
		return analysis.BenchmarkSeries{}, fmt.Errorf("%w: dataset %s has no usable bars for %s",
			analysis.ErrBenchmarkDataUnavailable, resolved.DatasetHash, code)
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Date.Before(points[j].Date) })

	ref := analysis.BenchmarkRef{
		Code:            code,
		AdjustmentBasis: resolved.Metadata.AdjustmentBasis,
		DatasetHash:     resolved.DatasetHash,
		Source:          "tellurion-data:x1b",
	}
	return analysis.BenchmarkSeries{Ref: ref, Points: points}, nil
}

// getJSON 取一个 JSON 响应；非 2xx 一律按 benchmark_data_unavailable 报。
func (r *BenchmarkReader) getJSON(ctx context.Context, endpoint string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%w: build request: %v", analysis.ErrBenchmarkDataUnavailable, err)
	}
	applyAuth(req, r.token)
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", analysis.ErrBenchmarkDataUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: upstream %s: %s", analysis.ErrBenchmarkDataUnavailable, resp.Status, readErrorBody(resp.Body))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(dst); err != nil {
		return fmt.Errorf("%w: decode response: %v", analysis.ErrBenchmarkDataUnavailable, err)
	}
	return nil
}

// ArtifactReader 是 holdings / 因子暴露 artifact 的 interim 读取适配器（R2）。
//
// R2 的 artifact owner 与最终形状未裁（docs/ARCHITECTURE.md §9.7）；这里按
// internal/analysis/model.go 的临时形状读取，端点形状随契约评审再迁。取不到
// 时返回 *analysis.IncompleteError（点名缺哪个输入），**不**用成交明细倒推。
type ArtifactReader struct {
	base   string
	client *http.Client
	token  string
}

var (
	_ analysis.HoldingsProvider       = (*ArtifactReader)(nil)
	_ analysis.FactorExposureProvider = (*ArtifactReader)(nil)
)

// NewArtifactReader 构造不带凭证的 artifact 读取适配器（回环 + 无 secret 的开发态）。
func NewArtifactReader(base string, client *http.Client) *ArtifactReader {
	return NewArtifactReaderWithToken(base, client, "")
}

// NewArtifactReaderWithToken 构造带服务间凭证的 artifact 读取适配器；base 是 artifact 面的根 URL。
func NewArtifactReaderWithToken(base string, client *http.Client, token string) *ArtifactReader {
	return &ArtifactReader{
		base:   strings.TrimRight(base, "/"),
		client: clientOr(client),
		token:  strings.TrimSpace(token),
	}
}

// GetPortfolioSnapshot 读取期末持仓 artifact。
func (r *ArtifactReader) GetPortfolioSnapshot(ctx context.Context, runID string) (analysis.PortfolioSnapshot, error) {
	var snap analysis.PortfolioSnapshot
	if err := r.getJSON(ctx, url.PathEscape(runID), "portfolio-snapshot", "holdings", &snap); err != nil {
		return analysis.PortfolioSnapshot{}, err
	}
	if len(snap.Holdings) == 0 {
		return analysis.PortfolioSnapshot{}, &analysis.IncompleteError{Missing: "holdings"}
	}
	return snap, nil
}

// GetFactorExposures 读取逐因子暴露与因子收益。
func (r *ArtifactReader) GetFactorExposures(ctx context.Context, runID string) (analysis.FactorExposures, error) {
	var exposures analysis.FactorExposures
	if err := r.getJSON(ctx, url.PathEscape(runID), "factor-exposures", "factor_exposures", &exposures); err != nil {
		return analysis.FactorExposures{}, err
	}
	if len(exposures.Factors) == 0 {
		return analysis.FactorExposures{}, &analysis.IncompleteError{Missing: "factor_exposures"}
	}
	return exposures, nil
}

// getJSON 读取 `/runs/{runID}/{kind}`；404 表示 artifact 缺失（409），
// 其他失败是部署态问题（503）。
func (r *ArtifactReader) getJSON(ctx context.Context, runID, kind, missing string, dst any) error {
	endpoint := joinURL(r.base, "/runs/"+runID+"/"+kind)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%w: build %s request: %v", analysis.ErrDependencyUnavailable, kind, err)
	}
	applyAuth(req, r.token)
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: get %s: %v", analysis.ErrDependencyUnavailable, kind, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return &analysis.IncompleteError{Missing: missing}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: get %s: upstream %s: %s",
			analysis.ErrDependencyUnavailable, kind, resp.Status, readErrorBody(resp.Body))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(dst); err != nil {
		return fmt.Errorf("%w: decode %s: %v", analysis.ErrDependencyUnavailable, kind, err)
	}
	return nil
}
