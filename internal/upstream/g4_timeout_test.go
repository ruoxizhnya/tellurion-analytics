package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"tellurion-analytics/internal/analysis"
)

// TestG4OutboundRequestHasTimeoutBound 是 SVC-AN-G4 ③ 的用例：
// 注入一个**永不响应**的假上游，并给 reader 显式注入一个**没有 Timeout** 的
// http.Client——上界只能来自 adapter 自己在 context 上加的截止时间。
//
// 预期：到上界即返回，且被折算成 analysis.ErrDependencyUnavailable
// （wire 层 = 503 dependency_unavailable，见 docs/api-schema.md §2.2）。
//
// 破坏验证：去掉 ResultReader.GetResult 的 boundedContext ⇒ 本用例在 2s
// 看门狗处红（调用永远不返回），错误信息直接点名「缺少显式超时上界」。
func TestG4OutboundRequestHasTimeoutBound(t *testing.T) {
	previous := outboundTimeout
	outboundTimeout = 100 * time.Millisecond
	t.Cleanup(func() { outboundTimeout = previous })

	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release // 永不主动响应；用例收尾时统一放行，避免 Close 挂住。
	}))
	defer func() {
		unblock()
		srv.Close()
	}()

	reader := NewResultReader(srv.URL, &http.Client{}) // 关键：client 无 Timeout

	done := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := reader.GetResult(context.Background(), "run-1")
		done <- err
	}()

	select {
	case err := <-done:
		elapsed := time.Since(started)
		unblock()
		if !errors.Is(err, analysis.ErrDependencyUnavailable) {
			t.Fatalf("never-responding upstream: want ErrDependencyUnavailable, got %v", err)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("timed out after %s, want the 100ms bound to fire", elapsed)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "deadline exceeded") &&
			!strings.Contains(strings.ToLower(err.Error()), "timeout") {
			t.Fatalf("error should carry the deadline/timeout cause: %v", err)
		}
		t.Logf("never-responding upstream failed after %s with %v (wire: 503 dependency_unavailable)",
			elapsed.Round(time.Millisecond), err)
	case <-time.After(2 * time.Second):
		unblock()
		t.Fatalf("GetResult did not return within 2s: 出站请求缺少显式超时上界（注入的 client 没有 Timeout，必须由 context 截止时间兜住）")
	}
}
