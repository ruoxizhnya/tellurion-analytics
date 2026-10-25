---
status: active
last-verified: 2026-10-26
verified-by: H6（R2 owner 落本仓 + 触发条件成文于 §6.4；`service` 服务间角色入策略表，ADR-0004）
---

# API Schema — tellurion-analytics

> **实现状态（2026-10-25）**：8 个端点全部在 `internal/httpapi` 落盘；计算面在 `internal/analysis`（纯函数）；**PG Store、启动迁移与四个上游适配器已接线**（`cmd/analyticsservice` 的 `-dsn` / `-core-url` / `-data-url` / `-artifact-url` / `-orchestration-url`，各自也可用同名 `ANALYTICS_*` 环境变量）。
> **未配置的端口**仍 fail-closed 成 `503 dependency_unavailable`，**不伪造结果、不返回空报告**。sensitivity 的父作业队列（X5b / W6）已接：提交建 pending 报告，`GET /reports/{id}` 轮询父汇总并逐点回填。可用性取决于部署配置（`docs/TASKS.md` QT-43 施工 ② / W8 记录）。

> **契约地位**：HTTP，**全部只读语义**（POST 表示"提交计算"，不改上游）。重计算走异步 job。
> 本文件是**本仓对外接口的唯一真相源**。代码与它不一致时，先判断谁错：契约错 → 改本文 + 立 ADR；实现错 → 改代码。
> 跨仓消费方（core / agent / ui / 其他服务）按本文件编码，**不读本仓实现**。
> **请求 / 响应形状**：本文只冻结**路径、错误码、幂等与兼容**；逐端点的目标签名、DTO 字段与版本策略见 `ARCHITECTURE.md` §9（M8 接口对表），两处必须同步改，不复制形状。
> **鉴权（2026-10-25 已接线，ADR-0003；2026-10-26 加 `service` 档，ADR-0004）**：机制在 kit `v0.1.1` 的 `auth/httpadapter`（Bearer → `Parse` →
> `kind=access` → 角色 any-of），策略表在本仓 `internal/httpapi/policy.go`；**全部端点**过闸且 fail-closed：
> 未装配 Authorizer → 503，无凭证 / 坏 token → 401，越权 → 403。6 个 POST = trader（+ admin + **service**），2 个 GET = viewer（+ trader + admin + **service**）。
> 无 secret + 回环 = 开放；无 secret + 非回环 = **拒绝启动**。现状登记见 §7。
> **前置现状**：sensitivity 的父作业队列走 G7/X5b（`tellurion-orchestration` QT-44，**W6 已落**）；结果读取走 core 的 **R1 持久读面**（B6-3 `8537ffd`，2026-10-25：`backtest_jobs.result_status` 是 `status` 的唯一权威，「跑完但无效」返 200 不再 404；队列态非 completed 的行仍 404，偏差见 §6.1）；基准走 X1b（已落）；holdings / 因子暴露走 interim artifact 面（R2 形状未冻结）。缺依赖即报错，不降级出假报告。

## 1. 接口面

| 方法 | 路径 | 入参 → 出参 |
|---|---|---|
| POST | `/api/analysis/metrics` | `{run_id}` → 绩效指标全集（含收益类补齐） |
| POST | `/api/analysis/benchmark` | `{run_id, benchmark_code}` → 超额 / IR / 跟踪误差 / 滚动对比 |
| POST | `/api/analysis/attribution` | `{run_id, method: brinson\|factor}` → 归因表 |
| POST | `/api/analysis/sensitivity` | `{run_id, param_grid}` → 202 `{report_id, status:"pending", parent_job_id}`（**异步 job**；父作业 = X5b） |
| POST | `/api/analysis/montecarlo` | `{run_id, n, seed}` → 分布 / 置信区间（**异步 job**） |
| POST | `/api/analysis/correlation` | `{run_ids[]}` → 相关性矩阵 |
| GET | `/api/analysis/reports/{id}` | 报告（JSON / HTML） |
| GET | `/api/analysis/reports/{id}/export?format=html\|csv` | 导出（**pdf 已裁为非目标**，见 `ARCHITECTURE.md` §9.7 R5） |

