package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"tellurion-analytics/internal/analysis"
)

// TestAuthNotWiredCurrentState 是「鉴权现状登记」的可执行锚点（docs/api-schema.md §7）。
//
// 今天没有鉴权：不带 Authorization、甚至带无效 Bearer 也能通。一旦接线 QT-50 ④，
// 本用例应当转红——那时把它改写成 401（无凭证 / 凭证无效）的断言，并同步更新
// docs/api-schema.md §7 与 docs/TASKS.md，而不是删掉了事。
func TestAuthNotWiredCurrentState(t *testing.T) {
	if AuthStatus != "unwired" {
		t.Fatalf("AuthStatus = %q；鉴权接线必须同步 docs/api-schema.md §7 与本用例", AuthStatus)
	}
	srv, _ := newTestServer(t, analysis.Options{
		Results: &stubReader{results: map[string]*analysis.BacktestResponse{"r1": sampleResult("r1")}},
	})
	for _, header := range []string{"", "Bearer not-a-real-token"} {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/analysis/metrics", strings.NewReader(`{"run_id":"r1"}`))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("request with Authorization=%q: %v", header, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("auth is expected to be unwired today; Authorization=%q got %d", header, resp.StatusCode)
		}
	}
}
