package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tellurion-analytics/internal/analysis"
)

var testStart = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

func resultJSON(status string, extra map[string]any) []byte {
	body := map[string]any{
		"id":     "run-1",
		"status": status,
		"portfolio_values": []map[string]any{
			{"date": "2026-01-02T00:00:00Z", "total_value": 100},
			{"date": "2026-01-05T00:00:00Z", "total_value": 101},
		},
	}
	for k, v := range extra {
		body[k] = v
	}
	b, _ := json.Marshal(body)
	return b
}

func TestResultReader_K9Mapping(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    []byte
		wantErr error
		check   func(t *testing.T, err error)
	}{
		{name: "completed", status: http.StatusOK, body: resultJSON("completed", nil)},
		{
			name: "invalid with reasons", status: http.StatusOK,
			body: resultJSON("invalid", map[string]any{"invalid_reasons": []string{"data_gap_skipped"}}),
			check: func(t *testing.T, err error) {
				var invalid *analysis.InvalidReasonsError
				if !errors.As(err, &invalid) || len(invalid.Reasons) != 1 || invalid.Reasons[0] != "data_gap_skipped" {
					t.Fatalf("want InvalidReasonsError[data_gap_skipped], got %v", err)
				}
			},
		},
		{
			name: "failed", status: http.StatusOK,
			body: resultJSON("failed", map[string]any{"error": "boom"}),
			check: func(t *testing.T, err error) {
				var failed *analysis.FailedError
				if !errors.As(err, &failed) || failed.Message != "boom" {
					t.Fatalf("want FailedError(boom), got %v", err)
				}
			},
		},
		{name: "not found", status: http.StatusNotFound, wantErr: analysis.ErrRunNotFound},
		{
			name: "upstream failure", status: http.StatusBadGateway,
			body: []byte("upstream down"), wantErr: analysis.ErrDependencyUnavailable,
		},
		{
			name: "missing status fails closed", status: http.StatusOK,
			body: resultJSON("", nil),
			check: func(t *testing.T, err error) {
				var incomplete *analysis.IncompleteError
				if !errors.As(err, &incomplete) {
					t.Fatalf("want IncompleteError, got %v", err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/backtest/run-1/report" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			defer srv.Close()

			reader := NewResultReader(srv.URL, srv.Client())
			got, err := reader.GetResult(context.Background(), "run-1")
			if tc.check != nil {
				tc.check(t, err)
				return
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("want %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetResult: %v", err)
			}
			if got.Status != analysis.ResultCompleted || len(got.PortfolioValues) != 2 {
				t.Fatalf("unexpected result: %+v", got)
			}
		})
	}
}

// TestResultReader_PersistentK9ReadPlane 把 ResultReader 钉在 core B6-3（`8537ffd`）
// 落地的 R1 持久读面上：两轴不合并（`backtest_jobs.status` = 队列生命周期，
// `result_status` = K9 结论），report 端点以 `result_status` 为 `status` 的唯一权威
// （列 → 结果体 → completed），且只有队列态 completed 的行可读。
//
// 这里的 handler 是 core 那条 overlay 的**模型**（源：core
// `cmd/analysis/handlers_backtest.go` 的 `GET /:id/report`）：core 侧改了 overlay，
// 本用例必须同批改，否则「本仓以为的读面」和真读面各走各的。
func TestResultReader_PersistentK9ReadPlane(t *testing.T) {
	tests := []struct {
		name          string
		queueStatus   string // backtest_jobs.status
		resultStatus  string // backtest_jobs.result_status（"" = NULL）
		bodyHasStatus bool   // 结果体里带不带 status（异步旧体不带）
		wantErr       error
		check         func(t *testing.T, err error)
	}{
		{
			name:         "async valid: body has no status but column says completed",
			queueStatus:  "completed",
			resultStatus: "completed",
		},
		{
			name:          "async invalid: readable as 200 with reasons (was 404 pre-B6-3)",
			queueStatus:   "completed",
			resultStatus:  "invalid",
			bodyHasStatus: true,
			check: func(t *testing.T, err error) {
				var invalid *analysis.InvalidReasonsError
				if !errors.As(err, &invalid) || len(invalid.Reasons) != 1 || invalid.Reasons[0] != "data_gap_skipped" {
					t.Fatalf("want InvalidReasonsError[data_gap_skipped], got %v", err)
				}
			},
		},
		{
			name:         "invalid without reasons: still invalid, no completed fallback",
			queueStatus:  "completed",
			resultStatus: "invalid",
			check: func(t *testing.T, err error) {
				var withReasons *analysis.InvalidReasonsError
				if !errors.Is(err, analysis.ErrRunInvalid) || errors.As(err, &withReasons) {
					t.Fatalf("want bare ErrRunInvalid, got %v", err)
				}
			},
		},
		{
			name:         "legacy row: no status anywhere -> core falls back to completed",
			queueStatus:  "completed",
			resultStatus: "",
		},
		{
			// 偏差（docs/ARCHITECTURE.md §9.7 R1）：端点仍要求队列态 completed，
			// 所以 K9 的 failed 经持久面到不了本仓的 409 run_result_failed。
			name:         "engine failure: queue status failed -> 404 -> run_not_found",
			queueStatus:  "failed",
			resultStatus: "failed",
			wantErr:      analysis.ErrRunNotFound,
		},
		{
			name:        "still running: queue running + NULL column -> 404 -> run_not_found",
			queueStatus: "running",
			wantErr:     analysis.ErrRunNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/backtest/run-1/report" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				if tc.queueStatus != "completed" {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"error":"backtest not found or not completed"}`))
					return
				}
				report := map[string]any{
					"id": "run-1",
					"portfolio_values": []map[string]any{
						{"date": "2026-01-02T00:00:00Z", "total_value": 100},
						{"date": "2026-01-05T00:00:00Z", "total_value": 101},
					},
				}
				if tc.bodyHasStatus {
					report["status"] = tc.resultStatus
					if tc.resultStatus == "invalid" {
						report["invalid_reasons"] = []string{"data_gap_skipped"}
					}
				}
				// B6-3 的回落顺序：列 → 结果体 → completed。
				status := tc.resultStatus
				if status == "" {
					status, _ = report["status"].(string)
				}
				if status == "" {
					status = "completed"
				}
				report["status"] = status
				_ = json.NewEncoder(w).Encode(report)
			}))
			defer srv.Close()

			reader := NewResultReader(srv.URL, srv.Client())
			got, err := reader.GetResult(context.Background(), "run-1")
			if tc.check != nil {
				tc.check(t, err)
				return
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("want %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetResult: %v", err)
			}
			if got.Status != analysis.ResultCompleted || len(got.PortfolioValues) != 2 {
				t.Fatalf("unexpected result: %+v", got)
			}
		})
	}
}