**响应公共前缀**：所有**同步计算端点**（metrics / benchmark / attribution / correlation）与 `GET /reports/{id}` 的 2xx 报告体，都以 `{report_id, run_id, analysis_type, status}` 开头，随后才是各类型载荷。异步提交端点（sensitivity / montecarlo）只返回 `{report_id, status:"pending"}`；sensitivity 额外带 `parent_job_id`（X5b 父作业，2026-10-25 加，可缺省），完整信封由 `GET /reports/{id}` 给出。

**sensitivity 的完成语义（2026-10-25，W8）**：
- 提交成功只代表网格已交给父作业，报告 `pending`；`GET /reports/{id}` 会读一次父作业并推进（读路径轮询，无后台线程）。
- 父作业落定后，`analysis.sensitivity_runs.points` 一次性写满，点顺序 = 网格展开顺序（参数名字典序、最后一维最快）；报告只有在**每个点都 completed** 时才 `completed`。
- 任一点 `invalid` / `failed` ⇒ 报告 `failed`（`error` 逐点点名，points 仍完整落库）；**缺失的点不补齐**（父 total / 点数对不上 ⇒ `failed` 且不写 points）。
- 上限 100 点（与 orchestration 的 `MaxBatchTasks` 同值），超限 `400 invalid_params`。

**两处实现口径（契约已同步）**：

- `metrics` 的 `computed` 只有 `volatility / var_95 / cvar_95`；**`beta` 不在 metrics 报告里**——它需要基准序列，归 `benchmark` 报告（`ARCHITECTURE.md` §9.4）。`sources` 标注 `from_result` 与 `computed` 各自的来源。
- `correlation` 的报告 `run_id` 是**去重保序后的逗号拼接**（如 `"r1,r2,r3"`）；矩阵行序即 `strategies` 顺序。

**错误体（本轮契约化）**：`run_result_incomplete` 的响应带机器可读 `missing`（缺哪个输入），取值词表见 §6；其他错误码不带该字段。调用方**不应**解析 `error` 文本。

## 2. 错误码与 fail-closed 语义

> 错误码与 `ARCHITECTURE.md` §9.4 **双向一一对应**；本节是 code 的**唯一词表**，新增 code 必须先改这里。

| 情况 | 返回 | 说明 |
|---|---|---|
| 无凭证 / token 过期 | 401 `unauthenticated` | 不重试，补齐凭证。✅ 全部端点（2026-10-25） |
| 角色不足（读 viewer / 写 trader / 服务间 service） | 403 `forbidden` | 不重试。✅ 全部端点（2026-10-25；`service` 档 2026-10-26） |
| 鉴权闸未装配 | 503 `auth_unavailable` | 不重试；服务端装配问题。✅ |
| 请求体 / 参数非法（含 `n<=0`、空网格、ID 数越界） | 400 `invalid_params` | 修正后重试 |
| 归因方法不支持 | 400 `method_not_supported` | 只接受 `brinson` / `factor` |
| 缺 `seed`（蒙特卡洛） | 400 `seed_required` | **fail-closed**：没有种子就不算（`seed=0` 是合法显式种子） |
| `run_id` 不存在 | 404 `run_not_found` | 不返回空报告（空报告会被误读为"零收益"） |
| 基准未登记 | 404 `benchmark_not_registered` | 不自动抓数据 |
| 报告不存在 | 404 `report_not_found` | 不重试 |
| 回测结果 **invalid** | 409 `run_result_invalid` + `invalid_reasons` | **拒算**；不得当 completed（K9 三值原样透传） |
| 回测结果 **failed** | 409 `run_result_failed` + `error` | 拒算；不当"可重试成功" |
| 上游结果不完整（缺曲线 / holdings / 因子暴露 / artifact 口径） | 409 `run_result_incomplete` + `missing` | 拒算，不猜；`missing` 点名缺哪个输入（词表见 §6） |
| 基准已登记但取不到序列 | 409 `benchmark_data_unavailable` | 不回落别的基准 / 不拿价格指数冒充全收益 |
| 报告未完成（导出 / 读取报告体） | 409 `report_not_ready` | 轮询 `GET /reports/{id}` |
| 依赖端口未配置 / 未接线（QT-44 队列；或部署未给上游 URL） | 503 `dependency_unavailable` | **不降级**：不伪造结果、不返回空报告、不回落别的数据源；补齐配置或接线后重试 |

