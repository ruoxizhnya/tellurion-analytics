// Command analyticsservice 是分析服务的进程入口。
//
// 装配顺序：PG 连接 + 启动迁移（给了 DSN 时）→ 上游读取端口（给了 base URL
// 时）→ HTTP 面。未配置的端口保持 nil，对应端点 fail-closed 成
// 503 dependency_unavailable —— 不伪造结果、不返回空报告。
//
// 未接线项：PDF 导出（R5 已裁为非目标，不实现）。
// 鉴权与上游服务间凭证已接线（裁-15）：本服务过 kit/httpadapter 启动闸，
// 调 core / data 时按 -core-token / -data-token 带 Bearer。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ruoxizhnya/tellurion-kit/auth"
	kithttp "github.com/ruoxizhnya/tellurion-kit/auth/httpadapter"

	"tellurion-analytics/internal/analysis"
	"tellurion-analytics/internal/httpapi"
	"tellurion-analytics/internal/upstream"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8087", "listen address")
	dsn := flag.String("dsn", os.Getenv("ANALYTICS_DATABASE_DSN"),
		"PostgreSQL DSN for the analysis.* schema (empty = in-memory store, dev only)")
	coreURL := flag.String("core-url", os.Getenv("ANALYTICS_CORE_URL"),
		"core base URL for run results (empty = ResultReader unwired → 503)")
	dataURL := flag.String("data-url", os.Getenv("ANALYTICS_DATA_URL"),
		"tellurion-data base URL for X1b datasets (empty = BenchmarkReader unwired → 503)")
	artifactURL := flag.String("artifact-url", os.Getenv("ANALYTICS_ARTIFACT_URL"),
		"artifact base URL for holdings / factor exposures (empty = providers unwired → 503)")
	orchestrationURL := flag.String("orchestration-url", os.Getenv("ANALYTICS_ORCHESTRATION_URL"),
		"orchestration base URL for X5b parent jobs (empty = SensitivityQueue unwired → 503)")
	jwtSecret := flag.String("jwt-secret", envFirst("JWT_SECRET", "AUTH_JWT_SECRET"),
		"JWT secret shared with analysis (empty = open access on loopback only)")
	// 裁-15：服务间凭证。上游（core / data）上了鉴权闸后，不带 Bearer 恒 401；
	// 空值 = 上游未开鉴权（回环 + 无 secret 的开发态）。
	coreToken := flag.String("core-token", envFirst("ANALYTICS_CORE_TOKEN", "CORE_SERVICE_TOKEN"),
		"service token for core upstream (empty = no Authorization header)")
	dataToken := flag.String("data-token", envFirst("ANALYTICS_DATA_TOKEN", "DATA_SERVICE_TOKEN"),
		"service token for tellurion-data upstream (empty = no Authorization header)")
	artifactToken := flag.String("artifact-token", envFirst("ANALYTICS_ARTIFACT_TOKEN"),
		"service token for artifact upstream (empty = fall back to -core-token)")
	orchestrationToken := flag.String("orchestration-token", envFirst("ANALYTICS_ORCHESTRATION_TOKEN", "ORCHESTRATION_SERVICE_TOKEN"),
		"service token for orchestration upstream (empty = no Authorization header)")
	exposure := flag.String("insecure-exposure", os.Getenv("AUTH_INSECURE_EXPOSURE"),
		"explicit exposure declaration for open access on a non-loopback bind (only loopback-published is accepted)")
	flag.Parse()

	// 启动闸（裁-15）：有 secret ⇒ 鉴权；无 secret + 回环 ⇒ 开放；无 secret + 非回环 ⇒ 拒绝启动。
	startup := kithttp.DecideStartup(*jwtSecret, true, bindHost(*addr), *exposure)
	if startup.Refuse {
		log.Fatalf("analyticsservice: %s", startup.Reason)
	}
	if startup.Insecure {
		log.Printf("analyticsservice: WARNING authentication disabled (open access); bind address %q is loopback-only", *addr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := analysis.Options{Executor: analysis.AsyncExecutor{}}
	var pg *analysis.PG
	if *dsn != "" {
		var err error
		pg, err = analysis.OpenPG(ctx, *dsn)
		if err != nil {
			log.Fatalf("analyticsservice: %v", err)
		}
		defer pg.Close()
		if err := pg.Migrate(ctx); err != nil {
			log.Fatalf("analyticsservice: migrate: %v", err)
		}
		opts.Store = pg
		log.Printf("analyticsservice: postgres store ready (analysis.* migrated)")
	} else {
		opts.Store = analysis.NewMemory()
		log.Printf("analyticsservice: WARNING in-memory store (set -dsn / ANALYTICS_DATABASE_DSN to persist analysis.*)")
	}

	artToken := *artifactToken
	if artToken == "" {
		// artifact 面当前挂在 core（R2 的 DTO owner 已裁 = 本仓，wire 面仍 interim）；未单独配就复用 core 凭证。
		artToken = *coreToken
	}
	if *coreURL != "" {
		opts.Results = upstream.NewResultReaderWithToken(*coreURL, nil, *coreToken)
	}
	if *dataURL != "" {
		opts.Benchmarks = upstream.NewBenchmarkReaderWithToken(*dataURL, nil, *dataToken)
	}
	if *artifactURL != "" {
		artifacts := upstream.NewArtifactReaderWithToken(*artifactURL, nil, artToken)
		opts.Holdings = artifacts
		opts.Factors = artifacts
	}
	if *orchestrationURL != "" {
		opts.Queue = upstream.NewSensitivityQueueWithToken(*orchestrationURL, nil, *orchestrationToken)
	}
	log.Printf("analyticsservice: readers wired result=%t benchmark=%t holdings=%t factors=%t queue=%t",
		opts.Results != nil, opts.Benchmarks != nil, opts.Holdings != nil, opts.Factors != nil, opts.Queue != nil)

	svc, err := analysis.New(opts)
	if err != nil {
		log.Fatalf("analyticsservice: %v", err)
	}
	// 就绪判定（SVC-AN-G1）：/healthz 只说进程在监听；/readyz 要求
	// 「PG 可连 + analysis.* 已迁移 + 四个上游端口装齐」。memory 模式恒 503——
	// 结果不落 analysis.*，进程活着不等于能出报告。
	var readiness httpapi.ReadinessFunc
	if pg != nil {
		readiness = func(ctx context.Context) httpapi.ReadinessReport {
			if err := pg.Ready(ctx); err != nil {
				return httpapi.ReadinessReport{Store: "postgres", Err: err}
			}
			return httpapi.ReadinessReport{Store: "postgres", Missing: svc.MissingUpstreams()}
		}
	} else {
		readiness = func(context.Context) httpapi.ReadinessReport {
			return httpapi.ReadinessReport{
				Store:   "memory",
				Missing: []string{"store: postgres (set -dsn / ANALYTICS_DATABASE_DSN)"},
			}
		}
	}
	tokens := auth.NewTokenService(auth.Config{JWTSecret: startup.Secret})
	srv := &http.Server{
		Addr: *addr,
		Handler: newMux(httpapi.New(
			svc,
			httpapi.WithAuthorizer(httpapi.NewAuthorizer(tokens)),
			httpapi.WithReadiness(readiness),
		).Handler()),
		ReadHeaderTimeout: 5 * time.Second,
	}
	// 先监听再进入 serve：Serve 与 Shutdown 共用同一个 listener，端口冲突在
	// 这里就报错，不必等 listen 与信号竞态。
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("analyticsservice: listen %s: %v", *addr, err)
	}
	// 终止信号只影响 serve；PG 连接与启动期迁移沿用上面的 startup ctx。
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("analyticsservice: listening on %s (auth=%t, drain=%s)", ln.Addr(), tokens.Enabled(), drainTimeout)
	if err := serve(sigCtx, srv, ln, drainTimeout); err != nil {
		log.Fatalf("analyticsservice: %v", err)
	}
}

