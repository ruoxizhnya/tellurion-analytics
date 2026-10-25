---
status: active
last-verified: 2026-10-26
verified-by: H6（R2 owner 落本仓（§9.7 R2 + §5 依赖方向）；`service` 服务间角色入策略表，ADR-0004）
---

# 架构 — tellurion-analytics

## 1. 定位

分析服务：基准 / 归因 / 敏感性 / 蒙特卡洛。**纯函数、只读上游**（不写别人的表，只写自己的 `analysis.*`）。

它出仓的理由不是"计算重"（core 也能算），而是这些分析**互相独立、可并行、结果可缓存**，且天然是"给报告用的"，不进入决策热路径。

## 2. 分层

```
上游只读输入（结果 / 基准 / 因子值）
        ↓
   计算层（纯函数 + 显式 seed）
        ├── 同步：metrics · benchmark · attribution · correlation
        └── 异步：sensitivity · montecarlo（job）
        ↓
   analysis.reports / _benchmarks / _sensitivity_runs / _mc_runs / _attribution
```

## 3. 组件职责

| 组件 | 职责 | 不做 |
|---|---|---|
| 指标 | 绩效指标（补齐收益类） | 不改上游已算的值 |
| 基准 | 超额 / IR / 跟踪误差 | 不抓行情 |
| 归因 | Brinson / 因子归因 | 不做因子研究 |
| 敏感性 / 蒙特卡洛 | 网格与分布（异步、seed 显式） | 不读系统时钟做随机 |

## 4. 三份契约（功能的边界就在这三份文件里）

| 维度 | 文件 | 回答什么问题 |
|---|---|---|
| 数据形状 | [`db-schema.md`](db-schema.md) | 本仓拥有哪些表？谁能写？ |
| 接口形状 | [`api-schema.md`](api-schema.md) | 对外暴露什么？错误码与幂等怎么做？ |
| 流向与顺序 | [`dataflow.md`](dataflow.md) | 数据怎么流？哪里 fail-closed？ |

## 5. 依赖方向

```
kit（鉴权机制）+ pgx（PG 驱动）
        ↑
tellurion-analytics ──(只读)──► core / run（结果）
        └──(只读)──► tellurion-data（基准、因子值）
```

**今日实况（2026-10-26）**：`go.mod` 的直接依赖两条——`github.com/jackc/pgx/v5 v5.11.0`（PG Store 与迁移执行）与 `github.com/ruoxizhnya/tellurion-kit v0.1.4`（鉴权机制，2026-10-25 W3 接入时是 `v0.1.1`，同日随 kit `KT-11` 升——`v0.1.1` 会把裁-17 的 `sub=service:<repo>` 非数字 subject 判 401，服务间调用读不到本仓策略表）；**内部仓只有 kit 这一条**。**本仓不依赖 `tellurion-contracts`**：R1 / R2 的 DTO owner 已裁 = **本仓**（裁决 ③，2026-10-25 主任口径），本地形状冻结在 `internal/analysis/model.go`（§9.8），触发迁移的条件写死为「core 或 orchestration 出现 import analytics DTO 的 Go 代码」（HTTP 交互不算）——成文见 `api-schema.md` §6.4。

⇒ 依赖方向图里**没有 contracts 那一层**：DTO 只在本仓出现，不向其他仓输出；将来真迁（触发条件命中）时按 contracts ADR-0001 ① 迁，端口方法集与校验语义不变。守卫 `TestUpstreamContractOwnerStaysLocal` 钉住「本仓源码零 contracts import」。

## 6. 关键决策

- [`adr/0001-纯函数只读.md`](adr/0001-纯函数只读.md)
- [`adr/0002-报告不可变由PG触发器兜底.md`](adr/0002-报告不可变由PG触发器兜底.md)
- [`adr/0003-鉴权接入-kit适配与本仓策略表.md`](adr/0003-鉴权接入-kit适配与本仓策略表.md)
- [`adr/0004-service角色在本仓全表开放.md`](adr/0004-service角色在本仓全表开放.md)

## 7. 非目标（明确不做）

- 不做数据抓取
- 不做回测
- 不做因子研究
- 不做实时分析

## 8. 分期与状态

> 更新（2026-10-25，W8）：PG Store、启动迁移与**四个**上游适配器（结果 / X1b 基准 / artifact / X5b 父作业）已接线；未配置上游 URL 的端口仍 fail-closed（503）。sensitivity 的**逐点回填**已落（提交父作业 → 读路径轮询 → points）。详见 §9.8。

| 阶段 | 内容 | 状态 |
|---|---|---|
| X4a | metrics + benchmark（同步） | 🔶 代码 + PG Store 已落；`ResultReader` 已接 **core R1 持久读面**（`-core-url`，§9.7 R1）、`BenchmarkReader` 已接 X1b（`-data-url`），未配置仍 503 |
| X4b | attribution（Brinson / 因子） | 🔶 代码已落；`HoldingsProvider` / `FactorExposureProvider` 已接 interim artifact 面（`-artifact-url`），R2 形状未冻结 |
| X4c | sensitivity + montecarlo（异步 + seed） | 🔶 montecarlo 端到端可用（PG 持久化）；sensitivity 已接 X5b（提交 + 逐点回填，W8）。**执行侧 worker 已在 core 落地**（`tellurion:cmd/worker`，QT-44 core 腿 2026-10-25）：本仓 E2E 经真实 worker 协议（注册 / 领取 / 上报）驱动父汇总，不再 SQL 模拟 |
| X4d | 报告导出（html / csv；**pdf 已裁为非目标**，§9.7 R5） | 🔶 html / csv 可用且确定性；pdf 不做 |

