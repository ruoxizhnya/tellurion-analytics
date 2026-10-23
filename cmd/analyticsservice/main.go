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
	"net/http"
	"os"
	"time"

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
	flag.Parse()

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
	srv := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.New(svc).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("analyticsservice: listening on %s", *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("analyticsservice: %v", err)
	}
}
