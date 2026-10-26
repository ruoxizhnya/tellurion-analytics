package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 探针必须真注册、无鉴权、只接 GET；业务路径仍原样交给 httpapi。
// 这条用例的存在理由：Dockerfile / compose 的 HEALTHCHECK 打的是 /healthz，
// 端点漏注册时容器会永远 unhealthy（静态 compose config 看不出来）。
func TestNewMux_HealthzIsPublicAndDelegates(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/analysis/metrics" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusTeapot)
	})
	h := newMux(api)

	t.Run("GET healthz", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, healthzPath, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", healthzPath, rec.Code)
		}
		if got := rec.Body.String(); got != "ok\n" {
			t.Fatalf("GET %s body = %q, want \"ok\\n\"", healthzPath, got)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Fatalf("Content-Type = %q", ct)
		}
	})

	t.Run("healthz is GET only", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, healthzPath, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s = %d, want 405", healthzPath, rec.Code)
		}
	})

	t.Run("business paths delegate", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/analysis/metrics", nil))
		if rec.Code != http.StatusTeapot {
			t.Fatalf("delegated path = %d, want 418 from api handler", rec.Code)
		}
	})
}