## 9. 接口对表（M8 前置 · ✅ 2026-10-22）

> **本节是什么**：`tellurion:docs/TASKS.md`「五、X 线」公共前置 ② 要求的**接口对表本体**（模板 = `tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §4.1）。逐个 port / DTO / endpoint 列 **签名 + 错误语义 + 版本策略**；缺一列就是没对完表。
> **实测基线**：core `dbe4c68`（2026-10-22；QT-63 已落）。行号一律取 `git show HEAD:<path>`。复核：`git -C ../tellurion show HEAD:<path> | sed -n '<line>p'`。
> **列口径**：「今天」= 源码实测；「契约位」= S1（QT-05）后 contracts 的落点，写 `core-only` 表示今天**不满足** contracts ADR-0001 ① 的「≥2 个仓使用」判据（不是永久留 core）；「版本策略」按 QT-19 的 MAJOR / MINOR / PATCH 判定。
> **M5 口径**：六仓 ≠ 六进程（`tellurion:docs/design/qt-decoupling/repo-inventory.md` §1.2）。本仓今天不升格容器；**X4a 的纯函数迁移不需要容器**，C 组 HTTP 面是升格时的目标形状。

### 9.1 六条前置事实（每条都会改变施工判断）

| # | 事实 | 取证（core `dbe4c68`） | 对施工的后果 |
|---|---|---|---|
| F1 | **结果读取面没有 K9-correct 的持久读路径；`backtest_jobs.result` 有两种不兼容编码。** | 异步路径：invalid 走 `UpdateJobFailed`、**结果体直接丢弃**（`pkg/backtest/job/job.go:490-505`），有效结果才 marshal `domain.BacktestResult`（**无 status**，`pkg/backtest/job/job.go:518`）。同步路径：`SaveSyncResult` 把完整 `contracts.BacktestResponse`（**有 status**）写进同一列（`pkg/backtest/job/job.go:709-741`）。`GET /api/backtest/:id` 返回的是 **job 行**（`cmd/analysis/handlers_backtest.go:160-172`）；`lookupBacktestResponse` 要求 `job.Status=="completed"` 再按 `BacktestResponse` 反序列化（`cmd/analysis/handlers_backtest.go:357-377`）⇒ 异步行的 status 为空、invalid 行直接 404。 | X4a **不能**按今天的读路径安全开工；解析错误串 `"backtest produced an invalid run: ..."` 来还原三值是**禁止项**。需先裁 **R1**：(a) core job 层把 invalid 落成 status=invalid + 保留结果体，或 (b) `run`/artifact 读取面（QT-44 / QT-63）提供 K9-correct 读取。**R1 落地前 QT-43 ⛔。** ⇒ **订正（2026-10-25）**：R1 已按 (a) 落地（core B6-3 `8537ffd` 新增持久列 `result_status`，两轴不合并），本行取证是基线 `dbe4c68` 的历史快照；今天的事实与两条残留偏差见 §9.7 R1。 |
| F2 | **Brinson / 因子归因的输入不在结果 DTO 里。** | `domain.BacktestResult`（`pkg/domain/backtest.go:5-39`）只有聚合的 `PortfolioValues`（`Date / TotalValue / Cash / Positions`，`pkg/domain/backtest.go:41-45`）与 `Trades`——**没有逐标的持仓快照、没有因子暴露**。`BacktestState.Tracker` / `TargetPositions` 只在内存（`pkg/backtest/state/state.go:30-75`），不落结果。 | Brinson 需要**期末持仓 artifact**；因子归因需要**逐期因子值/暴露**。两者都是新的读取输入（**R2**）。**不许**用成交明细倒推持仓（T+1、部分成交、手数、多空都会让 `trades ≠ positions`）。 |
| F3 | **Sensitivity 不是纯函数：它要再跑回测，而本仓「不做回测」。** | 设计 §4 说异步 job；job / batch / walkforward 的 owner 是 X5（`tellurion-orchestration:docs/TASKS.md` QT-44）；`pkg/backtest/batch` + `walkforward` 仍在 core；contracts 仓 `JobQueue` **零命中**。 | X4c 的 sensitivity 必须把参数网格**提交给 X5** 再读结果——依赖 QT-44（**R3**）。只有蒙特卡洛 / bootstrap 是纯函数，可以先落。**不许**在本仓内起回测循环。 |
| F4 | **基准序列的粗粒度读取面尚未建；口径必须先钉住。** | 目标 `/datasets/resolve` + `/datasets/{hash}` 属 X1b（`tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §4.1 C 组），今天 data-service 只有逐 symbol 面（同文 D 组）；`docs/dataflow.md` §3 已把「全收益 vs 价格」列为超额失真的失败点。 | X4b 的基准输入依赖 X1b，或一个**显式** interim adapter（读逐 symbol OHLCV + 登记 `adjustment_basis`）。本仓**不抓行情**。`analysis.benchmarks` 只登记 `{code, source, adjustment_basis, dataset_hash}`（**R4**）。 |
| F5 | **结果里已有的指标不得重算**（否则同一指标有两个值）。 | `BacktestResult` 已有 Sharpe / Sortino / MDD / Calmar / 交易计数；`metrics.CalculateMetrics` 返回**另一个** `domain.RiskMetrics`，其中也含 Sharpe / Sortino / MDD（`pkg/backtest/metrics/performance.go:13`）。红线见 `docs/db-schema.md` §3-3。 | `metrics` 响应必须**分区**：`from_result`（权威，原样）与 `computed`（补算：Volatility / Beta / VaR95 / CVaR95）+ `metrics_version` + `sources`。禁止用 `RiskMetrics.SharpeRatio` 覆盖结果里的 Sharpe。验收：同一 run 的 Sharpe 与结果**逐位一致**。 |
| F6 | **seed 的零值语义与引擎不同。** | 引擎 `Config.Seed == 0` ⇒ time-based（`pkg/backtest/engine.go:35`、`pkg/backtest/engine.go:309-314`）；`analysis.mc_runs.seed` NOT NULL（`docs/db-schema.md` §1/§3）。 | 本仓 `seed` 是 `*int64`：缺省 / null ⇒ `400 seed_required`；**`0` 是合法的显式种子**（`rand.NewSource(0)`，可复现），**不套用**引擎「0=time-based」。PRNG 冻结为 `math/rand.New(rand.NewSource(seed))`（与引擎同源；换算法 = breaking）。 |

