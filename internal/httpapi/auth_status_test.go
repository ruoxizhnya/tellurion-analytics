package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ruoxizhnya/tellurion-kit/auth"

	"tellurion-analytics/internal/analysis"
)

// TestRouteTableEqualsPolicyTable 是本仓的鉴权守卫：8 个注册路由与策略表必须
// 双向一一对应。漏登策略 / 表里写了不存在的路由都会红。
func TestRouteTableEqualsPolicyTable(t *testing.T) {
	srv := New(nil)
	routes := srv.routePatterns()
	policy := PolicyEntries()

	if len(routes) != 8 {
		t.Fatalf("route table has %d entries, want 8: %v", len(routes), routes)
	}
	if len(routes) != len(policy) {
		t.Fatalf("route table has %d entries, policy has %d", len(routes), len(policy))
	}
	seen := make(map[string]bool, len(routes))
	for _, r := range routes {
		if _, ok := policy[r]; !ok {
			t.Errorf("route %q has no policy entry", r)
		}
		if seen[r] {
			t.Errorf("route %q registered twice", r)
		}
		seen[r] = true
	}
	for k := range policy {
		if !seen[k] {
			t.Errorf("policy entry %q has no registered route", k)
		}
	}
}

func analyticsToken(t *testing.T, svc *auth.TokenService, username, role string) string {
	t.Helper()
	tok, _, err := svc.IssueTokens(1, username, role)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return tok
}

// TestAuthEnforcedCurrentState 是「鉴权现状登记」的可执行锚点（docs/api-schema.md §7）。
//
// 2026-10-25 起鉴权已接线：无凭证 / 坏 token → 401，viewer 打 POST → 403，
// 6 个 POST = trader、2 个 GET = viewer。AuthStatus 改回 unwired 或鉴权被摘掉，本用例红。
func TestAuthEnforcedCurrentState(t *testing.T) {
	if AuthStatus != "enforced" {
		t.Fatalf("AuthStatus = %q；鉴权变更必须同步 docs/api-schema.md §7 与本用例", AuthStatus)
	}
	svc := auth.NewTokenService(auth.Config{JWTSecret: []byte("analytics-auth-test")})
	if !svc.Enabled() {
		t.Fatal("token service must be enabled")
	}
	srv, _ := newTestServerWithAuthorizer(t, analysis.Options{
		Results: &stubReader{results: map[string]*analysis.BacktestResponse{"r1": sampleResult("r1")}},
	}, NewAuthorizer(svc))

	// 全部 8 条路由：无凭证一律 401（闸在 handler 之前）。
	for _, pattern := range New(nil).routePatterns() {
		method, path := splitPattern(pattern)
		status, body := doRequest(t, srv, method, path, "", "")
		wantCode(t, status, body, http.StatusUnauthorized, "unauthenticated")
	}

	// 坏 token 也是 401。
	status, body := doRequest(t, srv, http.MethodPost, "/api/analysis/metrics", `{"run_id":"r1"}`, "not-a-real-token")
	wantCode(t, status, body, http.StatusUnauthorized, "unauthenticated")

	// 6 个 POST = trader：viewer 全 403。
	viewer := analyticsToken(t, svc, "alice", "viewer")
	trader := analyticsToken(t, svc, "bob", "trader")
	for _, pattern := range New(nil).routePatterns() {
		method, path := splitPattern(pattern)
		if method != http.MethodPost {
			continue
		}
		status, body := doRequest(t, srv, method, path, `{"run_id":"r1"}`, viewer)
		wantCode(t, status, body, http.StatusForbidden, "forbidden")
	}

	// trader 打 metrics 通过鉴权并真正执行。
	status, body = doRequest(t, srv, http.MethodPost, "/api/analysis/metrics", `{"run_id":"r1"}`, trader)
	if status != http.StatusOK {
		t.Fatalf("trader metrics: %d %s", status, body)
	}
	reportID, _ := decode(t, body)["report_id"].(string)
	if reportID == "" {
		t.Fatalf("metrics response has no report_id: %s", body)
	}

	// 2 个 GET = viewer：无 token 401 已覆盖，带 viewer 应 200。
	status, body = doRequest(t, srv, http.MethodGet, "/api/analysis/reports/"+reportID, "", viewer)
	if status != http.StatusOK {
		t.Fatalf("viewer get report: %d %s", status, body)
	}
	status, body = doRequest(t, srv, http.MethodGet, "/api/analysis/reports/"+reportID+"/export?format=html", "", viewer)
	if status != http.StatusOK {
		t.Fatalf("viewer export: %d %s", status, body)
	}

	// 闸未装配 → 全部 503（不是放行）。
	noGate, _ := newTestServerWithAuthorizer(t, analysis.Options{
		Results: &stubReader{results: map[string]*analysis.BacktestResponse{"r1": sampleResult("r1")}},
	}, nil)
	status, body = doRequest(t, noGate, http.MethodGet, "/api/analysis/reports/r1", "", "")
	wantCode(t, status, body, http.StatusServiceUnavailable, "auth_unavailable")

	// 显式开放（无 secret）不受影响：本地开发 / CI 仍免 token。
	openSvc := auth.NewTokenService(auth.Config{})
	openServer, _ := newTestServerWithAuthorizer(t, analysis.Options{
		Results: &stubReader{results: map[string]*analysis.BacktestResponse{"r1": sampleResult("r1")}},
	}, NewAuthorizer(openSvc))
	status, body = doRequest(t, openServer, http.MethodPost, "/api/analysis/metrics", `{"run_id":"r1"}`, "")
	if status != http.StatusOK {
		t.Fatalf("open access metrics: %d %s", status, body)
	}
}

func splitPattern(pattern string) (method, path string) {
	parts := strings.SplitN(pattern, " ", 2)
	if len(parts) != 2 {
		return pattern, pattern
	}
	return parts[0], strings.ReplaceAll(parts[1], "{id}", "r1")
}

func doRequest(t *testing.T, srv *httptest.Server, method, path, body, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}
