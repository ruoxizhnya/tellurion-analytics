package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-kit/auth"

	"tellurion-analytics/internal/analysis"
)

// 本文件钉住 SVC-AN-G1 的四件（docs/TASKS.md）：/readyz 三态、幂等复用、
// 429 与 401/403 可区分、探针豁免鉴权与限流。

func decodeReady(t *testing.T, body []byte) readyBody {
	t.Helper()
	var out readyBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode readyz body %s: %v", body, err)
	}
	return out
}

func readyServer(t *testing.T, readiness ReadinessFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(nil, WithAuthorizer(allowAllAuthorizer{}), WithReadiness(readiness)).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestReadyzThreeVerdicts(t *testing.T) {
	cases := []struct {
		name       string
		readiness  ReadinessFunc
		wantStatus int
		wantStore  string
		wantMiss   []string
		wantError  string
	}{
		{
			name:       "no readiness wired is fail-closed",
			readiness:  nil,
			wantStatus: http.StatusServiceUnavailable,
			wantStore:  "unwired",
			wantMiss:   []string{"store"},
		},
		{
			name: "memory store is not ready",
			readiness: func(context.Context) ReadinessReport {
				return ReadinessReport{Store: "memory", Missing: []string{"store: postgres"}}
			},
			wantStatus: http.StatusServiceUnavailable,
			wantStore:  "memory",
			wantMiss:   []string{"store: postgres"},
		},
		{
			name: "pg down",
			readiness: func(context.Context) ReadinessReport {
				return ReadinessReport{Store: "postgres", Err: errors.New("ping postgres: connection refused")}
			},
			wantStatus: http.StatusServiceUnavailable,
			wantStore:  "postgres",
			wantError:  "connection refused",
		},
		{
			name: "pg up but upstreams unwired names them",
			readiness: func(context.Context) ReadinessReport {
				return ReadinessReport{Store: "postgres", Missing: []string{"benchmarks", "queue"}}
			},
			wantStatus: http.StatusServiceUnavailable,
			wantStore:  "postgres",
			wantMiss:   []string{"benchmarks", "queue"},
		},
		{
			name: "pg up and all upstreams wired",
			readiness: func(context.Context) ReadinessReport {
				return ReadinessReport{Store: "postgres"}
			},
			wantStatus: http.StatusOK,
			wantStore:  "postgres",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := readyServer(t, tc.readiness)
			resp, body := get(t, srv, "/readyz")
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("GET /readyz = %d, want %d: %s", resp.StatusCode, tc.wantStatus, body)
			}
			got := decodeReady(t, body)
			if got.Store != tc.wantStore {
				t.Fatalf("store = %q, want %q", got.Store, tc.wantStore)
			}
			if tc.wantStatus == http.StatusOK {
				if got.Status != "ready" {
					t.Fatalf("status = %q, want ready", got.Status)
				}
			} else if got.Status != "not_ready" {
				t.Fatalf("status = %q, want not_ready", got.Status)
			}
			if strings.Join(got.Missing, ",") != strings.Join(tc.wantMiss, ",") {
				t.Fatalf("missing = %v, want %v", got.Missing, tc.wantMiss)
			}
			if tc.wantError != "" && !strings.Contains(got.Error, tc.wantError) {
				t.Fatalf("error = %q, want contains %q", got.Error, tc.wantError)
			}
		})
	}
}

// 探针必须豁免鉴权与限流：否则鉴权一开、桶一空，编排器的健康检查就会红，
// 把「服务能出报告」误判成「服务挂了」。
func TestReadyzIsExemptFromAuthAndRateLimit(t *testing.T) {
	tokens := auth.NewTokenService(auth.Config{JWTSecret: []byte("g1-probe-exempt")})
	srv := httptest.NewServer(New(nil,
		WithAuthorizer(NewAuthorizer(tokens)),
		WithReadiness(func(context.Context) ReadinessReport {
			return ReadinessReport{Store: "postgres"}
		}),
		WithRateLimit(1, 1),
	).Handler())
	t.Cleanup(srv.Close)

	for i := 0; i < 10; i++ {
		resp, body := get(t, srv, "/readyz")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("/readyz attempt %d = %d, want 200: %s", i+1, resp.StatusCode, body)
		}
	}

	// 业务端点仍受鉴权：无凭证 401，不会被限流掩盖成 429。
	status, body := doRequest(t, srv, http.MethodPost, "/api/analysis/metrics", `{"run_id":"r1"}`, "")
	wantCode(t, status, body, http.StatusUnauthorized, "unauthenticated")
}