**零实现核对（③-② 的「真实缺口」）**：设计 §2 引的 `engine.go` 第 1059 行只有一行注释——HEAD 对应位置是 **cache warming** 注释（`pkg/backtest/engine.go:1149`），与基准 / 归因无关。生产代码 grep `Brinson|TrackingError|InformationRatio|MonteCarlo|monte_carlo` = **0 命中**；唯一 `sensitivity` 命中是 `pkg/strategy/options/types.go:67` 的期权 Greeks 注释。⇒ BE-05 / BE-06 是**真缺口**，不是「已实现只差搬」；X4a~X4c 是新实现，不是搬迁。

### 9.2 A 组 · 纯函数与 port

| 成员 | 今天（签名 + 取证） | 契约位 | 错误语义 | 版本策略 |
|---|---|---|---|---|
| `ResultReader`（目标） | **已落**（本仓端口 `internal/analysis/ports.go` + 适配器 `internal/upstream/upstream.go`，读 core R1 持久读面，§9.7 R1） | `tellurion-contracts`（新包 `analysis`；core 与本仓两个使用方）——**包未发布**，DTO 仍是本仓本地镜像 v1 | `GetResult(ctx, runID) (*contracts.BacktestResponse, error)`：`ErrRunNotFound` / `ErrRunNotCompleted`（pending/running，**今天未实现**：core 对队列态非 completed 的行一律 404 ⇒ 落 `ErrRunNotFound`）/ `ErrRunInvalid`（携带 `invalid_reasons`）/ `ErrRunFailed`（携带 `error`；持久面读不到，只有 core 内存态路径给得出）/ `ErrRunResultIncomplete`。**三值原样透传，本仓不重判** | 方法集 frozen；加方法 = breaking；`GetResult` 的 wire 形状由 QT-50/openapi 统一 |
| `BenchmarkReader`（目标） | 不存在；今天只能读 data-service 逐 symbol OHLCV（F4） | `contracts/analysis` | `GetSeries(ctx, code, start, end, basis) (Series, error)`：`ErrBenchmarkNotRegistered` / `ErrBenchmarkDataUnavailable`；**不回落「用价格指数冒充全收益」** | 加参数 = MINOR；改 `basis` 语义 = breaking（超额收益不可比） |
| `JobQueue`（目标） | 不存在；contracts 零命中（F3） | **QT-44** 的 port（不属本仓定义） | 提交网格 / 查询状态 / 取结果；失败逐条可归因 | 以 QT-44 对表为准；本仓只消费 |
| `metrics.CalculateMetrics` | `pkg/backtest/metrics/performance.go:13`：`CalculateMetrics(portfolioValues []domain.PortfolioValue, trades []domain.Trade, riskFreeRate float64) domain.RiskMetrics` | 目标：本仓（**搬迁，不 import**；依赖 contracts 的 domain 类型） | 空序列返回零值（`pkg/backtest/metrics/performance.go:15` / `pkg/backtest/metrics/performance.go:21`）——**本仓调用前必须 fail-closed**：空 / 不足 ⇒ 409 `run_result_incomplete`，不把零值当结果 | 指标定义 frozen（`metrics_version`）；改定义 = MAJOR/ADR |
| `metrics.CalculateTradeMetrics` | `pkg/backtest/metrics/performance.go:54` → `TradeMetrics`（`pkg/backtest/metrics/performance.go:44`） | 本仓内部（只有本仓用，按 ADR-0001 **不进 contracts**） | 同 `CalculateMetrics`：空输入返回零值，调用前拦住 | 同上 |
| `reporting.CompareReports` | `pkg/backtest/reporting/compare.go:175`；resolver `pkg/backtest/reporting/compare.go:128` 返回 `backtest.BacktestResponse`（`pkg/backtest/aliases.go:39` 是 contracts 别名） | 本仓（搬迁）；resolver 改为 `ResultReader` | `2 <= len(ids) <= 8`（`pkg/backtest/reporting/compare.go:176-181`，`MaxCompareIDs=8`）；去重保序；**invalid 进 `Invalid` 不进 `Entries`、解析失败进 `Missing`**（`pkg/backtest/reporting/compare.go:20-30` 注释与 `pkg/backtest/reporting/compare.go:175` 实现）；不因单条失败整体报错 | 响应字段只增；`Entries` 的「只含 completed」语义 frozen |
| `reporting.CalculateStrategyCorrelation` | `pkg/backtest/reporting/correlation.go:40` | 本仓（搬迁） | <2 策略 ⇒ error；单条曲线 <30 点（`pkg/backtest/reporting/correlation.go:15` `minEquityPoints`）⇒ error；前值 0 ⇒ 该日收益按 0（`pkg/backtest/reporting/correlation.go:95` 注释） | 矩阵语义 frozen（Pearson、按名字排序、截断到最短、对角 1.0）；改口径 = breaking |
| `reporting.RenderHTML` | `pkg/backtest/reporting/export.go:39`（`HTMLReportOptions` 在 `pkg/backtest/reporting/export.go:21`） | 本仓（X4d 搬迁） | 渲染失败返回 error；**今天只支持 html**——core handler 对非 html 直接 400（`cmd/analysis/handlers_backtest.go:270-272`，提示用浏览器打印成 PDF） | 输出格式只增；模板变更不改报告身份（`report_id` 不含渲染版本，导出按需重渲） |
| `data.FactorAttributor` | `pkg/data/factor_attribution.go:16` `AttributionStore` / `pkg/data/factor_attribution.go:30` `FactorAttributor` | **留 core / 数据服务**（因子研究侧） | 缺缓存 / 交易日不足 ⇒ error（`pkg/data/factor_attribution.go:50-80`） | **不搬进本仓**：它是「因子研究期归因」（IC / 分位），不是「回测结果归因」（Brinson / 因子贡献）。同名不同物，别复制 |