// TestUpstream_SendsServiceToken（裁-15）：core / data 上了鉴权闸后，三个
// reader 都必须带 Bearer；空 token 不发头（回环免密形态）。
func TestUpstream_SendsServiceToken(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resultJSON("completed", nil))
	}))
	defer srv.Close()

	reader := NewResultReaderWithToken(srv.URL, srv.Client(), " svc-token ")
	if _, err := reader.GetResult(context.Background(), "run-1"); err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if len(got) != 1 || got[0] != "Bearer svc-token" {
		t.Fatalf("want Bearer svc-token, got %v", got)
	}

	plain := NewResultReader(srv.URL, srv.Client())
	got = nil
	if _, err := plain.GetResult(context.Background(), "run-1"); err != nil {
		t.Fatalf("GetResult: %v", err)
	}
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("empty token must not send Authorization, got %v", got)
	}
}

func TestResultReader_EmptyRunID(t *testing.T) {
	reader := NewResultReader("http://127.0.0.1:1", nil)
	if _, err := reader.GetResult(context.Background(), "  "); !errors.Is(err, analysis.ErrRunNotFound) {
		t.Fatalf("want ErrRunNotFound, got %v", err)
	}
}

func TestBenchmarkReader_X1b(t *testing.T) {
	var resolveQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/datasets/resolve":
			resolveQuery = r.URL.RawQuery
			_ = json.NewEncoder(w).Encode(map[string]any{
				"dataset_hash": "ds-1",
				"metadata":     map[string]any{"adjustment_basis": "qfq"},
			})
		case r.URL.Path == "/datasets/ds-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hash":     "ds-1",
				"metadata": map[string]any{"adjustment_basis": "qfq"},
				"bars": map[string]any{
					"000300.SH": []map[string]any{
						{"date": "2026-01-05T00:00:00Z", "close": 101.5},
						{"date": "2026-01-02T00:00:00Z", "close": 100},
					},
				},
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	reader := NewBenchmarkReader(srv.URL, srv.Client())
	series, err := reader.GetSeries(context.Background(), "000300.SH", "qfq", testStart, testStart.AddDate(0, 0, 3))
	if err != nil {
		t.Fatalf("GetSeries: %v", err)
	}
	if len(series.Points) != 2 || series.Points[0].Value != 100 || series.Points[1].Value != 101.5 {
		t.Fatalf("unexpected points: %+v", series.Points)
	}
	if series.Ref.DatasetHash != "ds-1" || series.Ref.AdjustmentBasis != "qfq" {
		t.Fatalf("unexpected ref: %+v", series.Ref)
	}
	if resolveQuery == "" {
		t.Fatal("resolve was not called")
	}
	for _, want := range []string{"adjustment=qfq", "freq=1d", "symbols=000300.SH"} {
		if !strings.Contains(resolveQuery, want) {
			t.Fatalf("resolve query %q missing %q", resolveQuery, want)
		}
	}
}

