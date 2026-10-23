package httpapi

import (
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
// 本层不伪造结果、不做鉴权（QT-50 ④ 统一面未落，见 docs/ARCHITECTURE.md §9.7 R6）。
type Server struct {
	svc *analysis.Service
	mux *http.ServeMux
}

// New 构造 Server 并注册路由。
func New(svc *analysis.Service) *Server {
	s := &Server{svc: svc, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /api/analysis/metrics", s.metrics)
	s.mux.HandleFunc("POST /api/analysis/benchmark", s.benchmark)
	s.mux.HandleFunc("POST /api/analysis/attribution", s.attribution)
	s.mux.HandleFunc("POST /api/analysis/sensitivity", s.sensitivity)
	s.mux.HandleFunc("POST /api/analysis/montecarlo", s.monteCarlo)
	s.mux.HandleFunc("POST /api/analysis/correlation", s.correlation)
	s.mux.HandleFunc("GET /api/analysis/reports/{id}", s.getReport)
	s.mux.HandleFunc("GET /api/analysis/reports/{id}/export", s.exportReport)
	return s
}

// Handler 返回 HTTP handler。
func (s *Server) Handler() http.Handler { return s.mux }

type errorBody struct {
	Error          string   `json:"error"`
	Code           string   `json:"code"`
	InvalidReasons []string `json:"invalid_reasons,omitempty"`
}

// Envelope 是所有报告响应的公共前缀。
type Envelope struct {
	ReportID     string                `json:"report_id"`
	RunID        string                `json:"run_id"`
	AnalysisType analysis.AnalysisType `json:"analysis_type"`
	Status       analysis.ReportStatus `json:"status"`
}

func envelopeOf(rep analysis.Report) Envelope {
	return Envelope{
		ReportID:     rep.ReportID,
		RunID:        rep.RunID,
		AnalysisType: rep.AnalysisType,
		Status:       rep.Status,
	}
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
	rep, err := s.svc.Sensitivity(r.Context(), req.RunID, req.ParamGrid)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, jobResponse{ReportID: rep.ReportID, Status: rep.Status})
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
	writeJSON(w, status, errorBody{Error: msg, Code: code, InvalidReasons: reasons})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("httpapi: encode response: %v", err)
	}
}