### 9.3 B 组 · DTO / 值对象

| 符号 | 今天（字段 / 取证） | 契约位 | 错误语义 | 版本策略 |
|---|---|---|---|---|
| `contracts.BacktestResponse` | `pkg/backtest/contracts/contracts.go:219`；K9 常量 `pkg/backtest/contracts/contracts.go:299-301`（completed / invalid / failed）；含 status、error、invalid_reasons、portfolio_values、trades | 已属 contracts（S1 后随 `domain` 迁） | status 是**唯一判据**；invalid 必带 reasons、error 空；failed 必带 error、reasons 空 | 字段只增（`omitempty`）；删 / 改名 = breaking；**不得加第四值** |
| `domain.BacktestResult` | `pkg/domain/backtest.go:5`：收益 / 风险 / 交易计数 / `PortfolioValues` / `Trades` / `UniverseMaxSize *int` / `InvalidReasons`；**无 status**（K9 裁决） | `contracts/domain`（S1 整包迁） | — | 加字段 = MINOR（位置化字面量红，QT-19 ③）；指标口径受 SPEC 冻结 |
| `domain.RiskMetrics` | `pkg/domain/types.go:127`：Volatility / Beta / Sharpe / Sortino / MDD / VaR95 / CVaR95 | `contracts/domain` | 零值 ≠ 有效（空输入）；本仓只把**补算**字段交出去 | 同 `BacktestResult`；Sharpe/Sortino/MDD 的重算值**不得**覆盖结果值（F5） |
| `metrics.TradeMetrics` | `pkg/backtest/metrics/performance.go:44` | 本仓内部（单使用方，不进 contracts） | — | 加字段 = MINOR |
| `Report`（目标） | 不存在；`analysis.reports` 待建 | 本仓（wire 形状入 openapi） | 只有 `status=completed` 才有 `report`；failed 带 `error` | `report_id` 的构成 frozen（含 `input_hash`，见 D 组）；加字段 = MINOR |
| `AnalysisJob`（目标） | 不存在 | 本仓 | `status ∈ {pending, running, completed, failed}`——**这是作业态，不是 K9 三值**，两者不许互相映射 | 加状态 = MINOR；语义变更 = ADR |
| `BenchmarkRef`（目标） | 不存在；`analysis.benchmarks` 待建 | 本仓 + data 只读引用 | 未登记 ⇒ `benchmark_not_registered`；登记了但取不到 ⇒ `benchmark_data_unavailable` | `adjustment_basis` 是身份的一部分；改它 = 新登记（新 `dataset_hash`） |
| `seed` | 引擎 `int64`（0 = time-based，F6） | 请求 / DB 列（`mc_runs.seed NOT NULL`） | 缺省 / null ⇒ `seed_required`；`0` 合法 | 显式种子语义 frozen；换 PRNG = breaking |

### 9.4 C 组 · 服务对外 HTTP 面（升格后）

