# AGENTS — tellurion-analytics

> 本文件是**本仓**的唯一入口。判据：一个 agent 只 checkout 本仓，就能知道「怎么构建、边界在哪、依赖哪版契约、活在哪、禁止什么」，**不需要读别的仓的源码**。

## 1. 这是什么仓

分析服务：基准 / 归因 / 敏感性 / 蒙特卡洛。**纯函数、只读上游**——不持有跨请求状态、不写任何上游表；但**拥有自己的结果表** `analysis.*`（reports / benchmarks / sensitivity_runs / mc_runs / attribution，见 `docs/db-schema.md`）。

**状态**：已启动（2026-10-23，QT-43 施工 ①）——X4a~X4d 的代码与测试已落盘，`go build/vet/test` 全绿；**PG Store 与上游读取端口已于 2026-10-24（施工 ②）接线**，**sensitivity 逐点回填于 2026-10-25（W8）接通 X5b 父作业**（执行侧 worker 同在 2026-10-25 落 core `tellurion:cmd/worker`，真进程 E2E 已改走 worker HTTP 协议，不再 SQL 模拟），**结果读取于 2026-10-25 换到 core 的 R1 持久读面**（B6-3 `8537ffd`：`backtest_jobs.result_status` 是 `status` 的唯一权威）；鉴权与出站服务间凭证同日接线；**R2 的 DTO owner 于 2026-10-26 落定为本仓**（裁决 ③：触发条件 = core / orchestration 出现 import analytics DTO 的 Go 代码，HTTP 交互不算），**`service` 服务间角色于 2026-10-26 入策略表**（ADR-0004，本仓 8 条端点全开；签发与分发不在本仓）；未配置的上游端口仍 fail-closed 成 `503 dependency_unavailable`。现状详见 `docs/ARCHITECTURE.md` §9.8；**未落项只剩 K7（挂起，见 `docs/TASKS.md`）**，历史施工记录见 `docs/archive/tasks-archive-2026-11.md`（token 分发跨仓腿归 `tellurion:QT-77`；R1 偏差① 已于 2026-11-10 结清，偏差② 为不可恢复历史数据）。**定位裁决（2026-11-10，ADR-0005）**：本仓为独立只读分析服务/参考实现，core `metrics`/`reporting` 不迁移（保留冻结）；`SVC-AN-G3` 已关闭，K7 挂起。

**今天的实现**：本仓自有 `internal/analysis`（纯函数 + 端口 + Memory Store + DDL）、`internal/httpapi`（8 端点，全部过鉴权闸 + 按身份限流；`auth.go` 是 kit 适配，`policy.go` 是策略表，`limit.go` 是令牌桶，`GET /readyz` 是就绪探针）、`cmd/analyticsservice`。上游计算口径的参考实现仍在 core（`core:pkg/backtest/metrics`、`core:pkg/backtest/reporting`），本仓**只对照公式、不 import**。

## 2. 构建 / 测试（必须单仓可跑）

```bash
go build ./...
go vet ./...
go test ./...
```