// 鉴权先于限流：无凭证恒 401；带凭证打到 burst 上界才 429，且带 Retry-After。
func TestRateLimitIs429AndDistinctFromAuth(t *testing.T) {
	tokens := auth.NewTokenService(auth.Config{JWTSecret: []byte("g1-rate-limit")})
	svc, err := analysis.New(analysis.Options{
		Store:   analysis.NewMemory(),
		Results: &stubReader{results: map[string]*analysis.BacktestResponse{"r1": sampleResult("r1")}},
		Now:     func() time.Time { return testDate },
	})
	if err != nil {
		t.Fatalf("analysis.New: %v", err)
	}
	srv := httptest.NewServer(New(svc,
		WithAuthorizer(NewAuthorizer(tokens)),
		WithRateLimit(1, 2),
	).Handler())
	t.Cleanup(srv.Close)

	for i := 0; i < 5; i++ {
		status, body := doRequest(t, srv, http.MethodPost, "/api/analysis/metrics", `{"run_id":"r1"}`, "")
		wantCode(t, status, body, http.StatusUnauthorized, "unauthenticated")
	}

	trader := analyticsToken(t, tokens, "bob", "trader")
	for i := 0; i < 2; i++ {
		status, body := doRequest(t, srv, http.MethodPost, "/api/analysis/metrics", `{"run_id":"r1"}`, trader)
		if status != http.StatusOK {
			t.Fatalf("burst attempt %d = %d, want 200: %s", i+1, status, body)
		}
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/analysis/metrics", strings.NewReader(`{"run_id":"r1"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+trader)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over-burst = %d, want 429", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}

// countingStore 数 PutReport 次数：幂等复用生效时第二次 POST 不该再落库。
type countingStore struct {
	analysis.Store
	puts int32
}

func (c *countingStore) PutReport(ctx context.Context, rep analysis.Report) error {
	atomic.AddInt32(&c.puts, 1)
	return c.Store.PutReport(ctx, rep)
}

// 幂等语义写死：同 (run_id, input_hash, params_hash, seed, metrics_version)
// 的重复 POST 返回**同一**报告且不重复计算（不重复 PutReport）。
// 破坏验证：删掉 Service.Metrics 的 replay 早退 ⇒ puts 变 2，本用例红。
func TestSyncPostIsIdempotentOnSameInput(t *testing.T) {
	store := &countingStore{Store: analysis.NewMemory()}
	svc, err := analysis.New(analysis.Options{
		Store:   store,
		Results: &stubReader{results: map[string]*analysis.BacktestResponse{"r1": sampleResult("r1")}},
		Now:     func() time.Time { return testDate },
	})
	if err != nil {
		t.Fatalf("analysis.New: %v", err)
	}
	srv := httptest.NewServer(New(svc, WithAuthorizer(allowAllAuthorizer{})).Handler())
	t.Cleanup(srv.Close)

	status1, body1 := post(t, srv, "/api/analysis/metrics", `{"run_id":"r1"}`)
	if status1 != http.StatusOK {
		t.Fatalf("first POST = %d: %s", status1, body1)
	}
	status2, body2 := post(t, srv, "/api/analysis/metrics", `{"run_id":"r1"}`)
	if status2 != http.StatusOK {
		t.Fatalf("second POST = %d: %s", status2, body2)
	}

	id1, _ := decode(t, body1)["report_id"].(string)
	id2, _ := decode(t, body2)["report_id"].(string)
	if id1 == "" || id1 != id2 {
		t.Fatalf("report_id changed on repeat POST: %q vs %q", id1, id2)
	}
	if string(body1) != string(body2) {
		t.Fatalf("repeat POST returned a different body:\n%s\n%s", body1, body2)
	}
	if got := atomic.LoadInt32(&store.puts); got != 1 {
		t.Fatalf("PutReport called %d times, want 1 (repeat POST must replay the stored report)", got)
	}
}