> **鉴权（2026-10-25 已接线，ADR-0003；2026-10-26 加 `service` 档，ADR-0004）**：机制在 kit `v0.1.4` 的 `auth/httpadapter`（`Parse` + `kind==access` + 角色 any-of + 启动闸；`v0.1.4` 起 `role=service` 豁免 subject 的 `int64` 要求），策略表在本仓 `internal/httpapi/policy.go`。**全部端点** fail-closed：未装配 Authorizer → `503 auth_unavailable`，无 token / 过期 / refresh 当 access → `401 unauthenticated`，角色不足 → `403 forbidden`。6 个 POST = trader（+ admin + **service**），2 个 GET = viewer（+ trader + admin + **service**）；无 secret + 回环 = 开放，无 secret + 非回环 = **拒绝启动**。
> `service` 是**服务间**角色（core 代机器调本仓用）：本仓 8 条端点全是分析计算 / 报告读取，**没有**审批 / 留痕 / 配置 / 交易端点，故全表开放（ADR-0004）。签发与分发**不在本仓**——本仓只保证「有 token 就能用」。
> **wire 契约落点**：core `docs/openapi.yaml`（QT-50 ③-③）。今天它没有下表端点；下表在升格前是目标草案，**不复制进 openapi**。
> **错误码**：与 `docs/api-schema.md` §2 双向一一对应。

| 端点 | 目标签名（请求 → 响应） | 错误语义 | 幂等 / 版本策略 |
|---|---|---|---|
| `POST /api/analysis/metrics` | `{run_id}` → `200 {report_id, run_id, analysis_type:"metrics", status:"completed", metrics_version, from_result:{...}, computed:{volatility, var_95, cvar_95}, sources}` | `404 run_not_found`；`409 run_result_invalid`（带 `invalid_reasons`）/ `409 run_result_failed` / `409 run_result_incomplete`；`400 invalid_params`；`503 dependency_unavailable`（部署未配置 `-core-url`） | `report_id = hash(run_id, "metrics", params_hash, input_hash, seed, metrics_version)`；`input_hash` = 结果体 hash（同 `run_id` 结果变了 ⇒ 新报告，不复用旧报告）；同键返回已存报告（字节一致）。同步。**`beta` 不在 metrics 报告里**——它需要基准序列，归 `benchmark`（metrics 无基准入参，硬塞会造出无来源的值） |
| `POST /api/analysis/benchmark` | `{run_id, benchmark_code, window?}` → `200 {report_id, benchmark:{code, adjustment_basis, dataset_hash}, excess_return, tracking_error, information_ratio, beta, rolling:{window, series}}` | 上列 + `404 benchmark_not_registered`；`409 benchmark_data_unavailable`（含口径不一致）；`503 dependency_unavailable` | 幂等键含 `benchmark_code` + `window` + 基准 `dataset_hash`（基准数据换版 ⇒ 新报告）；同步。`window` 缺省 20，负数 ⇒ 400 |
| `POST /api/analysis/attribution` | `{run_id, method:"brinson"\|"factor"}` → `200 {report_id, method, contributions:[...], residual, total_return, benchmark_return?}` | `400 method_not_supported`；`409 run_result_incomplete`（缺 holdings / 因子暴露时**点名缺哪个**）；`404 run_not_found`；`409 run_result_invalid` / `run_result_failed`；`503 dependency_unavailable`（部署未配置 `-artifact-url`） | 同步；**不变量**：factor 的 `Σcontributions + residual == total_return`、Brinson 的 `allocation + selection + interaction == portfolio_return - benchmark_return`，容差 `1e-8`；residual **不得**摊进因子贡献，且落 `analysis.attribution`。R2 未裁前按 §9.8 的**临时 artifact 形状**实现（provider 端口不变） |
| `POST /api/analysis/sensitivity` | `{run_id, param_grid:[{name, values[]}]}` → `202 {report_id, status:"pending", parent_job_id?}`；队列未接线 → `503 dependency_unavailable` | `400 invalid_params`（空网格 / 空 name / 空 values / 重名 / >100 点）；`404 run_not_found`；`409 run_result_invalid/failed/incomplete`；`503` | `report_id` 由 `(run_id, grid_canonical_hash)` 派生；同键复用。**异步**；网格作为 batch 父作业提交给 X5b（W6），`GET /reports/{id}` 轮询 `GET /api/runs/parents/{id}` 并按计划顺序回填 `analysis.sensitivity_runs.points`；本仓**只提交与汇总、不执行** |
| `POST /api/analysis/montecarlo` | `{run_id, n, seed, method?:"bootstrap"\|"shuffle"}` → `202 {report_id, status:"pending"}` | `400 seed_required`（缺省 / null）；`400 invalid_params`（`n<=0`、`n>10000`、未知 method）；`404 run_not_found`；`409 run_result_invalid/failed/incomplete` | 幂等键含 `n` + `seed` + `method`；同键同报告；**本仓异步执行**（`Executor`），pending → completed/failed |
| `POST /api/analysis/correlation` | `{run_ids:[...]}` → `200 {report_id, strategies:[...], correlations:[[...]]}` | `400 invalid_params`（2..8 个去重后 ID）；`404 run_not_found`（点名 ID）；`409 run_result_invalid/failed`（任一 run 不可分析）；`409 run_result_incomplete`（<30 点） | **不返回部分矩阵**：任一 run 不可用即整体拒绝（与 compare 的「部分视图」不同，矩阵少一行会误导）。同步 |
| `GET /api/analysis/reports/{id}` | → `200 {report_id, run_id, analysis_type, status, report?, error?}` | `404 report_not_found` | 轮询端点：pending / running 也是 200（status 说明），只有 completed 才带 `report`；只读可重试 |
| `GET /api/analysis/reports/{id}/export?format=html\|csv` | → `200` 文件体（`Content-Disposition`） | `404 report_not_found`；`409 report_not_ready`；`400 invalid_params`（不支持的 format） | 同 `report_id` + format 的导出字节一致；**pdf ⛔ R5**（core 现状只有 html，`cmd/analysis/handlers_backtest.go:270-272`） |