> PG 集成与端到端用例是 opt-in（CI 无库时自动 skip）：设置
> `ANALYTICS_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_analytics_test?sslmode=disable'`
> 后再跑一次 `go test ./... -count=1`。

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
| `github.com/ruoxizhnya/tellurion-contracts` | **未引入**（实测 2026-11-12：远端最新 `v0.10.0` 的业务包只有 `broker` / `conformance` / `dataprotocol` / `domain` / `engine` / `fees` / `risk` / `run`，**无 `analysis` 包**） | R1 / R2 的 DTO **owner = 本仓**（裁决 ③，2026-10-25）：DTO 留在 `internal/analysis/model.go`，**不迁 contracts**。触发迁移的条件写死为「core 或 orchestration 出现 import analytics DTO 的 Go 代码」（HTTP 交互不算）——成文见 `docs/api-schema.md` §6.4，守卫 `TestUpstreamContractOwnerStaysLocal` |
| `github.com/ruoxizhnya/tellurion-kit` | `v0.5.0`（2026-11-12 随 `X-DEP-BUMP-CONSUMERS` 从 `v0.2.0` 升；2026-11-10 随 kit `KT-13` 从 `v0.1.4` 升到 `v0.2.0`；2026-10-25 引入 `v0.1.1`，同日随 `KT-11` 升到 `v0.1.4`） | 鉴权机制：`auth`（token 解析）+ `auth/httpadapter`（net/http 适配、启动闸、Policy）。本仓只写策略表，不另造 token 校验。**`v0.1.4` 起 `role=service` 的非数字 subject（裁-17 的 `sub=service:<repo>`）不再被 401** ⇒ 服务间调用才真能走到本仓策略表；`v0.5.0` 新增 stdlib `net/http` 版 `RequestIDHandler`（`KIT-REQUESTID-STDLIB`），本仓未接线，仍用 `auth/httpadapter` 那条链路 |
| `github.com/jackc/pgx/v5` | `v5.11.0` ⚠️ **分叉已登记** | `analysis.*` 的 PG 连接池与迁移执行（`internal/analysis/pgstore.go`）；选 pgx 而非 `database/sql` 驱动是为了直接拿 `pgxpool` 与 `pgtype` 的 NULL 语义。**本仓是全组里最高的 pgx，别按别的仓那句「与 core 对齐」推断**：2026-10-26 实测 core / kit / data / strategy / agent / orchestration / broker 七仓 = `v5.7.3`，risk = `v5.7.5`，**本仓 = `v5.11.0`**。三档并存不是同一把刀——跨仓抄 `pgxpool` / `pgtype` 用法前先核目标仓版本；统一到一档要单独立单，不在顺手 `go get -u` 里做 |
| Go 工具链（`go.mod` 的 `go` 指令） | `1.26.11`（2026-11-12 随 `X-DEP-BUMP-CONSUMERS` 从 `1.25.0` 升；下限由 `tellurion-kit v0.5.0` 的 `go 1.26.11` 决定） | 本机 Go 1.25.14 + `GOTOOLCHAIN=auto` 会自动切到缓存的 go1.26.11 构建；CI 的 `setup-go` 必须显式钉 `1.26.11`，Dockerfile builder 若仍是 `golang:1.25-alpine` 则依赖构建期自动下载工具链 |

> `go.mod` 现在有两条直接依赖：`pgx v5.11.0`（2026-10-24，施工 ②）与 `tellurion-kit v0.5.0`（2026-10-25 W3 鉴权接线时是 `v0.1.1`，同日随 kit `KT-11` 升到 `v0.1.4`，2026-11-10 随 `KT-13` 升到 `v0.2.0`，2026-11-12 随 `X-DEP-BUMP-CONSUMERS` 升到 `v0.5.0`）。传递依赖随 MVS 抬到 kit `v0.5.0` 的档位：`golang.org/x/net v0.60.0`、`golang.org/x/text v0.42.0`、`golang.org/x/crypto v0.57.0`、`golang.org/x/sync v0.23.0`、`golang.org/x/sys v0.48.0`。**contracts 零依赖，且不打算引入**——DTO owner 已裁 = 本仓（裁决 ③）；真要迁的那天，触发条件见 `docs/api-schema.md` §6.4。
>
> 跨仓依赖**只认版本号**（伪版本或 tag），不认源码路径。契约变更走 `tellurion-contracts` 的发布，不走"顺手改一下"。
>
> **鉴权三态（2026-11-12 收紧，同日修正第 3 态）**：有 `JWT_SECRET` → 全闸；无 secret + 无显式声明 → 只读降级（读端点公开、写端点
> `503 auth_unavailable`），其中**非回环绑定拒绝启动**；无 secret + **显式声明**（`AUTH_INSECURE_EXPOSURE=loopback-published`，
> 或显式 `AUTH_ALLOW_INSECURE=true` / `-allow-insecure=true`）→ **任何绑定都允许放开**，启动 WARN 同时写出「写面无鉴权」与
> 绑定地址，非回环绑定额外点名「已对外暴露」。`allowInsecure` 由 `AUTH_ALLOW_INSECURE` / `-allow-insecure` 配置，不再硬编码。
> 细节与锚点见 `docs/api-schema.md` §7、`docs/runbook.md` §1。

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
