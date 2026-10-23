// Package analytics 是 tellurion-analytics 的根包（无实现）。
//
// 实现按能力拆在子包里，不堆在根包：
//
//   - internal/analysis：纯函数计算（metrics / benchmark / attribution /
//     montecarlo / correlation / export）、消费端端口、Memory Store、DDL；
//   - internal/httpapi：8 个 HTTP 端点（docs/api-schema.md）；
//   - cmd/analyticsservice：进程入口。
//
// 根包保留空的包声明是为了让 `go build ./...` / `go list ./...` 从一开始
// 就能识别本 module。现状与未落项见 docs/ARCHITECTURE.md §9.8 与
// docs/TASKS.md 的 QT-43 施工记录。
package analytics