### 9.5 D 组 · 写入口径（`analysis.*`）

| 表 | 主键 / 唯一键 | 写入语义 | 幂等 / 失败语义 |
|---|---|---|---|
| `analysis.reports` | PK `report_id`；UNIQUE `(run_id, analysis_type, params_hash, input_hash, seed, metrics_version)` | 只有 completed 才落报告体；**报告一旦 completed 不可变**；`input_hash` 覆盖所有读取输入（结果体 / 基准 dataset_hash / holdings artifact / 因子数据集） | 同键重复提交返回已存报告（字节一致）；同键不同内容 = fail-loud；体积上限按 `db-schema.md` §5。注意：`seed` 可空时 PG 唯一键**默认不拦 NULL 重复**：建表用 `NULLS NOT DISTINCT`（PG15+）或 0 哨兵 |
| `analysis.benchmarks` | PK `(code, adjustment_basis, dataset_hash)` | 只登记引用（不存行情本体，行情归 data） | 同键同 hash = no-op；同 code + basis 的新 dataset_hash = **新一行**（历史可查，不覆盖）；「当前」按 `registered_at` 取最新 |
| `analysis.sensitivity_runs` | PK `report_id`；`parent_job_id` 指向 X5b 父作业 | 网格 + 每个点的 `run_id` + 指标；父作业统一下发（W6） | 提交即落行（`grid` + `parent_job_id`，`points` NULL）；父作业落定后**一次写满** `points`，写满后不可变；缺 point ⇒ 报告 failed 且不写 points（不出「半张网格」） |
| `analysis.mc_runs` | PK `report_id` | `seed NOT NULL`、`n`、`method`、`input_hash`、结果摘要 | 同 `(run_id, n, seed, method, input_hash)` 复用；seed 缺失 = 拒写（`seed_required`） |
| `analysis.attribution` | PK `report_id` | `method`、`contributions`、`residual`、输入 hash（holdings artifact / factor dataset） | 同键复用；residual 必须落库（不许只在响应里出现） |
| 上游表 | — | **只读**，禁止写入、禁止 join | 跨 schema 取数只走只读 API / 视图（`db-schema.md` §2/§4） |

### 9.6 E 组 · 版本策略汇总（QT-19 口径）

- **frozen（改 = breaking + ADR）**：K9 三值语义与 `BacktestResponse` 字段名；`ResultReader` 的方法集；`report_id` 的构成；PRNG 算法（`math/rand` + 显式 seed）；相关系数矩阵口径（Pearson / 截断 / 对角 1.0）。
- **stable（改 = MINOR + CHANGELOG；语义变更 = MAJOR / ADR）**：既有端点路径与错误码；`Report` / `AnalysisJob` / `BenchmarkRef` 字段集（加字段 MINOR，改名 / 删字段 breaking）。
- **metrics_version**：指标定义变更 ⇒ **新版本**，老报告保留原版本、不重算；加一个指标 = MINOR；改既有指标定义 = MAJOR/ADR。
- **报告格式**：新格式必须保留旧读取器（历史报告不能失效，`docs/api-schema.md` §4）。
- **作业态 vs K9**：`AnalysisJob.status`（pending/running/completed/failed）是作业态，**不是** K9 三值；报告里的收益数字**不得**用作业态冒充回测 status。
- **路径**：只增不删；废弃走 `Deprecated` 头 + 一个 MINOR 周期。

### 9.7 待裁（R 组）与移交

