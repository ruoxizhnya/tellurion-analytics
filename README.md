# tellurion-analytics

> module: `tellurion-analytics` ｜ 状态：**已启动（X4 代码落盘 + PG / 上游读取端口接线）**

分析服务：基准 / 归因 / 敏感性 / 蒙特卡洛。**纯函数、只读上游**——不持有跨请求状态、不写别人的表；只写自己的结果表 `analysis.*`。

## 目录

| 路径 | 内容 |
|---|---|
| `internal/analysis/` | 纯函数计算（metrics / benchmark / attribution / montecarlo / correlation / export）+ 端口 + Memory / PG Store + DDL |
| `internal/upstream/` | 上游读取适配器（结果 / X1b 基准 / holdings / 因子暴露） |
| `internal/httpapi/` | 8 个 HTTP 端点、严格解码、错误码映射、全端点鉴权闸（`auth.go` = kit 适配，`policy.go` = 策略表） |
| `cmd/analyticsservice/` | 进程入口（默认 `127.0.0.1:8087`） |
| `docs/` | 本仓文档：台账 / 边界 / 决策 / 归档 —— 从这里进：`docs/README.md` |
| `AGENTS.md` | 本仓入口（构建、边界、禁止、依赖版本） |
| `doc.go` | 根包说明 |

## 快速开始

```bash
go build ./...
go vet ./...
go test ./...

# 有 PG：连接 + 启动迁移 + 持久化 analysis.*
ANALYTICS_DATABASE_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_analytics?sslmode=disable' \
ANALYTICS_CORE_URL='http://127.0.0.1:8085' \
ANALYTICS_DATA_URL='http://127.0.0.1:8081' \
ANALYTICS_ORCHESTRATION_URL='http://127.0.0.1:8088' \
go run ./cmd/analyticsservice -addr 127.0.0.1:8087
```

本仓**单独 checkout 就能跑通**上面三条。需要别的仓源码才过 → 边界错了，不是加 `replace` 能解决的问题。

> 不给 `-dsn` 时进程用 Memory Store（仅本地 / 测试）；未配置的上游端口保持 nil，对应端点返回 `503 dependency_unavailable`——这是**有意 fail-closed**（不伪造结果）。
> 鉴权：有 `JWT_SECRET` 时启用（6 个 POST = trader + admin + **service**，2 个 GET = viewer + trader + admin + **service**）；无 secret + 回环 = 开放，无 secret + 非回环 = **拒绝启动**。`service` 是服务间角色（core 代机器调本服务用，ADR-0004）：签发与分发**不在本仓**，本仓只保证「有 token 就能用」。现状与未落项见 `docs/ARCHITECTURE.md` §9.8、`docs/TASKS.md` QT-43 施工记录。

## 边界

**可以**：基准比较、归因分解、敏感性、蒙特卡洛等只读计算；接受数据集快照 hash 作为输入（可复现）

**不可以**：写任何**上游**表（只读是硬边界；自己的 `analysis.*` 可以写）；持有跨请求状态；import core 的实现包；自己做数据抓取（那是 data 服务）

## 文档

- 文档导航：`docs/README.md`
- 本仓台账：`docs/TASKS.md`（唯一；跨仓任务只留指针）
- 边界与依赖方向：`docs/ARCHITECTURE.md`
- 决策记录：`docs/adr/`

## 许可

MIT（见 `LICENSE`）。