**原则**：宁可报错，也不产出一份"看起来正常"的错误报告。`invalid` / `failed` / `incomplete` 三者**分开报**——它们对调用方的处置完全不同（换 run / 修上游 / 等数据）。`503` 是**部署态**问题（代码在、依赖没接），与业务错误分开，调用方应重试或联系运维，**不应**把它当"该 run 没数据"。

## 3. 幂等性与重试

- 计算类端点按 `(run_id, analysis_type, params_hash, input_hash, seed, metrics_version)` 派生**确定性** `report_id`：同键重复提交复用已有报告（字节一致），不重复计算。
- `input_hash` 覆盖所有读取输入（结果体 / 基准 `dataset_hash` / holdings artifact / 因子数据集）：**输入换版 ⇒ 新报告**，不复用旧结论。
- `params_hash` 是请求参数（基准代码 / 归因方法 / 网格 / n 等）的 canonical hash；不同参数 ⇒ 不同 `report_id`。
- 异步 job 的提交返回 `{report_id, status:"pending", parent_job_id?}`，轮询用 `GET /reports/{id}`；**作业态（pending/running/completed/failed）不是 K9 三值**，报告只在 completed 时存在。
- sensitivity 的父作业幂等键 = `analysis-sensitivity:<report_id>`；orchestration 侧再按 `plan_hash` 去重，同网格重复提交只会有同一个父作业。`points` 只能从空写满一次（写满后不可变）。
- `seed` 是 `*int64`：缺省 / `null` ⇒ `400 seed_required`；**`0` 是合法的显式种子**（可复现），不套用引擎「0 = time-based」语义（`ARCHITECTURE.md` §9.1 F6）。
- `POST /correlation` **不返回部分矩阵**：任一 run 不可用（缺 / invalid / failed / 点数不足）即整体拒绝。
- 归因报告里的 `residual` 是**独立字段**，不得摊进 `contributions`；它同时落 `analysis.attribution`（`db-schema.md` §1）。
- 只读 GET 与导出可任意重试；导出未完成报告 → 409。

## 4. 版本与兼容

> 逐端点 / 逐符号的版本策略见 `ARCHITECTURE.md` §9.6；本节只留三条总则。

- 报告**格式**演进要保留旧版本读取（历史报告不能失效）。
- 指标定义变更算 breaking：老报告不重算，新报告注明口径版本（`metrics_version`）。
- 端点只增不删。导出格式：html / csv 稳定；**pdf 已裁为非目标**（R5，2026-10-24），不再列入承诺面。

## 5. 不提供什么（边界）

- 不做数据抓取（基准行情归数据服务）
- 不做回测 / 策略执行；sensitivity 只把网格**提交给 X5**，不在本仓跑引擎
- 不做因子研究（`ic_analysis` / `factor_returns` 那套留在 core；两表实为 **public 无前缀**，不写 `quant.`）
- 不做实时分析（全部按 run 的离线计算）
- **不解析 job 错误串还原三值**：结果读取必须走 K9-correct 的读取面（`ARCHITECTURE.md` §9.1 F1 / R1），本仓不靠 `"backtest produced an invalid run: ..."` 这种字符串猜状态。

## 6. 上游读取契约（R1 结果面 / R2 artifact 面）v1