| # | 事项 | 当前状态 | 建议 / 影响 |
|---|---|---|---|
| R1 | **K9-correct 的结果读取面**（F1） | ✅ **已落（core B6-3 `8537ffd`，2026-10-25）**：`backtest_jobs.result_status` 持久化 K9 结论，`GET /api/backtest/{id}/report` 以该列为 `status` 的唯一权威（列 → 结果体 → completed），`completed/invalid` 行返 200 不再 404。本仓 `internal/upstream.ResultReader` 换到这一面：**URL 与 DTO 都没变**（正是本节原先写的「只换面，端口不变」），缺 `status` 的分支保留但语义改成「上游没升到 B6-3」。**残留偏差**（`api-schema.md` §6.1）：端点仍按**队列态**放行 ⇒ pending / running 与引擎故障行（`failed`/`failed`）读成 `404 run_not_found`，K9 的 `failed` 经持久面到不了 409；Migration 059 把迁移前的完成行回填成 `completed`，旧 invalid 读不回 | 消费端已由 `TestResultReader_PersistentK9ReadPlane` 按 core 的 overlay 建模钉住。要把两条偏差变成正确语义，得先裁「读面按队列态还是结论态放行」——属 core 侧改动，不在本仓猜 |
| R2 | **holdings / 因子暴露 artifact**（F2） | 结果 DTO 无逐标的持仓；`BacktestState` 只在内存。**本仓已按临时形状接 interim artifact 适配器**：`PortfolioSnapshot{holdings, benchmark_sector_weights, benchmark_sector_returns}`、`FactorExposures{factors[], dataset_hash}`（`internal/analysis/model.go`），HTTP 面 `GET /runs/{id}/portfolio-snapshot` / `/factor-exposures`；未配置即 503。消费端形状已冻结为 **`UpstreamContractVersion=v1`**（`api-schema.md` §6.2：`dataset_hash` 与行业权重 / 收益必填） | **owner 已裁 = 本仓（analytics），不迁 contracts**（2026-10-25 主任口径，裁决 ③）：DTO 留在 `internal/analysis/model.go`，触发迁移的条件写死为「core 或 orchestration 出现 import analytics DTO 的 Go 代码」（HTTP 交互不算）——成文见 `api-schema.md` §6.4，守卫 `TestUpstreamContractOwnerStaysLocal` / `TestUpstreamContractMigrationTriggerDocumented`。真迁移时**端口方法集与校验语义不变**，只换 DTO 的 owner |
| R3 | **sensitivity 的 job 队列**（F3） | **已落（2026-10-25，W8）**：orchestration 的 X5b 父端点（W6）已接；本仓 `SensitivityQueue` = `Submit` + `Status`，`analysis.sensitivity_runs.points` 按父汇总逐点回填；未配置 `-orchestration-url` 仍 503 | 执行侧装配不在本仓（仍正确）：`base_run_spec` 只带 `run_id` 锚点，**core worker 的 `AnchorResolver` 负责从 core 的 `backtest_jobs` 恢复完整请求**；锚点形状不变，无需契约变更 |
| R4 | **基准数据面与口径**（F4） | **X1b 已落**（`tellurion-data` `/datasets/resolve` + `/datasets/{hash}`）；本仓 `BenchmarkReader` 已接 X1b，按登记的 `adjustment_basis` 取数，basis 不一致即 409 | 保持 `adjustment_basis` 在登记身份里；若将来改走别的数据面，只换适配器 |
| R5 | **PDF 导出** | **已裁（2026-10-24）= 非目标**：core 现状只有 html（用浏览器打印），无渲染器 | 从目标面移除；只承诺 html / csv（`api-schema.md` §1 / §4）。不引入渲染器、不立 ADR |
| R6 | **鉴权角色矩阵** | **已裁（2026-10-25）**：6 个 POST = trader、2 个 GET = viewer；机制在 kit `auth/httpadapter`，策略表在本仓 `internal/httpapi/policy.go`（ADR-0003）。**2026-10-26 加 `service` 档**（ADR-0004）：本仓 8 条端点全开 `service`（本仓没有审批 / 留痕 / 配置 / 交易端点，不存在「机器凭证代劳人的责任动作」）；签发与分发不在本仓 | 全端点 fail-closed；无 secret + 非回环拒绝启动；`service` 与真人角色**并列**写在同一条 `Roles`（kit 角色判定 any-of 不排序） |

### 9.8 实现现状（2026-10-24，QT-43 施工 ②）

> 本节的目的是让下一个 agent **只看本仓**就能知道「哪些真的能跑、哪些只是接口就位」。

**落地包**

| 路径 | 内容 |
|---|---|
| `internal/analysis/model.go` | 本地 DTO 镜像（`BacktestResponse` / `BenchmarkSeries` / `PortfolioSnapshot` / `FactorExposures`）+ `Report` / `AttributionRecord` / `MonteCarloRecord` |
| `internal/analysis/ports.go` | 5 个消费端端口：`ResultReader` / `BenchmarkReader` / `HoldingsProvider` / `FactorExposureProvider` / `SensitivityQueue`；`Executor`（Inline / Async） |
| `internal/analysis/{metrics,benchmark,attribution,montecarlo,correlation,stats}.go` | 六类纯函数计算；口径与 core 同式（Pearson / 截断最短 / 对角 1.0 / Brinson 三分解） |
| `internal/analysis/sensitivity.go` | 网格展开（≤100 点，与 `plan.ExpandBatch` 同序）、父作业进度推进、逐点回填与 fail-closed |
| `internal/analysis/report.go` | `report_id` 派生（域标签 `tellurion.analysis_report_id.v1` + 长度前缀哈希）、`hashInputs` |
| `internal/analysis/store.go` | `Store` 端口 + `Memory` 实现（单锁、出库深拷贝、`completed` 不可变、同键不同内容 `ErrReportConflict`） |
| `internal/analysis/pgstore.go` | **PG 实现**：`OpenPG` / `PG.Migrate`（单事务按序执行 `Migrations()`）/ `Store` 五方法；completed 同键同内容 no-op、异内容 `ErrReportConflict`；`CHAR(64)` 读取 `rtrim` |
| `internal/analysis/ddl.go` | 5 张表 DDL + `reports_completed_immutable` 触发器（`Migrations()` 返回副本） |
| `internal/analysis/export.go` | 确定性 html / csv 渲染（不读时钟、不读随机） |
| `internal/analysis/service.go` | 门面：读上游 → 纯函数 → 落 `analysis.*`；未接线端口 ⇒ `ErrDependencyUnavailable` |
| `internal/upstream/upstream.go` | **上游 HTTP 适配器**：`ResultReader`（core `/api/backtest/{id}/report` = **R1 持久读面**，`result_status` 是 `status` 权威）、`BenchmarkReader`（X1b resolve + snapshot）、`ArtifactReader`（holdings / 因子暴露，interim） |
| `internal/upstream/sensitivity.go` | **X5b 父作业适配器**：`POST /api/runs/parents`（batch，`base_run_spec={"run_id":…}` 锚点）+ `GET /api/runs/parents/{id}`（`summary.Tasks` 优先，回落 `children`） |
| `internal/httpapi/api.go` | 8 端点（Go 1.22 mux pattern）、严格 JSON 解码（拒未知字段 / 拒尾随 / 1 MiB 上限）、13 词错误码映射 |
| `cmd/analyticsservice/main.go` | 进程入口：`-dsn`（PG + 启动迁移）/ `-core-url` / `-data-url` / `-artifact-url` / `-orchestration-url`；未给 DSN 时 Memory（dev）；Async Executor；回环 `127.0.0.1:8087` |

