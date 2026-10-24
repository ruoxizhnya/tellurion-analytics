package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tellurion-analytics/internal/analysis"
)

type stubReader struct {
	results map[string]*analysis.BacktestResponse
}

func (s *stubReader) GetResult(_ context.Context, runID string) (*analysis.BacktestResponse, error) {
	if resp, ok := s.results[runID]; ok {
		return resp, nil
	}
	return nil, analysis.ErrRunNotFound
}

type stubQueue struct{ submitted int }

func (s *stubQueue) Submit(_ context.Context, _ analysis.SensitivityJob) (string, error) {
	s.submitted++
	return "job-1", nil
}

type stubBenchmarks struct {
	series analysis.BenchmarkSeries
	err    error
}

func (s *stubBenchmarks) GetSeries(_ context.Context, _, _ string, _, _ time.Time) (analysis.BenchmarkSeries, error) {
	if s.err != nil {
		return analysis.BenchmarkSeries{}, s.err
	}
	return s.series, nil
}

var testDate = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

func sampleResult(id string) *analysis.BacktestResponse {
	values := make([]analysis.PortfolioValue, 0, 40)
	value := 100.0
	for i := 0; i < 40; i++ {
		values = append(values, analysis.PortfolioValue{Date: testDate.AddDate(0, 0, i), TotalValue: value})
		if i%2 == 0 {
			value *= 1.01
		} else {
			value *= 0.995
		}
	}
	return &analysis.BacktestResponse{
		ID:              id,
		Status:          analysis.ResultCompleted,
		TotalReturn:     0.1,
		SharpeRatio:     1.234,
		PortfolioValues: values,
	}
}

func newTestServer(t *testing.T, opts analysis.Options) (*httptest.Server, *analysis.Service) {
	t.Helper()
	if opts.Store == nil {
		opts.Store = analysis.NewMemory()
	}
	opts.Now = func() time.Time { return testDate }
	svc, err := analysis.New(opts)
	if err != nil {
		t.Fatalf("analysis.New: %v", err)
	}
	srv := httptest.NewServer(New(svc).Handler())
	t.Cleanup(srv.Close)
	return srv, svc
}