> 这不是对外 API，但它是**本仓读上游**的冻结形状——`409 / 404 / 503` 的归因全靠它，所以同样按契约管理。
> 契约版本：`analysis.UpstreamContractVersion = "v1"`（`internal/analysis/upstream_contract.go`）；形状镜像在 `internal/analysis/model.go`。
> **owner = 本仓（analytics），不迁 `tellurion-contracts`**：该仓至今**没有** `analysis` 包——实测（2026-10-26）远端最新 tag `v0.6.0` 与 `origin/main` 的业务包只有 `dataprotocol` / `domain` / `engine` / `fees`。迁移的**触发条件**与实测依据见 **§6.4**。
> **版本策略**：加字段 = MINOR（`omitempty`，消费端忽略未知字段）；**改名 / 删字段 / 改语义 = breaking**，先改本节再改代码。
> **钉死方式**：`TestUpstreamContractWireShape` 用 golden JSON 往返比对（字段名被改 / 被删即红）；`TestUpstreamContractArtifactFailClosed` 钉必填项；`TestUpstreamContractOwnerStaysLocal` / `TestUpstreamContractMigrationTriggerDocumented` 钉 owner 与触发条件（§6.4）。

### 6.1 R1 结果面 — `BacktestResponse`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `id` | string | 否 | run id（幂等键来自请求，不依赖它） |
| `status` | `completed \| invalid \| failed` | **是** | K9 三值，**原样透传**；上游权威是 core 的持久列 `backtest_jobs.result_status`（overlay 顺序：列 → 结果体 → completed）；缺失 / 未知 ⇒ `409 run_result_incomplete`（`missing=status`），**不解析错误串** |
| `error` | string | failed 时 | 上游原文透传（`run_result_failed`） |
| `invalid_reasons` | string[] | invalid 时 | 原样透传（`run_result_invalid`） |
| `start_date` / `end_date` | string | 否 | 期间 |
| `total_return` / `annual_return` / `sharpe_ratio` / `sortino_ratio` / `max_drawdown` / `win_rate` / `avg_holding_days` / `calmar_ratio` | number | 否 | 上游指标，本仓只读 |
| `max_drawdown_date` | string | 否 | 同上 |
| `total_trades` / `win_trades` / `lose_trades` | int | 否 | 同上 |
| `portfolio_values` | `{date, total_value}[]` | completed 时 | 净值曲线；空 ⇒ `409 run_result_incomplete`（`missing=portfolio_values`） |

**fail-closed 规则**：`status` 不在三值内 ⇒ 拒算。B6-3 之后「无 status」**不再是异步结果的合法旧编码**，出现它只意味着上游部署没升到 R1 —— 照样 409，不当 completed、不猜状态。**禁止**从 `"backtest produced an invalid run: ..."` 之类字符串反推状态。

**残留偏差（在 core 侧，本仓不猜、也不二次调用去补）**：

1. report 端点仍要求**队列态** `status=="completed"` 才可读 ⇒ pending / running 与引擎故障行（`status=failed` + `result_status=failed`）经持久面一律 **404**，本仓映射成 `404 run_not_found`；K9 的 `failed` 因此只能经 core 的内存态路径到达（`409 run_result_failed`）。
2. Migration 059 把迁移前的完成行回填成 `completed`（旧代码当时把 invalid 写成 `status=failed`），那批历史行读不回 invalid。

两条各有一条用例钉住（`internal/upstream.TestResultReader_PersistentK9ReadPlane`，按 core 的 overlay 建模）；要把它们变成正确语义，得先裁「读面按队列态还是结论态放行」，属 core 侧的改动。

### 6.2 R2 artifact 面 — `PortfolioSnapshot` / `FactorExposures`

`PortfolioSnapshot`（Brinson 输入）：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `holdings` | `{symbol, sector, weight, period_return}[]` | **是** | 期末持仓；为空 ⇒ `missing=holdings` |
| `benchmark_sector_weights` | `{sector: number}` | **是** | 基准分行业权重；缺失 ⇒ `missing=benchmark_sector_weights` |
| `benchmark_sector_returns` | `{sector: number}` | **是** | 基准分行业收益；缺失 ⇒ `missing=benchmark_sector_returns` |
| `benchmark_return` | number | 否 | 基准期收益（Brinson 算术用） |
| `dataset_hash` | string | **是** | artifact 版本身份，**进 `input_hash`**；缺失 ⇒ `missing=holdings.dataset_hash` |

