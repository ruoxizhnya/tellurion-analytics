// Command analyticsservice 是分析服务的进程入口。
//
// 当前只装配纯函数计算面（指标 / 基准 / 归因 / 敏感性 / 蒙特卡洛 / 相关性）在
// Memory Store 上；上游读取端口（ResultReader / BenchmarkReader / holdings /
// factor exposures）、PG Store、迁移执行入口、QT-44 job 队列、鉴权均未接线
// （docs/TASKS.md QT-43 施工记录）。未接线的端口一律 fail-closed 成
// 503 dependency_unavailable —— 不伪造结果、不返回空报告。
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"tellurion-analytics/internal/analysis"
	"tellurion-analytics/internal/httpapi"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8087", "listen address")
	flag.Parse()

	svc, err := analysis.New(analysis.Options{
		Store:    analysis.NewMemory(),
		Executor: analysis.AsyncExecutor{},
	})
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
