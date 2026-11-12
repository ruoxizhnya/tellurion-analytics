package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tellurion-analytics/internal/analysis"
	"tellurion-analytics/internal/httpapi"
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

// TestResolveAuthMode 钉住启动期三态裁决与拒绝条件（六仓统一口径）。
//
// 2026-11-12 规则修正：第 3 态不再因绑定地址拒绝启动——有显式声明时任何绑定都放开。
func TestResolveAuthMode(t *testing.T) {
	cases := []struct {
		name          string
		secret        string
		allowInsecure bool
		declared      bool
		host          string
		exposure      string
		want          authMode
		wantErr       bool
	}{
		{"secret wins", "s3cret", true, false, "0.0.0.0", "", authModeEnforced, false},
		{"secret wins even with insecure disabled", "s3cret", false, false, "127.0.0.1", "", authModeEnforced, false},
		{"no secret + no declaration + loopback = degraded", "", true, false, "127.0.0.1", "", authModeDegraded, false},
		{"no secret + no declaration + non-loopback refuses", "", true, false, "0.0.0.0", "", 0, true},
		{"explicit exposure on loopback = open", "", true, false, "127.0.0.1", "loopback-published", authModeOpen, false},
		{"explicit exposure on non-loopback = open", "", true, false, "0.0.0.0", "loopback-published", authModeOpen, false},
		{"explicit allow-insecure on loopback = open", "", true, true, "127.0.0.1", "", authModeOpen, false},
		{"explicit allow-insecure on non-loopback = open", "", true, true, "0.0.0.0", "", authModeOpen, false},
		{"unknown declaration refuses", "", true, false, "127.0.0.1", "loopback", 0, true},
		{"insecure disabled refuses without secret", "", false, false, "127.0.0.1", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveAuthMode(tc.secret, tc.allowInsecure, tc.declared, tc.host, tc.exposure)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveAuthMode = %v, want error (refuse to start)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveAuthMode error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveAuthMode = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestInsecureDeclared 钉住「显式声明」的判定：出厂默认 true 不算声明，
// 只有环境变量实际存在或 -allow-insecure 被显式给出才算。
func TestInsecureDeclared(t *testing.T) {
	t.Run("unset is not a declaration", func(t *testing.T) {
		t.Setenv("AUTH_ALLOW_INSECURE", "")
		if insecureDeclared(true) {
			t.Fatal("AUTH_ALLOW_INSECURE 未设置时不应算显式声明")
		}
	})
	t.Run("env true is a declaration", func(t *testing.T) {
		t.Setenv("AUTH_ALLOW_INSECURE", "true")
		if !insecureDeclared(true) {
			t.Fatal("AUTH_ALLOW_INSECURE=true 应算显式声明")
		}
	})
	t.Run("env false is not an open declaration", func(t *testing.T) {
		t.Setenv("AUTH_ALLOW_INSECURE", "false")
		if insecureDeclared(false) {
			t.Fatal("AUTH_ALLOW_INSECURE=false 不应算开放声明")
		}
	})
}

// TestLogAuthPosture 钉住第 3 态 WARN 的两件事：必须同时写出「写面无鉴权」与
// 绑定地址；非回环绑定必须点名「已对外暴露」。
func TestLogAuthPosture(t *testing.T) {
	cases := []struct {
		name    string
		mode    authMode
		addr    string
		want    []string
		notWant []string
	}{
		{
			name: "open on non-loopback",
			mode: authModeOpen, addr: "0.0.0.0:8087",
			want: []string{"写面无鉴权", "0.0.0.0:8087", "已对外暴露"},
		},
		{
			name: "open on loopback",
			mode: authModeOpen, addr: "127.0.0.1:8087",
			want:    []string{"写面无鉴权", "127.0.0.1:8087"},
			notWant: []string{"已对外暴露"},
		},
		{
			name: "degraded",
			mode: authModeDegraded, addr: "127.0.0.1:8087",
			want:    []string{"503 auth_unavailable", "127.0.0.1:8087"},
			notWant: []string{"写面无鉴权", "已对外暴露"},
		},
		{
			name: "enforced prints nothing",
			mode: authModeEnforced, addr: "0.0.0.0:8087",
			notWant: []string{"WARNING"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			prev := log.Writer()
			log.SetOutput(&buf)
			defer log.SetOutput(prev)

			logAuthPosture(tc.mode, tc.addr)
			out := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Fatalf("启动 WARN 缺少 %q；实际输出: %s", w, out)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(out, w) {
					t.Fatalf("启动 WARN 不应包含 %q；实际输出: %s", w, out)
				}
			}
		})
	}
}

// stubResults 是 cmd 级用例的最小 ResultReader：只服务 run_id=r1。
type stubResults struct{}

func (stubResults) GetResult(_ context.Context, runID string) (*analysis.BacktestResponse, error) {
	if runID != "r1" {
		return nil, analysis.ErrRunNotFound
	}
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	values := make([]analysis.PortfolioValue, 0, 40)
	value := 100.0
	for i := 0; i < 40; i++ {
		values = append(values, analysis.PortfolioValue{Date: base.AddDate(0, 0, i), TotalValue: value})
		if i%2 == 0 {
			value *= 1.01
		} else {
			value *= 0.995
		}
	}
	return &analysis.BacktestResponse{
		ID:              runID,
		Status:          analysis.ResultCompleted,
		TotalReturn:     0.1,
		SharpeRatio:     1.234,
		PortfolioValues: values,
	}, nil
}

// TestAuthState3NonLoopbackServesWrites 是 2026-11-12 规则修正的锚点：
// 无 secret + 显式声明 + **非回环绑定** → 进程能起（resolveAuthMode 不报错），
// 且按 main 的装配方式（open → NewOpenAuthorizer）写面真放行（200）。
func TestAuthState3NonLoopbackServesWrites(t *testing.T) {
	mode, err := resolveAuthMode("", true, false, "0.0.0.0", "loopback-published")
	if err != nil {
		t.Fatalf("非回环 + 显式声明应允许启动，却返回错误: %v", err)
	}
	if mode != authModeOpen {
		t.Fatalf("mode = %v, want authModeOpen", mode)
	}

	// 显式 allow-insecure 声明同样让非回环绑定落地（不用 AUTH_INSECURE_EXPOSURE）。
	mode, err = resolveAuthMode("", true, true, "0.0.0.0", "")
	if err != nil {
		t.Fatalf("非回环 + 显式 allow-insecure 应允许启动，却返回错误: %v", err)
	}
	if mode != authModeOpen {
		t.Fatalf("mode = %v, want authModeOpen", mode)
	}

	svc, err := analysis.New(analysis.Options{Store: analysis.NewMemory(), Results: stubResults{}})
	if err != nil {
		t.Fatalf("analysis.New: %v", err)
	}
	srv := httptest.NewServer(newMux(httpapi.New(svc, httpapi.WithAuthorizer(httpapi.NewOpenAuthorizer())).Handler()))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/analysis/metrics", "application/json", strings.NewReader(`{"run_id":"r1"}`))
	if err != nil {
		t.Fatalf("POST metrics: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("第 3 态非回环写面 = %d, want 200: %s", resp.StatusCode, body)
	}
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