func TestBenchmarkReader_BasisMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"dataset_hash": "ds-1",
			"metadata":     map[string]any{"adjustment_basis": "hfq"},
		})
	}))
	defer srv.Close()

	reader := NewBenchmarkReader(srv.URL, srv.Client())
	_, err := reader.GetSeries(context.Background(), "000300.SH", "qfq", testStart, testStart.AddDate(0, 0, 3))
	if !errors.Is(err, analysis.ErrBenchmarkDataUnavailable) {
		t.Fatalf("want ErrBenchmarkDataUnavailable, got %v", err)
	}
}

func TestBenchmarkReader_MissingBars(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/datasets/resolve" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"dataset_hash": "ds-1",
				"metadata":     map[string]any{"adjustment_basis": "qfq"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hash":     "ds-1",
			"metadata": map[string]any{"adjustment_basis": "qfq"},
			"bars":     map[string]any{},
		})
	}))
	defer srv.Close()

	reader := NewBenchmarkReader(srv.URL, srv.Client())
	_, err := reader.GetSeries(context.Background(), "000300.SH", "qfq", testStart, testStart.AddDate(0, 0, 3))
	if !errors.Is(err, analysis.ErrBenchmarkDataUnavailable) {
		t.Fatalf("want ErrBenchmarkDataUnavailable, got %v", err)
	}
}

func TestArtifactReader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runs/run-1/portfolio-snapshot":
			_ = json.NewEncoder(w).Encode(analysis.PortfolioSnapshot{
				Holdings: []analysis.Holding{
					{Symbol: "A", Sector: "tech", Weight: 0.6, PeriodReturn: 0.12},
					{Symbol: "B", Sector: "fin", Weight: 0.4, PeriodReturn: 0.07},
				},
				BenchmarkSectorWeights: map[string]float64{"tech": 0.5, "fin": 0.5},
				BenchmarkSectorReturns: map[string]float64{"tech": 0.10, "fin": 0.08},
				BenchmarkReturn:        0.09,
				DatasetHash:            "artifact-1",
			})
		case "/runs/run-1/factor-exposures":
			_ = json.NewEncoder(w).Encode(analysis.FactorExposures{
				Factors: []analysis.FactorExposure{
					{Name: "value", Exposure: 1, FactorReturn: 0.06},
					{Name: "size", Exposure: 1, FactorReturn: 0.02},
				},
				DatasetHash: "factor-1",
			})
		case "/runs/missing/portfolio-snapshot", "/runs/missing/factor-exposures":
			w.WriteHeader(http.StatusNotFound)
		case "/runs/empty/portfolio-snapshot":
			_ = json.NewEncoder(w).Encode(analysis.PortfolioSnapshot{})
		case "/runs/empty/factor-exposures":
			_ = json.NewEncoder(w).Encode(analysis.FactorExposures{})
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	reader := NewArtifactReader(srv.URL, srv.Client())

	snap, err := reader.GetPortfolioSnapshot(context.Background(), "run-1")
	if err != nil || len(snap.Holdings) != 2 || snap.DatasetHash != "artifact-1" {
		t.Fatalf("snapshot: %+v err=%v", snap, err)
	}
	exposures, err := reader.GetFactorExposures(context.Background(), "run-1")
	if err != nil || len(exposures.Factors) != 2 || exposures.DatasetHash != "factor-1" {
		t.Fatalf("exposures: %+v err=%v", exposures, err)
	}

	if _, err := reader.GetPortfolioSnapshot(context.Background(), "missing"); !isIncomplete(err, "holdings") {
		t.Fatalf("missing snapshot: want incomplete(holdings), got %v", err)
	}
	if _, err := reader.GetFactorExposures(context.Background(), "missing"); !isIncomplete(err, "factor_exposures") {
		t.Fatalf("missing exposures: want incomplete(factor_exposures), got %v", err)
	}
	if _, err := reader.GetPortfolioSnapshot(context.Background(), "empty"); !isIncomplete(err, "holdings") {
		t.Fatalf("empty snapshot: want incomplete(holdings), got %v", err)
	}
	if _, err := reader.GetFactorExposures(context.Background(), "empty"); !isIncomplete(err, "factor_exposures") {
		t.Fatalf("empty exposures: want incomplete(factor_exposures), got %v", err)
	}
}

func isIncomplete(err error, missing string) bool {
	var incomplete *analysis.IncompleteError
	return errors.As(err, &incomplete) && incomplete.Missing == missing
}
