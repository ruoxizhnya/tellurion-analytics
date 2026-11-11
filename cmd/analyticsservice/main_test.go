package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

// slowServer 起一个真 listener + 慢处理器，返回可取消的 serve 与请求结果。
type slowServer struct {
	addr     string
	cancel   context.CancelFunc
	serveErr chan error
	respCh   chan slowResult
	started  chan struct{}
	finished chan struct{}
}

type slowResult struct {
	status int
	body   string
	err    error
}

// startSlowServer 模拟 SIGTERM：测试里用 cancel() 代替真实信号（Windows 上
// os.Process.Signal 不支持对子进程投递 SIGTERM/os.Interrupt），但走的是
// 与 main 完全相同的 serve 路径。
func startSlowServer(t *testing.T, drain time.Duration, handlerDelay time.Duration) *slowServer {
	t.Helper()
	started := make(chan struct{})
	finished := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(handlerDelay)
		close(finished)
		_, _ = io.WriteString(w, "slow-ok")
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ss := &slowServer{
		addr:     ln.Addr().String(),
		cancel:   cancel,
		serveErr: make(chan error, 1),
		respCh:   make(chan slowResult, 1),
		started:  started,
		finished: finished,
	}
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
	})
	go func() { ss.serveErr <- serve(ctx, srv, ln, drain) }()
	go func() {
		resp, err := http.Get("http://" + ss.addr + "/slow")
		if err != nil {
			ss.respCh <- slowResult{err: err}
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		ss.respCh <- slowResult{status: resp.StatusCode, body: string(body)}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never started")
	}
	return ss
}

// 收到终止信号后：在途请求必须在 drain 窗口内正常完成，serve 返回 nil
// （main 据此走正常返回 = 退出码 0）。
func TestServeDrainsInFlightRequestsOnSignal(t *testing.T) {
	ss := startSlowServer(t, 5*time.Second, 200*time.Millisecond)
	ss.cancel() // 等价 SIGTERM

	if err := <-ss.serveErr; err != nil {
		t.Fatalf("serve() on graceful shutdown = %v, want nil (exit code 0)", err)
	}
	// Shutdown 承诺等处理器跑完才返回：serve 先返回就说明没有真 drain。
	select {
	case <-ss.finished:
	default:
		t.Fatal("serve returned before the in-flight handler finished (request not drained)")
	}
	select {
	case res := <-ss.respCh:
		if res.err != nil {
			t.Fatalf("in-flight request failed during drain: %v", res.err)
		}
		if res.status != http.StatusOK || res.body != "slow-ok" {
			t.Fatalf("in-flight request = %d %q, want 200 \"slow-ok\"", res.status, res.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request never completed")
	}
}

// drain 窗口必须是有界的：超过窗口仍未完成的请求被强制断开，serve 返回
// 非 nil（main 转成非零退出），而不是无限等待。
func TestServeDrainTimeoutForcesClose(t *testing.T) {
	ss := startSlowServer(t, 10*time.Millisecond, 500*time.Millisecond)
	ss.cancel()

	if err := <-ss.serveErr; err == nil {
		t.Fatal("serve() = nil after drain timeout, want non-nil (non-zero exit)")
	}
	select {
	case res := <-ss.respCh:
		if res.err == nil && res.status == http.StatusOK {
			t.Fatalf("request completed %d after bounded drain, want forced close", res.status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not observe forced close")
	}
}