func post(t *testing.T, srv *httptest.Server, path, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func get(t *testing.T, srv *httptest.Server, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return m
}

func wantCode(t *testing.T, status int, body []byte, wantStatus int, wantCode string) {
	t.Helper()
	m := decode(t, body)
	if status != wantStatus || m["code"] != wantCode {
		t.Fatalf("got %d %v, want %d %s", status, m, wantStatus, wantCode)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	srv, _ := newTestServer(t, analysis.Options{
		Results: &stubReader{results: map[string]*analysis.BacktestResponse{"r1": sampleResult("r1")}},
	})
	status, body := post(t, srv, "/api/analysis/metrics", `{"run_id":"r1"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	m := decode(t, body)
	if m["run_id"] != "r1" || m["analysis_type"] != "metrics" || m["status"] != "completed" {
		t.Fatalf("envelope = %v", m)
	}
	fromResult, ok := m["from_result"].(map[string]any)
	if !ok || fromResult["sharpe_ratio"] != 1.234 {
		t.Fatalf("from_result = %v", m["from_result"])
	}
	computed, ok := m["computed"].(map[string]any)
	if !ok {
		t.Fatalf("computed missing: %v", m)
	}
	if _, ok := computed["volatility"]; !ok {
		t.Fatalf("volatility missing: %v", computed)
	}
	if _, ok := fromResult["volatility"]; ok {
		t.Fatalf("volatility must not be in from_result")
	}
	reportID, _ := m["report_id"].(string)
	if reportID == "" {
		t.Fatalf("report_id missing")
	}

	// GET 报告
	resp, reportBody := get(t, srv, "/api/analysis/reports/"+reportID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET report = %d %s", resp.StatusCode, reportBody)
	}
	report := decode(t, reportBody)
	if report["report_id"] != reportID || report["report"] == nil {
		t.Fatalf("GET report body = %v", report)
	}

	// 导出 html / csv
	for _, format := range []string{"html", "csv"} {
		expResp, expBody := get(t, srv, "/api/analysis/reports/"+reportID+"/export?format="+format)
		if expResp.StatusCode != http.StatusOK || len(expBody) == 0 {
			t.Fatalf("export %s = %d %s", format, expResp.StatusCode, expBody)
		}
		if cd := expResp.Header.Get("Content-Disposition"); !strings.Contains(cd, "."+format) {
			t.Fatalf("export %s Content-Disposition = %q", format, cd)
		}
	}
}

func TestStrictDecodeAndMissingBody(t *testing.T) {
	srv, _ := newTestServer(t, analysis.Options{})
	status, body := post(t, srv, "/api/analysis/metrics", `{"run_id":"r1","extra":1}`)
	wantCode(t, status, body, http.StatusBadRequest, "invalid_params")

	status, body = post(t, srv, "/api/analysis/metrics", "")
	wantCode(t, status, body, http.StatusBadRequest, "invalid_params")

	resp, err := http.Post(srv.URL+"/api/analysis/metrics", "application/json", strings.NewReader(`{"run_id":"r1"}{"run_id":"r2"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	wantCode(t, resp.StatusCode, out, http.StatusBadRequest, "invalid_params")
}

func TestMonteCarloSeedWireCodes(t *testing.T) {
	srv, _ := newTestServer(t, analysis.Options{
		Results: &stubReader{results: map[string]*analysis.BacktestResponse{"r1": sampleResult("r1")}},
	})
	status, body := post(t, srv, "/api/analysis/montecarlo", `{"run_id":"r1","n":10}`)
	wantCode(t, status, body, http.StatusBadRequest, "seed_required")

	status, body = post(t, srv, "/api/analysis/montecarlo", `{"run_id":"r1","n":0,"seed":0}`)
	wantCode(t, status, body, http.StatusBadRequest, "invalid_params")

	status, body = post(t, srv, "/api/analysis/montecarlo", `{"run_id":"r1","n":10,"seed":0}`)
	if status != http.StatusAccepted {
		t.Fatalf("seed=0: status = %d, body = %s", status, body)
	}
	m := decode(t, body)
	if m["status"] != "pending" || m["report_id"] == "" {
		t.Fatalf("job response = %v", m)
	}
}

func TestThreeValueAndMissingRunWireCodes(t *testing.T) {
	reader := &stubReader{results: map[string]*analysis.BacktestResponse{
		"invalid": {Status: analysis.ResultInvalid, InvalidReasons: []string{"garbage_metric"}},
		"failed":  {Status: analysis.ResultFailed, Error: "boom"},
		"empty":   {Status: analysis.ResultCompleted},
	}}
	srv, _ := newTestServer(t, analysis.Options{Results: reader})

	status, body := post(t, srv, "/api/analysis/metrics", `{"run_id":"missing"}`)
	wantCode(t, status, body, http.StatusNotFound, "run_not_found")

	status, body = post(t, srv, "/api/analysis/metrics", `{"run_id":"invalid"}`)
	wantCode(t, status, body, http.StatusConflict, "run_result_invalid")
	if m := decode(t, body); len(m["invalid_reasons"].([]any)) != 1 {
		t.Fatalf("invalid_reasons = %v", m["invalid_reasons"])
	}

	status, body = post(t, srv, "/api/analysis/metrics", `{"run_id":"failed"}`)
	wantCode(t, status, body, http.StatusConflict, "run_result_failed")

	status, body = post(t, srv, "/api/analysis/metrics", `{"run_id":"empty"}`)
	wantCode(t, status, body, http.StatusConflict, "run_result_incomplete")
}

func TestDependencyUnavailable(t *testing.T) {
	srv, _ := newTestServer(t, analysis.Options{})
	status, body := post(t, srv, "/api/analysis/metrics", `{"run_id":"r1"}`)
	wantCode(t, status, body, http.StatusServiceUnavailable, "dependency_unavailable")

	status, body = post(t, srv, "/api/analysis/sensitivity", `{"run_id":"r1","param_grid":[{"name":"lb","values":[10]}]}`)
	wantCode(t, status, body, http.StatusServiceUnavailable, "dependency_unavailable")
}

func TestSensitivityPendingAndNotReadyExport(t *testing.T) {
	queue := &stubQueue{}
	srv, _ := newTestServer(t, analysis.Options{
		Results: &stubReader{results: map[string]*analysis.BacktestResponse{"r1": sampleResult("r1")}},
		Queue:   queue,
	})
	status, body := post(t, srv, "/api/analysis/sensitivity", `{"run_id":"r1","param_grid":[{"name":"lb","values":[10,20]}]}`)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	m := decode(t, body)
	if m["status"] != "pending" {
		t.Fatalf("job response = %v", m)
	}
	if queue.submitted != 1 {
		t.Fatalf("queue submitted = %d", queue.submitted)
	}
	reportID := m["report_id"].(string)
	if r, b := get(t, srv, "/api/analysis/reports/"+reportID); r.StatusCode != http.StatusOK {
		t.Fatalf("GET pending report = %d %s", r.StatusCode, b)
	}
	resp, err := http.Get(srv.URL + "/api/analysis/reports/" + reportID + "/export?format=html")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	wantCode(t, resp.StatusCode, out, http.StatusConflict, "report_not_ready")
}

func TestBenchmarkSuccessAndDataUnavailable(t *testing.T) {
	result := sampleResult("r1")
	ref := analysis.BenchmarkRef{
		Code:            "CSI300",
		AdjustmentBasis: "total_return",
		DatasetHash:     "h1",
		RegisteredAt:    testDate,
	}
	store := analysis.NewMemory()
	if err := store.RegisterBenchmark(context.Background(), ref); err != nil {
		t.Fatalf("RegisterBenchmark: %v", err)
	}
	points := make([]analysis.BenchmarkPoint, 0, len(result.PortfolioValues))
	value := 100.0
	for _, pv := range result.PortfolioValues {
		points = append(points, analysis.BenchmarkPoint{Date: pv.Date, Value: value})
		value *= 1.002
	}
	srv, _ := newTestServer(t, analysis.Options{
		Store:      store,
		Results:    &stubReader{results: map[string]*analysis.BacktestResponse{"r1": result}},
		Benchmarks: &stubBenchmarks{series: analysis.BenchmarkSeries{Ref: ref, Points: points}},
	})
	status, body := post(t, srv, "/api/analysis/benchmark", `{"run_id":"r1","benchmark_code":"CSI300"}`)
	if status != http.StatusOK {
		t.Fatalf("benchmark status = %d, body = %s", status, body)
	}
	m := decode(t, body)
	benchmark, ok := m["benchmark"].(map[string]any)
	if !ok || benchmark["dataset_hash"] != "h1" || benchmark["adjustment_basis"] != "total_return" {
		t.Fatalf("benchmark ref = %v", m["benchmark"])
	}
	if _, ok := m["excess_return"]; !ok {
		t.Fatalf("excess_return missing: %v", m)
	}
	if _, ok := m["beta"]; !ok {
		t.Fatalf("beta missing: %v", m)
	}

	// 已登记但取不到序列 ⇒ 409 benchmark_data_unavailable
	srv2, _ := newTestServer(t, analysis.Options{
		Store:      store,
		Results:    &stubReader{results: map[string]*analysis.BacktestResponse{"r1": result}},
		Benchmarks: &stubBenchmarks{err: analysis.ErrBenchmarkDataUnavailable},
	})
	status, body = post(t, srv2, "/api/analysis/benchmark", `{"run_id":"r1","benchmark_code":"CSI300"}`)
	wantCode(t, status, body, http.StatusConflict, "benchmark_data_unavailable")
}

func TestAttributionAndCorrelationWireCodes(t *testing.T) {
	srv, _ := newTestServer(t, analysis.Options{
		Results: &stubReader{results: map[string]*analysis.BacktestResponse{"r1": sampleResult("r1")}},
	})
	status, body := post(t, srv, "/api/analysis/attribution", `{"run_id":"r1","method":"ic"}`)
	wantCode(t, status, body, http.StatusBadRequest, "method_not_supported")

	status, body = post(t, srv, "/api/analysis/correlation", `{"run_ids":["r1"]}`)
	wantCode(t, status, body, http.StatusBadRequest, "invalid_params")

	status, body = post(t, srv, "/api/analysis/correlation", `{"run_ids":["r1","missing"]}`)
	wantCode(t, status, body, http.StatusNotFound, "run_not_found")

	// 基准未登记 ⇒ 404（不自动抓数据）
	status, body = post(t, srv, "/api/analysis/benchmark", `{"run_id":"r1","benchmark_code":"CSI300"}`)
	wantCode(t, status, body, http.StatusNotFound, "benchmark_not_registered")
}

func TestReportNotFoundAndExportFormat(t *testing.T) {
	srv, _ := newTestServer(t, analysis.Options{})
	resp, err := http.Get(srv.URL + "/api/analysis/reports/nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	wantCode(t, resp.StatusCode, out, http.StatusNotFound, "report_not_found")

	resp2, err := http.Get(srv.URL + "/api/analysis/reports/nope/export?format=pdf")
	if err != nil {
		t.Fatalf("GET export: %v", err)
	}
	defer resp2.Body.Close()
	out2, _ := io.ReadAll(resp2.Body)
	// 报告不存在优先于格式校验（先 404，不泄漏格式策略）
	wantCode(t, resp2.StatusCode, out2, http.StatusNotFound, "report_not_found")

	resp3, err := http.Get(srv.URL + "/api/analysis/reports/nope/export")
	if err != nil {
		t.Fatalf("GET export missing format: %v", err)
	}
	defer resp3.Body.Close()
	out3, _ := io.ReadAll(resp3.Body)
	wantCode(t, resp3.StatusCode, out3, http.StatusBadRequest, "invalid_params")
}

// run_result_incomplete 必须带机器可读的 missing（docs/api-schema.md §2 / §6），
// 调用方不应解析自由文本 message。
func TestIncompleteErrorCarriesMissingField(t *testing.T) {
	srv, _ := newTestServer(t, analysis.Options{
		Results: &stubReader{results: map[string]*analysis.BacktestResponse{
			"empty": {Status: analysis.ResultCompleted},
		}},
	})
	status, body := post(t, srv, "/api/analysis/metrics", `{"run_id":"empty"}`)
	wantCode(t, status, body, http.StatusConflict, "run_result_incomplete")
	if m := decode(t, body); m["missing"] != "portfolio_values" {
		t.Fatalf("missing = %v, want portfolio_values", m["missing"])
	}
}
