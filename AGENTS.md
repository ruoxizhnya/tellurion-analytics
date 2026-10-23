# AGENTS — tellurion-analytics

> 本文件是**本仓**的唯一入口。判据：一个 agent 只 checkout 本仓，就能知道「怎么构建、边界在哪、依赖哪版契约、活在哪、禁止什么」，**不需要读别的仓的源码**。

## 1. 这是什么仓

分析服务：基准 / 归因 / 敏感性 / 蒙特卡洛。**纯函数、只读上游**——不持有跨请求状态、不写任何上游表；但**拥有自己的结果表** `analysis.*`（reports / benchmarks / sensitivity_runs / mc_runs / attribution，见 `docs/db-schema.md`）。

**状态**：已启动（2026-10-23，QT-43 施工 ①）——X4a~X4d 的代码与测试已落盘，`go build/vet/test` 全绿；**上游端口与 PG 尚未接线**，进程用 Memory Store，未接线端口 fail-closed 成 `503 dependency_unavailable`。现状详见 `docs/ARCHITECTURE.md` §9.8，未落项见 `docs/TASKS.md` QT-43 施工记录。

**今天的实现**：本仓自有 `internal/analysis`（纯函数 + 端口 + Memory Store + DDL）、`internal/httpapi`（8 端点）、`cmd/analyticsservice`。上游计算口径的参考实现仍在 core（`core:pkg/backtest/metrics`、`core:pkg/backtest/reporting`），本仓**只对照公式、不 import**。

## 2. 构建 / 测试（必须单仓可跑）

```bash
go build ./...
go vet ./...
go test ./...
```

- 这三条是 R1/R2 的判据：**单独 checkout 就能绿**。
- 若某一步需要别的仓的源码目录才过，那是**边界画错了**——停下来修边界，**不要**加 `replace ../xxx` 绕过。
- 本机同时改多个仓时可用 `go.work` 联调，但**它不入库**（`.gitignore` 已挡）。

## 3. 边界（硬约束）

**允许**

- 基准比较、归因分解、敏感性、蒙特卡洛等只读计算
- 接受数据集快照 hash 作为输入（可复现）
- **写自己的结果表** `analysis.*`（只写自己 schema）

**禁止**

- 写**上游**表（core / data / risk / strategy 的任何 schema）——"只读"指的是上游，不是"什么都不写"
- 持有跨请求状态（结果落 `analysis.*`，不落内存）
- import core 的实现包
- 自己做数据抓取（那是 data 服务）

下面三条对所有仓都成立：

- **禁止** `replace` 指向仓外路径（单仓就不可构建了）。
- **禁止**提交 `go.work` / `go.work.sum`（本地产物；已在 `.gitignore`）。
- 新增第三方依赖前，先在第 4 节登记，再改 `go.mod`；没登记就加依赖 = 绕过评审。

## 4. 依赖的契约版本

| 依赖 | 版本 | 说明 |
|---|---|---|
| `github.com/ruoxizhnya/tellurion-contracts` | 未引入（待 S2 后 `v0.1.0`） | 契约仓的 `analysis` 包未发布；本仓暂以 `internal/analysis/model.go` 的本地 DTO 镜像冻结形状（只消费、不输出） |
| `github.com/ruoxizhnya/tellurion-kit` | 未引入（待 S2 后 `v0.1.0`） | 日志 / 统计 / HTTP；当前只用标准库 |

> **今日 `go.mod` 零 `require`**（2026-10-23）。这是临时状态，不是"以后也不依赖"：契约发布后按 `docs/ARCHITECTURE.md` §9.8 迁走本地镜像。

> 跨仓依赖**只认版本号**（伪版本或 tag），不认源码路径。契约变更走 `tellurion-contracts` 的发布，不走"顺手改一下"。

## 5. 活在哪、文档在哪

- **本仓台账**：`docs/TASKS.md`（唯一）。**没有单号别开工。**
- 文档导航：`docs/README.md`；边界与依赖方向：`docs/ARCHITECTURE.md`；决策：`docs/adr/`。
- 跨仓背景（只读，不在本仓改）：`tellurion:docs/design/qt-decoupling/repo-topology-and-naming.md`、`tellurion:docs/TASKS.md`。

> 跨仓引用一律写 `repo:path` 纯文本，**不做可点击相对链接**——独立 checkout 时 `../../` 必断，R2 会永远红。

## 6. 文档真相源（改功能前先看这里）

本仓的「功能边界」由三份文件钉住，**它们是唯一真相源，不是参考读物**：

| 维度 | 文件 |
|---|---|
| 数据形状（我拥有哪些表、谁能写） | `docs/db-schema.md` |
| 接口形状（对外暴露什么、错误码、幂等） | `docs/api-schema.md` |
| 流向与顺序（数据怎么走、哪里 fail-closed） | `docs/dataflow.md` |

**变更触发是硬约束**（完整表见 `docs/README.md` §3）：改表 → 改 `db-schema.md`；改接口 → 改 `api-schema.md`；改链路 → 改 `dataflow.md`；改分层 → 改 `ARCHITECTURE.md` + 一条 ADR；删档/迁移/换工具 → 一条 ODR。PR 里代码动了而对应文档没动，应被打回。
