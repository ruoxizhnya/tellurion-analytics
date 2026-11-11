package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"tellurion-analytics/internal/analysis"
)

// MaxBodyBytes 是单个请求体上限。分析请求只有小 JSON（网格除外，1 MiB 足够）。
const MaxBodyBytes = 1 << 20

// Server 是分析服务的 HTTP 面（8 个端点，docs/api-schema.md §1）。
//
// 未接线的上游端口由 Service fail-closed 成 503 dependency_unavailable；
// 所有端点都过鉴权闸（裁-15 / docs/ARCHITECTURE.md §9.7 R6）。
type Server struct {
	svc        *analysis.Service
	authorizer Authorizer
	readiness  ReadinessFunc
	limiter    *rateLimiter
	mux        *http.ServeMux
}

// Option 按需装配可选面（鉴权闸 / 就绪判定 / 限流参数）。
type Option func(*Server)

// ReadinessReport 是 /readyz 的一次判定结果。
//
// Store 取 "postgres" / "memory"：分析服务在 memory 模式下也能起进程，但
// 「能出报告」要求结果落 analysis.*，所以 memory 恒不就绪（503）。
// Missing 点名未装配的上游端口（results / benchmarks / holdings / factors / queue）。
type ReadinessReport struct {
	Store   string
	Missing []string
	Err     error
}

// ReadinessFunc 由进程装配提供：PG 可连 + analysis.* 已迁移 + 上游端口装齐。
type ReadinessFunc func(ctx context.Context) ReadinessReport

// WithReadiness 装配 /readyz 的判定。未装配 ⇒ /readyz 恒 503（fail-closed）。
func WithReadiness(f ReadinessFunc) Option {
	return func(s *Server) { s.readiness = f }
}

// WithRateLimit 覆盖默认令牌桶（默认 50 rps / burst 200；测试与容量调参用）。
func WithRateLimit(perSecond, burst int) Option {
	return func(s *Server) { s.limiter = newRateLimiter(perSecond, burst) }
}

// routeSpec 是「注册模式 + handler」；路由表与 policy 表由守卫测试双向对拍。
type routeSpec struct {
	pattern string
	handler http.HandlerFunc
}

func (s *Server) routes() []routeSpec {
	return []routeSpec{
		{"POST /api/analysis/metrics", s.metrics},
		{"POST /api/analysis/benchmark", s.benchmark},
		{"POST /api/analysis/attribution", s.attribution},
		{"POST /api/analysis/sensitivity", s.sensitivity},
		{"POST /api/analysis/montecarlo", s.monteCarlo},
		{"POST /api/analysis/correlation", s.correlation},
		{"GET /api/analysis/reports/{id}", s.getReport},
		{"GET /api/analysis/reports/{id}/export", s.exportReport},
	}
}

// routePatterns 只返回注册模式，守卫测试用它与 policy 表对拍。
func (s *Server) routePatterns() []string {
	specs := s.routes()
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.pattern)
	}
	return out
}

// New 构造 Server 并注册路由。每个业务路由都挂 requireAuth（未装配 Authorizer → 503）；
// /readyz 是部署探针，直接注册、不鉴权不限流（见 readyz）。
func New(svc *analysis.Service, opts ...Option) *Server {
	s := &Server{
		svc:     svc,
		limiter: newRateLimiter(defaultRatePerSecond, defaultRateBurst),
		mux:     http.NewServeMux(),
	}
	for _, opt := range opts {
		opt(s)
	}
	for _, spec := range s.routes() {
		s.mux.Handle(spec.pattern, s.requireAuth(spec.handler))
	}
	s.mux.HandleFunc("GET /readyz", s.readyz)
	return s
}

// Handler 返回 HTTP handler。
func (s *Server) Handler() http.Handler { return s.mux }

