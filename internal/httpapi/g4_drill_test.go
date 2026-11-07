package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"tellurion-analytics/internal/analysis"
	"tellurion-analytics/internal/upstream"
)

// TestG4UpstreamFailureWireCodes 把 SVC-AN-G4 ② 的五类上游失败落到 wire 上：
// 401 / 409 / 503 → 503 dependency_unavailable；404（run 不存在）→ 404 run_not_found。
// 超时由 internal/upstream.TestG4OutboundRequestHasTimeoutBound 钉住，折算同样是
// ErrDependencyUnavailable → 503（见 docs/api-schema.md §2.2）。
func TestG4UpstreamFailureWireCodes(t *testing.T) {
	cases := []struct {
		name       string
		upstream   int
		wantStatus int
		wantCode   string
	}{
		{"upstream 401 credentials wrong", http.StatusUnauthorized, http.StatusServiceUnavailable, "dependency_unavailable"},
		{"upstream 404 run not found", http.StatusNotFound, http.StatusNotFound, "run_not_found"},
		{"upstream 409 conclusion not ready", http.StatusConflict, http.StatusServiceUnavailable, "dependency_unavailable"},
		{"upstream 503 dependency unwired", http.StatusServiceUnavailable, http.StatusServiceUnavailable, "dependency_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.upstream)
				_, _ = w.Write([]byte(`{"error":"upstream said no"}`))
			}))
			defer upstreamSrv.Close()

			store := &countingStore{Store: analysis.NewMemory()}
			svc, err := analysis.New(analysis.Options{
				Store:   store,
				Results: upstream.NewResultReader(upstreamSrv.URL, &http.Client{}),
				Now:     func() time.Time { return testDate },
			})
			if err != nil {
				t.Fatalf("analysis.New: %v", err)
			}
			srv := httptest.NewServer(New(svc, WithAuthorizer(allowAllAuthorizer{})).Handler())
			defer srv.Close()

			status, body := post(t, srv, "/api/analysis/metrics", `{"run_id":"run-1"}`)
			wantCode(t, status, body, tc.wantStatus, tc.wantCode)
			if puts := atomic.LoadInt32(&store.puts); puts != 0 {
				t.Fatalf("upstream %d: PutReport called %d times, want 0", tc.upstream, puts)
			}
			t.Logf("upstream %d -> HTTP %d %s", tc.upstream, status, tc.wantCode)
		})
	}
}

// TestG4DrillUpstreamOutageAndDeterministicReplay 是 SVC-AN-G4 ⑥ 的演练：
//
//  1. 断开上游（handler 直接 hijack 并关闭 TCP 连接，模拟真实断连）⇒ 本仓
//     返回 503 dependency_unavailable，且**零 PutReport**（不产出半成品报告）；
//  2. 恢复上游后同输入连发两次 ⇒ 200、同一 report_id、响应体逐字节一致
//     （sha256 相同）、PutReport 只发生一次（第二次走幂等复用，不重算）。
//
// 这条同时兑现 ① 的可复现承诺：同样的输入两次得到同一份报告。
func TestG4DrillUpstreamOutageAndDeterministicReplay(t *testing.T) {
	var upstreamUp atomic.Bool

	payload, err := json.Marshal(sampleResult("run-1"))
	if err != nil {
		t.Fatalf("marshal upstream payload: %v", err)
	}
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !upstreamUp.Load() {
			// 真实断连：不走 5xx，直接关掉 TCP 连接（客户端看到 EOF / reset）。
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			conn, _, err := hijacker.Hijack()
			if err == nil {
				_ = conn.Close()
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(upstreamSrv.Close)

	// client 故意不带 Timeout：出站上界由 internal/upstream 的 context 截止时间兜住。
	store := &countingStore{Store: analysis.NewMemory()}
	svc, err := analysis.New(analysis.Options{
		Store:   store,
		Results: upstream.NewResultReader(upstreamSrv.URL, &http.Client{}),
		Now:     func() time.Time { return testDate },
	})
	if err != nil {
		t.Fatalf("analysis.New: %v", err)
	}
	srv := httptest.NewServer(New(svc, WithAuthorizer(allowAllAuthorizer{})).Handler())
	t.Cleanup(srv.Close)

	const request = `{"run_id":"run-1"}`

	// —— 阶段 1：上游断开 ——
	status, body := post(t, srv, "/api/analysis/metrics", request)
	wantCode(t, status, body, http.StatusServiceUnavailable, "dependency_unavailable")
	if puts := atomic.LoadInt32(&store.puts); puts != 0 {
		t.Fatalf("upstream down: PutReport called %d times, want 0 (no half-baked report)", puts)
	}
	if got := decode(t, body); got["report_id"] != nil {
		t.Fatalf("upstream down: response carried a report_id: %v", got)
	}
	t.Logf("PHASE 1 upstream down: HTTP %d code=%s body=%s PutReport=%d",
		status, decode(t, body)["code"], bytes.TrimSpace(body), atomic.LoadInt32(&store.puts))

	// —— 阶段 2：恢复上游，同输入两次 ——
	upstreamUp.Store(true)

	status1, body1 := post(t, srv, "/api/analysis/metrics", request)
	if status1 != http.StatusOK {
		t.Fatalf("recovered POST #1 = %d: %s", status1, body1)
	}
	status2, body2 := post(t, srv, "/api/analysis/metrics", request)
	if status2 != http.StatusOK {
		t.Fatalf("recovered POST #2 = %d: %s", status2, body2)
	}

	id1, _ := decode(t, body1)["report_id"].(string)
	id2, _ := decode(t, body2)["report_id"].(string)
	if id1 == "" || id1 != id2 {
		t.Fatalf("report_id changed on identical input: %q vs %q", id1, id2)
	}
	if !bytes.Equal(body1, body2) {
		t.Fatalf("identical input returned different bytes:\n#1 %s\n#2 %s", body1, body2)
	}
	sum1, sum2 := sha256.Sum256(body1), sha256.Sum256(body2)
	if sum1 != sum2 {
		t.Fatalf("sha256 mismatch: %s vs %s", hex.EncodeToString(sum1[:]), hex.EncodeToString(sum2[:]))
	}
	if puts := atomic.LoadInt32(&store.puts); puts != 1 {
		t.Fatalf("recovered: PutReport called %d times, want 1 (second POST must replay the stored report)", puts)
	}

	t.Logf("PHASE 2 recovered: report_id=%s", id1)
	t.Logf("PHASE 2 POST #1: len=%d sha256=%s", len(body1), hex.EncodeToString(sum1[:]))
	t.Logf("PHASE 2 POST #2: len=%d sha256=%s", len(body2), hex.EncodeToString(sum2[:]))
	t.Logf("PHASE 2 identical bytes=%t PutReport=%d", bytes.Equal(body1, body2), atomic.LoadInt32(&store.puts))
}