// drainTimeout 是收到终止信号后给在途请求的最大排空窗口。
const drainTimeout = 10 * time.Second

// serve 运行 HTTP 面，并在 ctx 取消（SIGTERM / SIGINT）时优雅停机：
// 先停止接受新连接，再给在途请求一个有界 drain 窗口。
//
// 返回 nil = 正常关闭（进程退出码 0）；Serve 自身出错或 drain 超时则返回非 nil，
// 由调用方转成非零退出。drain 超时用 Close 强制断开兜底，不无限等待。
func serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	log.Printf("analyticsservice: shutdown signal received, draining in-flight requests (timeout %s)", drain)
	drainCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		log.Printf("analyticsservice: drain did not finish in %s: %v; forcing close", drain, err)
		_ = srv.Close()
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// healthzPath 是进程健康探针（Dockerfile / compose 的 HEALTHCHECK 消费）。
//
// 它挂在 httpapi 之外：探针必须无凭证可用，也不属于 docs/api-schema.md 的
// 8 个分析端点契约。只报「进程在监听」，不报上游依赖状态——上游未接线是
// fail-closed 503，不是进程不健康。
const healthzPath = "/healthz"

// newMux 把公开探针与业务 HTTP 面拼起来。抽出来是为了能测：进程装配里
// 「探针确实注册了」不是一个能靠读代码断言的事实。
func newMux(api http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", api)
	// 不用 method pattern：带兜底 "/" 时，未匹配的方法会落到 API 而不是 405。
	// 探针自己判方法，避免 POST /healthz 被业务面（可能带鉴权）接走。
	mux.HandleFunc(healthzPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	return mux
}

// bindHost 从 -addr 取绑定主机；取不到端口时原样当主机比（空 = 全网卡）。
func bindHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// envFirst 按顺序取第一个非空环境变量（core 历史用法 JWT_SECRET、配置口径 AUTH_JWT_SECRET）。
func envFirst(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}