`FactorExposures`（因子归因输入）：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `factors` | `{name, exposure, factor_return}[]` | **是** | 为空 ⇒ `missing=factor_exposures` |
| `dataset_hash` | string | **是** | 同上，进 `input_hash`；缺失 ⇒ `missing=factor_exposures.dataset_hash` |

**为什么 `dataset_hash` 必填**：它参与派生 `report_id` 的 `input_hash`（§3）。缺了就无法区分 artifact 换版，同键会**复用旧结论**。校验点必须在算 `input_hash` **之前**（`Service.Attribution`），不能只在计算函数里。
**为什么行业权重 / 收益必填**：缺失会被静默按 `0` 算出一份「看起来正常」的归因——必须拒算，不用成交明细倒推持仓（`ARCHITECTURE.md` §9.1 F2）。

### 6.3 `missing` 词表（闭集）

`run_result_incomplete` 的 `missing` 只能取下列标识；`IncompleteError.Detail` 的人读定位**不进 wire**。

| `missing` | 缺什么 |
|---|---|
| `run result` | 上游根本没返回结果体 |
| `status` | 结果缺 K9 `status`，或取值不在三值内 |
| `portfolio_values` | 净值曲线为空 |
| `portfolio_returns` | 收益序列不足（指标 / 蒙特卡洛） |
| `equity_curve` | 相关性输入的点数不足（`Detail` 带 run id 与下限） |
| `benchmark_series` | 已登记基准取不到序列 |
| `holdings` | Brinson 的持仓 artifact 为空 |
| `holdings.dataset_hash` | 持仓 artifact 缺版本身份 |
| `benchmark_sector_weights` | 基准分行业权重缺失 |
| `benchmark_sector_returns` | 基准分行业收益缺失 |
| `factor_exposures` | 因子暴露为空 |
| `factor_exposures.dataset_hash` | 因子数据集缺版本身份 |

新增标识必须先改本节（`writeError` 原样透出 `IncompleteError.Missing`，无兜底映射）。

### 6.4 DTO 的 owner 与「第二个使用方」的触发条件

**owner = 本仓（analytics）**，不迁 `tellurion-contracts`。裁决原文见
`tellurion:docs/design/qt-decoupling/cross-repo-decision-record-2026-10-25.md` ③（跨仓决策的
真相源在 core 仓；本仓只留指针与可执行判据）。

**触发条件（唯一）**：**core 或 orchestration 出现 import analytics DTO 的 Go 代码**。

- **HTTP 交互不算**：core / orchestration 只经 HTTP 调本服务（wire 形状一致即可）**不构成**
  第二个使用方——那是**传输**依赖，不是 **Go 类型**依赖。
- 触发时怎么迁：按 contracts ADR-0001 ① 把 DTO 迁到 `tellurion-contracts` 的 `analysis` 包；
  **端口方法集与校验语义不变**（`validateResultContract` / `validatePortfolioSnapshot` /
  `validateFactorExposures` 三者逐字保留，只换 DTO 的 owner）。
- **不迁的代价**（接受）：DTO 留在 `internal/analysis/model.go`，本仓与第二个使用方之间只能靠
  wire 契约对齐，不能靠编译器——所以 wire 形状由 §6.1 / §6.2 的 golden 用例钉死。

**实测依据（2026-10-26，本仓可自证的部分）**：

| 事实 | 实测 |
|---|---|
| core 对 `tellurion-analytics` 的 Go import | **0 处** |
| core 内 `PortfolioSnapshot` 命中 | 26 处，**全是 core 自己的私有符号**（`pkg/alert/manager.go:76` 等） |
| core 内 `FactorExposure` / `AnalysisReport` 命中 | **0 处** |
| orchestration 与本仓的交互 | **只经 HTTP**（`internal/upstream/sensitivity.go` 走 `POST /api/runs/parents`） |
| `tellurion-contracts` 的业务包清单 | `dataprotocol` / `domain` / `engine` / `fees`——**无 `analysis` 包** |

⇒ **当前没有第二个使用方**，触发条件未命中。

