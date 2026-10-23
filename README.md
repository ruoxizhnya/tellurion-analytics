# tellurion-analytics

> module: `tellurion-analytics` ｜ 状态：**已启动（X4 代码落盘，上游端口未接线）**

分析服务：基准 / 归因 / 敏感性 / 蒙特卡洛。**纯函数、只读上游**——不持有跨请求状态、不写别人的表；只写自己的结果表 `analysis.*`。

## 目录

| 路径 | 内容 |
|---|---|
| `internal/analysis/` | 纯函数计算（metrics / benchmark / attribution / montecarlo / correlation / export）+ 端口 + Memory Store + DDL |
| `internal/httpapi/` | 8 个 HTTP 端点、严格解码、错误码映射 |
| `cmd/analyticsservice/` | 进程入口（默认 `127.0.0.1:8087`） |
| `docs/` | 本仓文档：台账 / 边界 / 决策 / 归档 —— 从这里进：`docs/README.md` |
| `AGENTS.md` | 本仓入口（构建、边界、禁止、依赖版本） |
| `doc.go` | 根包说明 |

## 快速开始

```bash
go build ./...
go vet ./...
go test ./...
go run ./cmd/analyticsservice -addr 127.0.0.1:8087
```

本仓**单独 checkout 就能跑通**上面三条。需要别的仓源码才过 → 边界错了，不是加 `replace` 能解决的问题。

> 进程当前只有 Memory Store、上游端口全 nil ⇒ 计算端点返回 `503 dependency_unavailable`；这是**有意 fail-closed**（不伪造结果）。现状与未落项见 `docs/ARCHITECTURE.md` §9.8、`docs/TASKS.md` QT-43 施工记录。

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
