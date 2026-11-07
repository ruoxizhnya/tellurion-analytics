package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"tellurion-analytics/internal/analysis"
)

// TestG4UpstreamFailureSemantics 钉住 SVC-AN-G4 ② 的五类上游失败（结果面）。
//
// 口径表见 docs/api-schema.md §2.2：
//   - 401 / 409 / 503 → 本仓 503 dependency_unavailable（不向上游透传 401/403 细节）；
//   - 404（run 不存在）→ 本仓 404 run_not_found（终态，不重试）；
//   - 超时 → 503 dependency_unavailable（见 TestG4OutboundRequestHasTimeoutBound）。
//
// 「是否重试」是调用方策略：401 属配置错误，修凭证/配置后重试；409/503/超时
// 属上游可用性，退避重试；404 不重试。代码只负责把上游状态折成稳定哨兵，
// 重试策略记录在 §2.2，不在这里发明第二套判据。
func TestG4UpstreamFailureSemantics(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr error
	}{
		{"401 upstream credentials wrong", http.StatusUnauthorized, analysis.ErrDependencyUnavailable},
		{"404 run does not exist", http.StatusNotFound, analysis.ErrRunNotFound},
		{"409 conclusion not ready", http.StatusConflict, analysis.ErrDependencyUnavailable},
		{"503 upstream dependency unwired", http.StatusServiceUnavailable, analysis.ErrDependencyUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"upstream said no"}`))
			}))
			defer srv.Close()

			reader := NewResultReader(srv.URL, srv.Client())
			_, err := reader.GetResult(context.Background(), "run-1")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("upstream %d: want %v, got %v", tc.status, tc.wantErr, err)
			}
			t.Logf("upstream %d -> %v", tc.status, err)
		})
	}
}

// TestG4BenchmarkAndArtifactFailureSemantics 钉住同一批上游状态在另外两个读取面
// 的折算差异（docs/api-schema.md §2.1 / §2.2）：基准面任意非 2xx 都是
// benchmark_data_unavailable；artifact 面的 404 是「输入缺失」而不是「run 不存在」。
func TestG4BenchmarkAndArtifactFailureSemantics(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusConflict, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))

		bench := NewBenchmarkReader(srv.URL, srv.Client())
		_, err := bench.GetSeries(context.Background(), "000300.SH", "qfq", testStart, testStart)
		if !errors.Is(err, analysis.ErrBenchmarkDataUnavailable) {
			t.Fatalf("benchmark upstream %d: want ErrBenchmarkDataUnavailable, got %v", status, err)
		}
		srv.Close()
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	artifacts := NewArtifactReader(srv.URL, srv.Client())
	_, err := artifacts.GetPortfolioSnapshot(context.Background(), "run-1")
	var incomplete *analysis.IncompleteError
	if !errors.As(err, &incomplete) || incomplete.Missing != "holdings" {
		t.Fatalf("artifact 404: want incomplete(holdings), got %v", err)
	}
}
