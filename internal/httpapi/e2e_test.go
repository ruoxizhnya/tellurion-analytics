package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"tellurion-analytics/internal/analysis"
	"tellurion-analytics/internal/upstream"
)

// TestEndToEndWithPostgres 是真库端到端验收：PG Store + 启动迁移 + 三个上游
// 读取适配器 + 8 端点 HTTP 面。
//
// 未设置 ANALYTICS_TEST_DSN 时跳过（CI 无 PG）。本地验证：
//
//	ANALYTICS_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_analytics_test?sslmode=disable' \
//	  go test ./internal/httpapi -run EndToEnd -count=1 -v
func TestEndToEndWithPostgres(t *testing.T) {
	dsn := os.Getenv("ANALYTICS_TEST_DSN")
	if dsn == "" {
		t.Skip("ANALYTICS_TEST_DSN is not set; skipping PostgreSQL end-to-end test")
	}
	ctx := context.Background()
	store, err := analysis.OpenPG(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenPG: %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	runID := "e2e-run-" + suffix
	benchCode := "E2E.BENCH." + suffix
	registeredHash := "registered-" + suffix
	// 基准登记是运维前置（本仓没有登记端点，见 docs/api-schema.md §1）。
	if err := store.RegisterBenchmark(ctx, analysis.BenchmarkRef{
		Code:            benchCode,
		AdjustmentBasis: "qfq",
		DatasetHash:     registeredHash,
		Source:          "e2e",
		RegisteredAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RegisterBenchmark: %v", err)
	}

	fixture := newE2EUpstream(t, runID, benchCode, suffix)
	artifacts := upstream.NewArtifactReader(fixture.URL, fixture.Client())
	srv, _ := newTestServer(t, analysis.Options{
		Store:      store,
		Results:    upstream.NewResultReader(fixture.URL, fixture.Client()),
		Benchmarks: upstream.NewBenchmarkReader(fixture.URL, fixture.Client()),
		Holdings:   artifacts,
		Factors:    artifacts,
		Executor:   analysis.InlineExecutor{},
	})

	runBody := fmt.Sprintf(`{"run_id":%q}`, runID)

	// 1) 同 run 两次 metrics：报告字节一致（同键重放，不重算）。
	status1, metrics1 := post(t, srv, "/api/analysis/metrics", runBody)
	if status1 != http.StatusOK {
		t.Fatalf("metrics: %d %s", status1, metrics1)
	}
	status2, metrics2 := post(t, srv, "/api/analysis/metrics", runBody)
	if status2 != http.StatusOK || !bytes.Equal(metrics1, metrics2) {
		t.Fatalf("metrics replay mismatch: %d/%d\n%s\n%s", status1, status2, metrics1, metrics2)
	}
	reportID, _ := decode(t, metrics1)["report_id"].(string)
	if reportID == "" {
		t.Fatal("metrics response has no report_id")
	}

	// 报告确实落在 PG（另开连接读取，排除进程内缓存）。
	other, err := analysis.OpenPG(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenPG (reader): %v", err)
	}
	defer other.Close()
	stored, err := other.GetReport(ctx, reportID)
	if err != nil {
		t.Fatalf("GetReport from PG: %v", err)
	}
	if stored.Status != analysis.ReportCompleted || len(stored.Body) == 0 || stored.RunID != runID {
		t.Fatalf("unexpected stored report: %+v", stored)
	}

	// 2) 同 run + 同 seed 两次蒙特卡洛：同一个 report_id，报告字节一致。
	mcBody := fmt.Sprintf(`{"run_id":%q,"n":200,"seed":42,"method":"bootstrap"}`, runID)
	mcStatus1, mc1 := post(t, srv, "/api/analysis/montecarlo", mcBody)
	if mcStatus1 != http.StatusAccepted {
		t.Fatalf("montecarlo: %d %s", mcStatus1, mc1)
	}
	mcID1, _ := decode(t, mc1)["report_id"].(string)
	_, mcReport1 := getReport(t, srv, mcID1)
	mcStatus2, mc2 := post(t, srv, "/api/analysis/montecarlo", mcBody)
	if mcStatus2 != http.StatusAccepted {
		t.Fatalf("montecarlo replay: %d %s", mcStatus2, mc2)
	}
	mcID2, _ := decode(t, mc2)["report_id"].(string)
	_, mcReport2 := getReport(t, srv, mcID2)
	if mcID1 != mcID2 || !bytes.Equal(mcReport1, mcReport2) {
		t.Fatalf("montecarlo seed replay mismatch: ids %s/%s\n%s\n%s", mcID1, mcID2, mcReport1, mcReport2)
	}

	// 3) 缺 seed 直接拒绝。
	noSeed := fmt.Sprintf(`{"run_id":%q,"n":10}`, runID)
	status, body := post(t, srv, "/api/analysis/montecarlo", noSeed)
	wantCode(t, status, body, http.StatusBadRequest, "seed_required")

	// 4) 因子归因：Σcontributions + residual ≈ total_return（容差 1e-8）。
	status, body = post(t, srv, "/api/analysis/attribution",
		fmt.Sprintf(`{"run_id":%q,"method":"factor"}`, runID))
	if status != http.StatusOK {
		t.Fatalf("factor attribution: %d %s", status, body)
	}
	factor := decode(t, body)
	if got := factorSum(t, factor) + number(t, factor["residual"]); math.Abs(got-number(t, factor["total_return"])) > 1e-8 {
		t.Fatalf("factor identity broken: sum+residual=%v total_return=%v", got, factor["total_return"])
	}

	// 5) Brinson 归因：Σ(allocation+selection+interaction) ≈ total_return - benchmark_return。
	status, body = post(t, srv, "/api/analysis/attribution",
		fmt.Sprintf(`{"run_id":%q,"method":"brinson"}`, runID))
	if status != http.StatusOK {
		t.Fatalf("brinson attribution: %d %s", status, body)
	}
	brinson := decode(t, body)
	want := number(t, brinson["total_return"]) - number(t, brinson["benchmark_return"])
	if got := factorSum(t, brinson); math.Abs(got-want) > 1e-8 {
		t.Fatalf("brinson identity broken: sum=%v want=%v", got, want)
	}

	// 6) 基准对比：走 X1b 适配器（resolve + snapshot）。
	status, body = post(t, srv, "/api/analysis/benchmark",
		fmt.Sprintf(`{"run_id":%q,"benchmark_code":%q}`, runID, benchCode))
	if status != http.StatusOK {
		t.Fatalf("benchmark: %d %s", status, body)
	}
	bench := decode(t, body)
	if bench["benchmark"] == nil || bench["tracking_error"] == nil {
		t.Fatalf("benchmark payload missing fields: %v", bench)
	}
}

// newE2EUpstream 模拟三个上游读取面（K9 结果 / X1b 数据集 / R2 artifact）。
//
// R1 的 K9-correct 读取面尚未在 core 落地（docs/ARCHITECTURE.md §9.7 R1），
// 这里按 ResultReader 的冻结语义返回 status 字段，用于验证**本仓**的接线；
// 它不是 core 的替代实现。
func newE2EUpstream(t *testing.T, runID, benchCode, suffix string) *httptest.Server {
	t.Helper()

	values := make([]analysis.PortfolioValue, 0, 40)
	bars := make([]map[string]any, 0, 40)
	value := 100.0
	for i := 0; i < 40; i++ {
		day := testDate.AddDate(0, 0, i)
		values = append(values, analysis.PortfolioValue{Date: day, TotalValue: value})
		bars = append(bars, map[string]any{
			"date":  day.Format(time.RFC3339),
			"close": 100 * math.Pow(1.0005, float64(i)),
		})
		if i%2 == 0 {
			value *= 1.01
		} else {
			value *= 0.995
		}
	}
	result := analysis.BacktestResponse{
		ID:              runID,
		Status:          analysis.ResultCompleted,
		TotalReturn:     0.10,
		AnnualReturn:    0.20,
		SharpeRatio:     1.234,
		PortfolioValues: values,
	}
	datasetHash := "x1b-" + suffix

	mux := http.NewServeMux()
	mux.HandleFunc("/api/backtest/"+runID+"/report", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(result)
	})
	mux.HandleFunc("/datasets/resolve", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"dataset_hash": datasetHash,
			"metadata":     map[string]any{"adjustment_basis": "qfq"},
		})
	})
	mux.HandleFunc("/datasets/"+datasetHash, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hash":     datasetHash,
			"metadata": map[string]any{"adjustment_basis": "qfq"},
			"bars":     map[string]any{benchCode: bars},
		})
	})
	mux.HandleFunc("/runs/"+runID+"/portfolio-snapshot", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(analysis.PortfolioSnapshot{
			Holdings: []analysis.Holding{
				{Symbol: "A", Sector: "tech", Weight: 0.6, PeriodReturn: 0.12},
				{Symbol: "B", Sector: "fin", Weight: 0.4, PeriodReturn: 0.07},
			},
			BenchmarkSectorWeights: map[string]float64{"tech": 0.5, "fin": 0.5},
			BenchmarkSectorReturns: map[string]float64{"tech": 0.10, "fin": 0.08},
			BenchmarkReturn:        0.09,
			DatasetHash:            "artifact-" + suffix,
		})
	})
	mux.HandleFunc("/runs/"+runID+"/factor-exposures", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(analysis.FactorExposures{
			Factors: []analysis.FactorExposure{
				{Name: "value", Exposure: 1, FactorReturn: 0.06},
				{Name: "size", Exposure: 1, FactorReturn: 0.02},
			},
			DatasetHash: "factor-" + suffix,
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// getReport 轮询报告直到 completed（InlineExecutor 下第一次即可）。
func getReport(t *testing.T, srv *httptest.Server, reportID string) (map[string]any, []byte) {
	t.Helper()
	_, body := get(t, srv, "/api/analysis/reports/"+reportID)
	rep := decode(t, body)
	if rep["status"] != string(analysis.ReportCompleted) {
		t.Fatalf("report %s is not completed: %v", reportID, rep)
	}
	return rep, body
}

// factorSum 求 contributions 的贡献和。
func factorSum(t *testing.T, payload map[string]any) float64 {
	t.Helper()
	raw, ok := payload["contributions"].([]any)
	if !ok {
		t.Fatalf("contributions missing: %v", payload)
	}
	sum := 0.0
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("unexpected contribution entry: %v", item)
		}
		sum += number(t, entry["contribution"])
	}
	return sum
}

func number(t *testing.T, v any) float64 {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("not a number: %v", v)
	}
	return f
}