// readyBody 是 /readyz 的响应体（部署面契约，不属于 8 个分析端点）。
type readyBody struct {
	Status  string   `json:"status"`
	Store   string   `json:"store"`
	Missing []string `json:"missing,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// readyz 判定「能出报告」而不是「进程活着」：/healthz 承担后者。
//
// 三种未就绪：未装配判定（store=unwired）/ 持久层不可用（Err）/ 上游端口缺失
// （Missing 点名）。memory Store 不算就绪——结果不落 analysis.*，重启即丢失。
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.readiness == nil {
		writeJSON(w, http.StatusServiceUnavailable, readyBody{
			Status:  "not_ready",
			Store:   "unwired",
			Missing: []string{"store"},
		})
		return
	}
	report := s.readiness(r.Context())
	body := readyBody{Status: "not_ready", Store: report.Store, Missing: report.Missing}
	if report.Err != nil {
		body.Error = report.Err.Error()
	}
	if report.Err == nil && report.Store == "postgres" && len(report.Missing) == 0 {
		body.Status = "ready"
		body.Missing = nil
		writeJSON(w, http.StatusOK, body)
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, body)
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	// Missing 是 run_result_incomplete 的机器可读明细（缺哪个输入）；
	// 词表见 docs/api-schema.md §6。其他错误码不带本字段。
	Missing        string   `json:"missing,omitempty"`
	InvalidReasons []string `json:"invalid_reasons,omitempty"`
}

type metricsRequest struct {
	RunID string `json:"run_id"`
}

type sensitivityRequest struct {
	RunID     string               `json:"run_id"`
	ParamGrid []analysis.GridParam `json:"param_grid"`
}

type jobResponse struct {
	ReportID string                `json:"report_id"`
	Status   analysis.ReportStatus `json:"status"`
	// ParentJobID 只对 sensitivity 出现（X5b 父作业），montecarlo 不带。
	ParentJobID string `json:"parent_job_id,omitempty"`
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	var req metricsRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeInvalidParams(w, err)
		return
	}
	rep, err := s.svc.Metrics(r.Context(), req.RunID)
	if err != nil {
		writeError(w, err)
		return
	}
	var payload analysis.MetricsPayload
	writeSync(w, rep, &payload)
}

func (s *Server) benchmark(w http.ResponseWriter, r *http.Request) {
	var req analysis.BenchmarkRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeInvalidParams(w, err)
		return
	}
	rep, err := s.svc.Benchmark(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	var payload analysis.BenchmarkPayload
	writeSync(w, rep, &payload)
}

func (s *Server) attribution(w http.ResponseWriter, r *http.Request) {
	var req analysis.AttributionRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeInvalidParams(w, err)
		return
	}
	rep, err := s.svc.Attribution(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	var payload analysis.AttributionPayload
	writeSync(w, rep, &payload)
}

func (s *Server) sensitivity(w http.ResponseWriter, r *http.Request) {
	var req sensitivityRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeInvalidParams(w, err)
		return
	}
	sub, err := s.svc.Sensitivity(r.Context(), req.RunID, req.ParamGrid)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, jobResponse{
		ReportID:    sub.Report.ReportID,
		Status:      sub.Report.Status,
		ParentJobID: sub.ParentJobID,
	})
}

func (s *Server) monteCarlo(w http.ResponseWriter, r *http.Request) {
	var req analysis.MonteCarloRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeInvalidParams(w, err)
		return
	}
	rep, err := s.svc.MonteCarlo(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, jobResponse{ReportID: rep.ReportID, Status: rep.Status})
}

func (s *Server) correlation(w http.ResponseWriter, r *http.Request) {
	var req analysis.CorrelationRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeInvalidParams(w, err)
		return
	}
	rep, err := s.svc.Correlation(r.Context(), req.RunIDs)
	if err != nil {
		writeError(w, err)
		return
	}
	var payload analysis.CorrelationPayload
	writeSync(w, rep, &payload)
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	rep, err := s.svc.GetReport(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) exportReport(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		writeInvalidParams(w, errors.New("format is required (html|csv)"))
		return
	}
	rep, err := s.svc.GetReport(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	body, contentType, filename, err := analysis.Export(rep, format)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		log.Printf("httpapi: write export: %v", err)
	}
}

// writeSync 把已存报告体解回 payload，与公共信封合并成扁平响应。
//
// 用 map 合并而不是匿名嵌入：payload 由调用方以 any 传入，扁平化后字段序
// 由 encoding/json 按 key 排序决定，重放（同报告体）字节一致。
func writeSync(w http.ResponseWriter, rep analysis.Report, payload any) {
	if err := json.Unmarshal(rep.Body, payload); err != nil {
		log.Printf("httpapi: decode stored report %s: %v", rep.ReportID, err)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error", Code: "internal"})
		return
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("httpapi: encode payload %s: %v", rep.ReportID, err)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error", Code: "internal"})
		return
	}
	merged := make(map[string]any, 8)
	if err := json.Unmarshal(bodyBytes, &merged); err != nil {
		log.Printf("httpapi: decode payload %s: %v", rep.ReportID, err)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error", Code: "internal"})
		return
	}
	merged["report_id"] = rep.ReportID
	merged["run_id"] = rep.RunID
	merged["analysis_type"] = rep.AnalysisType
	merged["status"] = rep.Status
	writeJSON(w, http.StatusOK, merged)
}

// decodeBody 严格解码：拒未知字段、拒尾随内容、限制体积。
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("request body is required")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is required")
		}
		return err
	}
	if dec.More() {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

// classify 把领域哨兵映射成 wire code（词表唯一真相 = docs/api-schema.md §2）。
func classify(err error) (int, string, []string) {
	var invalid *analysis.InvalidReasonsError
	if errors.As(err, &invalid) {
		return http.StatusConflict, "run_result_invalid", invalid.Reasons
	}
	switch {
	case errors.Is(err, analysis.ErrRunNotFound):
		return http.StatusNotFound, "run_not_found", nil
	case errors.Is(err, analysis.ErrBenchmarkNotRegistered):
		return http.StatusNotFound, "benchmark_not_registered", nil
	case errors.Is(err, analysis.ErrReportNotFound):
		return http.StatusNotFound, "report_not_found", nil
	case errors.Is(err, analysis.ErrRunInvalid):
		return http.StatusConflict, "run_result_invalid", nil
	case errors.Is(err, analysis.ErrRunFailed):
		return http.StatusConflict, "run_result_failed", nil
	case errors.Is(err, analysis.ErrRunIncomplete):
		return http.StatusConflict, "run_result_incomplete", nil
	case errors.Is(err, analysis.ErrBenchmarkDataUnavailable):
		return http.StatusConflict, "benchmark_data_unavailable", nil
	case errors.Is(err, analysis.ErrReportNotReady):
		return http.StatusConflict, "report_not_ready", nil
	case errors.Is(err, analysis.ErrReportConflict):
		// 写侧不可变冲突：同 report_id 已有 completed 且内容不同。那是调用方
		// 提交了不可重试的冲突内容，不是服务内部故障（500 会被误当可重试）。
		return http.StatusConflict, "report_conflict", nil
	case errors.Is(err, analysis.ErrMethodNotSupported):
		return http.StatusBadRequest, "method_not_supported", nil
	case errors.Is(err, analysis.ErrSeedRequired):
		return http.StatusBadRequest, "seed_required", nil
	case errors.Is(err, analysis.ErrInvalidParams):
		return http.StatusBadRequest, "invalid_params", nil
	case errors.Is(err, analysis.ErrDependencyUnavailable):
		return http.StatusServiceUnavailable, "dependency_unavailable", nil
	default:
		return http.StatusInternalServerError, "internal", nil
	}
}

func writeInvalidParams(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error(), Code: "invalid_params"})
}

func writeError(w http.ResponseWriter, err error) {
	status, code, reasons := classify(err)
	// 4xx / 503 的原文透传给调用方（便于修正请求或定位未接线依赖），5xx 才隐藏细节。
	msg := err.Error()
	if status == http.StatusInternalServerError {
		log.Printf("httpapi: internal error: %v", err)
		msg = "internal error"
	}
	var incomplete *analysis.IncompleteError
	missing := ""
	if errors.As(err, &incomplete) {
		missing = incomplete.Missing
	}
	writeJSON(w, status, errorBody{Error: msg, Code: code, Missing: missing, InvalidReasons: reasons})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("httpapi: encode response: %v", err)
	}
}