**端到端可用性**（「已接」= 给了对应 URL 即通；未给仍 503）

| 端点 | 计算 | 上游 | 结论 |
|---|---|---|---|
| metrics | ✅ | `ResultReader` 已接（R1 持久读面） | 已接 |
| benchmark | ✅ | `ResultReader` + `BenchmarkReader`（X1b）已接 | 已接 |
| attribution (brinson) | ✅（临时形状） | `ResultReader` + `HoldingsProvider`（interim artifact）已接 | 已接 |
| attribution (factor) | ✅（临时形状） | `ResultReader` + `FactorExposureProvider`（interim artifact）已接 | 已接 |
| sensitivity | ✅ | `ResultReader` + `SensitivityQueue`（X5b）已接 | 已接（提交 + 逐点回填；执行侧 = core `cmd/worker`，2026-10-25） |
| montecarlo | ✅ | `ResultReader` 已接；**其余本仓自足** | 已接（PG 持久化） |
| correlation | ✅ | `ResultReader` 已接 | 已接 |
| GET report / export | ✅ | 无 | **可用**（需先有报告） |

**故意取舍（不是遗漏）**

1. **内部仓依赖只有 kit 一条**：`go.mod` 的直接依赖是 `pgx v5.11.0`（第三方驱动）+ `tellurion-kit v0.1.4`（鉴权机制，W3 接入、同日随 kit `KT-11` 升 pin）；**contracts 零依赖**——R1 / R2 的 DTO owner 已裁 = **本仓**（裁决 ③，2026-10-25），本地镜像**不是「等契约发布就迁走」的临时态**：触发迁移的条件写死为「core 或 orchestration 出现 import analytics DTO 的 Go 代码」（HTTP 交互不算），成文在 `api-schema.md` §6.4，守卫 `TestUpstreamContractOwnerStaysLocal`（本仓源码零 contracts import）钉住。
2. **PG 是生产 Store，Memory 只做测试 / 无库本地装配**：启动时给了 DSN 就连接 + 迁移；空 DSN 会打 WARNING。`completed` 不可变由应用层 + DB 触发器双保险（ADR-0002）。
3. **R1 已落 ⇒ `ResultReader` 读的是持久读面**（2026-10-25，core B6-3 `8537ffd`）：`status` 的唯一权威是 `backtest_jobs.result_status`，缺 `status` 只剩「上游没升到 R1」一种含义，照旧 fail-closed、**不解析错误串**；URL / DTO / 端口方法集都没动（§9.7 R1 原先的预期成立）。残留两条偏差如实登记：读面按**队列态**放行（pending / running / 引擎故障行 ⇒ 404），Migration 059 把迁移前的完成行回填成 `completed`。
4. **R2 的 DTO owner 已裁 = 本仓（2026-10-25，裁决 ③）**：`GET /runs/{id}/portfolio-snapshot` / `/factor-exposures` 是 **interim wire 形状**（上游 artifact 面尚未由 core 定稿），但**消费端 DTO 的 owner 不再是「待定」**——留在本仓，触发迁移的条件见 `api-schema.md` §6.4。端口方法集与校验语义在真迁移时不变。
5. **sensitivity 的 points 由父汇总驱动（2026-10-25）**：`base_run_spec` 只带 `run_id` 锚点，网格交给 X5b；`points` 只能从 NULL 一次写满，缺 point 就整份失败（不补、不猜）。执行侧 worker 在 core（`cmd/worker`）：它按锚点恢复完整 RunSpec、用 core 引擎执行、把 K9 结论上报编排侧；本仓仍不执行回测。
6. **鉴权已接线（2026-10-25）**：kit `auth/httpadapter` + 本仓策略表，全端点 401/403/503 fail-closed（ADR-0003）；**2026-10-26 加 `service` 服务间角色**（ADR-0004，本仓 8 条端点全开；签发与分发不在本仓）。
7. **`Migrations()` 只返回副本**，没有「按版本号执行 / 回滚」——与 core / risk 同一取向（`docs/db-schema.md` §4）。

**自检（2026-10-25，W8）**：`gofmt -l .` 无输出；`go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿。带真库 + 真进程：PG 16.15（WSL）上 `ANALYTICS_TEST_DSN` / `ORCHESTRATION_TEST_DSN` 同时设置，`TestSensitivityEndToEndWithOrchestrationProcess` / `TestEndToEndWithPostgres` / `TestPGSensitivityBackfill` 全 `PASS`。测试覆盖：三值 fail-closed、`seed` 规则（nil / 0 / 上界）、同 seed 字节一致、`report_id` 各键敏感性、`completed` 不可变（含触发器直连）、Brinson / 因子恒等式（`1e-8`）、相关性 30 点边界与对角 1.0、导出确定性、13 个 wire code 与 503、PG 迁移幂等与真库 E2E、sensitivity 网格顺序 / 缺 point fail-closed / points 一次写满。
