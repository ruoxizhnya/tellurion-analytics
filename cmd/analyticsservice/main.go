// Command analyticsservice 是分析服务的进程入口。
//
// 装配顺序：PG 连接 + 启动迁移（给了 DSN 时）→ 上游读取端口（给了 base URL
// 时）→ HTTP 面。未配置的端口保持 nil，对应端点 fail-closed 成
// 503 dependency_unavailable —— 不伪造结果、不返回空报告。
//
// 未接线项（docs/TASKS.md QT-43 施工记录）：sensitivity 逐点回填（等 X5b）、
// PDF 导出（R5 未裁）、鉴权（QT-50 ④）。
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
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
	jwtSecret := flag.String("jwt-secret", envFirst("JWT_SECRET", "AUTH_JWT_SECRET"),
		"JWT secret shared with analysis (empty = open access on loopback only)")
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
	if *dsn != "" {
		pg, err := analysis.OpenPG(ctx, *dsn)
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

	if *coreURL != "" {
		opts.Results = upstream.NewResultReader(*coreURL, nil)
	}
	if *dataURL != "" {
		opts.Benchmarks = upstream.NewBenchmarkReader(*dataURL, nil)
	}
	if *artifactURL != "" {
		artifacts := upstream.NewArtifactReader(*artifactURL, nil)
		opts.Holdings = artifacts
		opts.Factors = artifacts
	}
	log.Printf("analyticsservice: readers wired result=%t benchmark=%t holdings=%t factors=%t",
		opts.Results != nil, opts.Benchmarks != nil, opts.Holdings != nil, opts.Factors != nil)

	svc, err := analysis.New(opts)
	if err != nil {
		log.Fatalf("analyticsservice: %v", err)
	}
	tokens := auth.NewTokenService(auth.Config{JWTSecret: startup.Secret})
	srv := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.New(svc, httpapi.WithAuthorizer(httpapi.NewAuthorizer(tokens))).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("analyticsservice: listening on %s (auth=%t)", *addr, tokens.Enabled())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("analyticsservice: %v", err)
	}
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