**钉死方式**（`internal/analysis/upstream_contract_test.go`）：

- `TestUpstreamContractOwnerStaysLocal`：本仓源码**不得** import `tellurion-contracts` 的任何包
  （owner 留在本仓 ⇒ 零跨仓类型依赖）。这条是本仓能自己证的**最强**判据：一旦有人把 DTO 挪进
  contracts 并改用它的类型，本仓就会有 contracts import ⇒ 红。
- `TestUpstreamContractMigrationTriggerDocumented`：本节（§6.4）必须在 `docs/api-schema.md` 里
  存在且含触发条件的两个要素（`core` / `orchestration` 的 **import** 措辞 + **HTTP 交互不算**）。
  防止「触发条件只活在某个人的脑子里」——裁决改了而文档没跟上 ⇒ 红。

## 7. 鉴权现状登记（2026-10-26）

> 本节记录**今天的事实**。目标口径见 `ARCHITECTURE.md` §9.7 R6；两者不得混读。

- **代码登记**：`httpapi.AuthStatus = "enforced"`（`internal/httpapi/auth_status.go`）。改鉴权必须同时改这个常量、重写 `TestAuthEnforcedCurrentState`，并同步 docs/api-schema.md §7
  与 docs/TASKS.md——不要只改代码。
- **事实**：`Server` 的每个端点都过 `requireAuth`（kit `auth/httpadapter`）：未装配 Authorizer → 503，无凭证 / 坏 token → 401，角色不足 → 403。策略表：6 个 POST = trader（+ admin + **service**），2 个 GET = viewer（+ trader + admin + **service**）（`internal/httpapi/policy.go`）。
- **可执行锚点**：`TestAuthEnforcedCurrentState` —— 8 条路由无凭证一律 401、6 个 POST 的 viewer 一律 403、trader 可执行、viewer 可读；`TestRouteTableEqualsPolicyTable` 钉住路由表 ≡ 策略表。
- **`service` 服务间角色（2026-10-26，裁-17 / ADR-0004）**：core 代机器调本仓时用 `service` 角色的长期 token。本仓 8 条端点**全部**开这一档——本仓没有审批 / 留痕 / 配置 / 交易端点，不存在「机器凭证代劳人的责任动作」这条风险面（详见 ADR-0004 的理由与代价）。
  - **签发与分发不在本仓**：本仓只保证「**有 token 就能用**」——不造 token、不存 token、不分发 token。签发入口是 kit `v0.1.2` 的 `IssueServiceToken`；签发与注入属 core QT-71 与 `deploy/k8s/` 部署清单（**跨仓腿**）。
  - **守卫**：`TestPolicy_ServiceRoleScope`（service 调 8 条端点都不被闸拦下 + viewer 打 POST 仍 403）、`TestPolicy_ServiceRoleCoexistsWithHumanRoles`（service 与真人角色并列写在同一条 `Roles`）、`TestPolicy_ServiceRoleDeclaredOnEveryRoute`（全表都开，新增端点漏表态即红）。
  - ⚠️ **已知跨仓缺口（不在本仓）**：kit `auth/httpadapter.Authenticate` 要求 subject 可解析为 `int64`，而裁-17 的生产口径 `sub=service:<repo>` 是非数字 subject ⇒ 生产 token 目前会被 kit 判 401，压根到不了本仓的角色判定。本仓的守卫用例证明的是**本仓策略表**正确，**不**证明生产 token 能走通；该缺口归 kit（放宽 subject 校验）或改裁-17 的 subject 口径，**已报协调者**（与 risk 的同型缺口同一处）。
- **启动闸**：有 `JWT_SECRET` 启用鉴权；无 secret + 回环 = 开放；无 secret + 非回环 = **拒绝启动**。
- **接线清单（已完成）**：① `AuthStatus` 常量与用例；② 本节与 §2 两行；③ `ARCHITECTURE.md` §9.7 R6；④ `docs/TASKS.md` QT-43 的「鉴权」条目；⑤ `service` 档（策略表 + 三条守卫用例 + ADR-0004 + 本节登记）。
