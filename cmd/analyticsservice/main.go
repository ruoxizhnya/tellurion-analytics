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
	"strconv"
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
	allowInsecure := flag.Bool("allow-insecure", envBool("AUTH_ALLOW_INSECURE", true),
		"allow any no-secret mode at all (false = JWT_SECRET is mandatory and a missing secret refuses startup)")
	flag.Parse()

	// 「显式声明」= 部署者主动写下 AUTH_INSECURE_EXPOSURE=loopback-published，或显式把
	// AUTH_ALLOW_INSECURE / -allow-insecure 置为 true。默认值（未设置）**不算**声明，
	// 否则非回环绑定会静默落到开放态。
	allowInsecureDeclared := insecureDeclared(*allowInsecure)

	// 三态启动裁决（裁-15 补充，2026-11-12 规则修正）：
	//   1. 有 secret                             → 全闸（所有端点过 kit）；
	//   2. 无 secret + 无显式声明 + 回环绑定       → 只读降级：读端点公开，写端点 503 auth_unavailable；
	//      无 secret + 无显式声明 + 非回环绑定     → 拒绝启动；
	//   3. 无 secret + 显式声明                    → 任何绑定都放开（写面无鉴权），启动 WARN 写清
	//      「写面无鉴权」+ 绑定地址；非回环绑定的 WARN 额外点名「已对外暴露」。
	// allowInsecure 来自配置（AUTH_ALLOW_INSECURE / -allow-insecure），不再是代码里的常量。
	mode, err := resolveAuthMode(*jwtSecret, *allowInsecure, allowInsecureDeclared, bindHost(*addr), *exposure)
	if err != nil {
		log.Fatalf("analyticsservice: %v", err)
	}
	logAuthPosture(mode, *addr)

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
	var tokens *auth.TokenService
	var authorizer httpapi.Authorizer
	if mode == authModeOpen {
		authorizer = httpapi.NewOpenAuthorizer()
	} else {
		secret := ""
		if mode == authModeEnforced {
			secret = *jwtSecret
		}
		tokens = auth.NewTokenService(auth.Config{JWTSecret: []byte(secret)})
		authorizer = httpapi.NewAuthorizer(tokens)
	}
	srv := &http.Server{
		Addr: *addr,
		Handler: newMux(httpapi.New(
			svc,
			httpapi.WithAuthorizer(authorizer),
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
	log.Printf("analyticsservice: listening on %s (auth=%t, drain=%s)", ln.Addr(), mode == authModeEnforced, drainTimeout)
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

// authMode 是本进程的鉴权三态（六仓统一口径）。
type authMode int

const (
	// authModeEnforced：有 JWT_SECRET，所有端点过 kit 闸（401 / 403）。
	authModeEnforced authMode = iota
	// authModeDegraded：无 secret、无显式开放声明。读端点公开，写端点 503。
	authModeDegraded
	// authModeOpen：无 secret + 显式声明（loopback-published 或显式 allow-insecure），
	// 任何绑定都放开写面（启动 WARN 点名「写面无鉴权」）。
	authModeOpen
)

// resolveAuthMode 按三态口径裁决启动模式；返回 error = 拒绝启动。
//
// 「显式声明」= allowInsecureDeclared（部署者显式写 AUTH_ALLOW_INSECURE/-allow-insecure）
// 或 exposure == loopback-published。显式声明后第 3 态不再看 bindHost：容器里绑 0.0.0.0
// 也能放开，启动 WARN 负责把「写面无鉴权」与「绑定地址」写清楚；非回环绑定额外点名
// 「已对外暴露」。无声明时，回环绑定落第 2 态（写面 503），非回环绑定拒绝启动。
func resolveAuthMode(secret string, allowInsecure, allowInsecureDeclared bool, host, exposure string) (authMode, error) {
	if strings.TrimSpace(secret) != "" {
		return authModeEnforced, nil
	}
	switch strings.TrimSpace(exposure) {
	case "":
		if allowInsecureDeclared {
			return authModeOpen, nil
		}
		if !allowInsecure {
			return 0, errors.New("auth: JWT secret missing and insecure mode disabled (AUTH_ALLOW_INSECURE=false) — refusing to start")
		}
		if !kithttp.IsLoopbackHost(host) {
			return 0, fmt.Errorf("auth: JWT secret missing and no explicit AUTH_INSECURE_EXPOSURE declaration; bind %q is not loopback — refusing to start", host)
		}
		return authModeDegraded, nil
	case kithttp.InsecureExposureLoopbackPublished:
		return authModeOpen, nil
	default:
		return 0, fmt.Errorf("auth: unknown AUTH_INSECURE_EXPOSURE %q (only %q is accepted) — refusing to start",
			exposure, kithttp.InsecureExposureLoopbackPublished)
	}
}

// logAuthPosture 打印三态启动 WARN。第 3 态必须同时写清「写面无鉴权」与绑定地址；
// 非回环绑定额外点名「已对外暴露」。
func logAuthPosture(mode authMode, addr string) {
	host := bindHost(addr)
	switch mode {
	case authModeOpen:
		if kithttp.IsLoopbackHost(host) {
			log.Printf("analyticsservice: WARNING 写面无鉴权（authentication disabled）：绑定地址 %s（回环）；写端点不再校验 token", addr)
			return
		}
		log.Printf("analyticsservice: WARNING 写面无鉴权（authentication disabled）：绑定地址 %s，已对外暴露；发布层必须回环或前置鉴权网关", addr)
	case authModeDegraded:
		log.Printf("analyticsservice: WARNING authentication is not configured (no JWT_SECRET)：读端点公开、写端点 503 auth_unavailable；绑定地址 %s", addr)
	}
}

// insecureDeclared 判断部署者是否**主动**声明了放开（第 3 态）。
//
// AUTH_ALLOW_INSECURE 出厂值就是 true（envBool 默认），所以「没设置」不能算声明；
// 只有环境变量实际存在且为 true，或命令行显式给出 -allow-insecure=true 才算。
func insecureDeclared(allowInsecure bool) bool {
	if strings.TrimSpace(os.Getenv("AUTH_ALLOW_INSECURE")) != "" {
		return allowInsecure
	}
	declared := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "allow-insecure" {
			declared = allowInsecure
		}
	})
	return declared
}

// bindHost 从 -addr 取绑定主机；取不到端口时原样当主机比（空 = 全网卡）。
func bindHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// envBool 解析布尔环境变量；缺省或无法解析时返回 def。
func envBool(name string, def bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
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
