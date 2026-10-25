---
status: active
last-verified: 2026-10-26
verified-by: H6（R2 owner 落本仓 + 触发条件成文于 api-schema §6.4；service 服务间角色入策略表，ADR-0004）
---

# TASKS — tellurion-analytics 未完成项

> 本仓**唯一**任务台账。跨仓任务只在 §二 留指针，**不镜像正文**（双写必然漂移）。
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `✅ 完成（附日期）`
> 每张单六字段（① 目标 ② 上下文 ③ 要求 ④ 约束与非目标 ⑤ 验收 ⑥ 边界），判据见 `tellurion:AGENTS.md` §8.5。
> 完成即标 ✅（附日期）并移入 `docs/archive/TASKS-YYYY-MM-DD-archived.md`；本文件只留未完成项。

---

## 一、本仓任务

> **约定变更（2026-10-21）**：原「占位仓不写正式六字段任务单」作废——X 线任务单已展开为本仓正式任务（见下）。M5/M8 开工前置不变。

### QT-43 · X4 分析服务（基准 / 归因 / 敏感性 / 蒙特卡洛）[P1] 🔶

> **迁入说明（2026-10-21）**：本单自 core 仓 `tellurion:docs/TASKS.md`「五、X 线」迁入（任务台账批量重分布：X 线单展开到各服务仓为正式六字段单，「占位仓不写正式任务单」旧约定同时作废）。六字段正文逐字保留、不改写；core 侧同号条目已改为指针。
>
> **M8 前置完成（2026-10-22）**：接口对表见 [`ARCHITECTURE.md`](ARCHITECTURE.md) §9。开工前先读 §9.1 的六条前置事实——结果读取面无 K9-correct 持久读路径（F1）、Brinson / 因子归因缺 holdings / 暴露输入（F2）、sensitivity 依赖 QT-44（F3）、基准面依赖 X1b（F4）、指标不得重算（F5）、`seed=0` 不等于 time-based（F6）。另：② 里「benchmark/attribution 仅一行注释」经实测订正——设计引的那行其实是 cache warming 注释，BE-05/BE-06 生产代码 **0 命中**（§9.1「零实现核对」）。~~本单仍 ⬜：对表是开工前置，不是实现；R1 / R2 / R3 未落地前 X4a / X4b / X4c 各自阻塞。~~（2026-10-23 订正：代码可按消费端端口先行落盘，R1/R2/R3 只决定「谁提供 reader」，不阻塞本仓——见下「施工记录」与 `ARCHITECTURE.md` §9.8。）
> 对表自检（2026-10-22，基线 `dbe4c68`）：35 条 core `path:line` 按 HEAD 逐条命中；C 组 8 端点 / 13 错误码与 `api-schema.md` 双向一致；破坏验证三腿（改错行号 / 删一行端点 / 删一个错误码）各自红，还原后绿。

- **① 目标**：补齐 BE-05/BE-06 的真实缺口，且分析只读 run 结果、零 import 引擎。
- **② 上下文**：`analytics-service-design.md`（D21）；回测族 grep benchmark/attribution 仅一行注释；MC/bootstrap 全无；`mc_runs.seed` 必填。
- **③ 要求**：① 搬现有 metrics/compare/correlation/export；② 新增基准对比 + Brinson/因子归因；③ 参数敏感性（网格）+ 蒙特卡洛/bootstrap（seed 留痕）；④ 只按 `run_id` 拉结果；⑤ `analysis.*` schema 唯一 owner。
- **④ 约束与非目标**：纯函数 + 只读；不 import `pkg/backtest`；不在分析层做交易决策。
- **⑤ 验收**：同一 run + 同一 seed 两次分析结果一致；归因分项和 ≈ 总收益（容差写明）；缺 seed 直接拒绝。
- **⑥ 边界**：只做分析与指标；报表呈现归 UI 仓。

#### 施工记录（2026-10-23，施工 ①：X4a~X4d 代码落盘）

**已落**

- 计算面：metrics / benchmark / attribution / montecarlo / correlation 五类纯函数 + 导出，口径与 core 同式；Brinson-Fachler 恒等式与因子归因 residual 用单测钉在 `1e-8`。
- 报告身份：`report_id = hash(run_id, analysis_type, params_hash, input_hash, seed, metrics_version)`，域标签 `tellurion.analysis_report_id.v1`；同键重放字节一致。
- 端口：`ResultReader` / `BenchmarkReader` / `HoldingsProvider` / `FactorExposureProvider` / `SensitivityQueue`（消费端定义，不 import 上游）。
- Store：`Memory`（单锁 + 出库深拷贝 + `completed` 不可变）；DDL 5 表落 `internal/analysis/ddl.go`（`Migrations()` 副本）。
- HTTP：8 端点 + 严格解码 + 13 词错误码（含 503 `dependency_unavailable`）；`cmd/analyticsservice` 回环进程入口。
- 测试：`internal/analysis/*_test.go` 与 `internal/httpapi/api_test.go`，覆盖三值、seed 规则、幂等、恒等式、30 点边界、导出确定性、wire code。

**未落（后续施工项；2026-10-24 施工 ② 的结清情况见下一条记录）**

1. **PG Store + 启动迁移执行**：`ddl.go` 已定稿，缺连接装配与启动执行（`Memory` 暂替）。
2. **上游读取端口接线**：`ResultReader`（依赖 R1 的 K9-correct 读取面）、`BenchmarkReader`（R4 / X1b 或 interim adapter）、`HoldingsProvider` / `FactorExposureProvider`（R2 artifact 形状冻结后迁 contracts）。
3. **sensitivity 逐点回填**：`SensitivityQueue` 只提交；`analysis.sensitivity_runs.points` 与完成态推进依赖 QT-44（R3）。
4. **鉴权**：QT-50 ④ 统一面（R6）；当前进程仅回环监听。
5. **PDF 导出**：**R5 已裁（2026-10-24）= 非目标**，只承诺 html / csv。
6. **契约迁移**：本地 DTO 镜像待 `tellurion-contracts` 发布 `analysis` 包后迁走。

**验收现状**：⑤ 的三条（同 run + 同 seed 两次结果一致、归因分项和 ≈ 总收益、缺 seed 拒绝）已在单测层面成立；**端到端**验收（真实 run + 真实基准 + 真实 DB）待上述 1/2 接线后才可执行，故本单保持 🔶。

#### 施工记录（2026-10-24，施工 ②：PG 与上游读取端口接线）

**已落**

1. **PG Store + 启动迁移**
   - `internal/analysis/pgstore.go`：`OpenPG` / `PG.Migrate`（单事务按序执行 `Migrations()`，可重入）/ `Store` 五方法；语义与 Memory 对齐（completed 同键同内容 = no-op，异内容 = `ErrReportConflict`；pending/running 可推进）。
   - `completed` 不可变：应用层先判断 + **DB 触发器** `reports_completed_immutable` 兜底（绕过程序直接 `UPDATE` 也被拒，SQLSTATE 55000）。理由与取舍见 `adr/0002-报告不可变由PG触发器兜底.md`。
   - `cmd/analyticsservice`：`-dsn` / `ANALYTICS_DATABASE_DSN` 有值 → 连接 + 启动迁移 + PG Store；空值 → Memory（仅本地/测试，启动日志打 WARNING）。`CHAR(64)` 列读取用 `rtrim`，避免短值被空格污染。
2. **上游读取端口**（`internal/upstream`；端口定义不变，只加适配器）
   - `ResultReader`：`-core-url` / `ANALYTICS_CORE_URL` → `GET /api/backtest/{id}/report`。K9 三值原样映射；**缺 `status` 的异步结果一律 409 `run_result_incomplete`**，不解析错误串。R1 的专用 K9-correct 持久读面尚未落地，故这是 interim 适配器（`ARCHITECTURE.md` §9.7 R1）。（**2026-10-25 更新**：R1 已由 core B6-3 落地，本仓已换面——本行是当时的状态快照，见「R1 换面」记录）
   - `BenchmarkReader`：`-data-url` / `ANALYTICS_DATA_URL` → X1b `GET /datasets/resolve` + `GET /datasets/{hash}`；`adjustment` 按登记口径传入，回包 basis 不一致即 `benchmark_data_unavailable`（不用价格指数冒充全收益）。
   - `HoldingsProvider` / `FactorExposureProvider`：`-artifact-url` / `ANALYTICS_ARTIFACT_URL` → interim artifact 面 `GET /runs/{id}/portfolio-snapshot`、`GET /runs/{id}/factor-exposures`；404 / 空体 → `run_result_incomplete` 并点名缺哪个输入。R2 的 owner / 形状未裁，DTO 仍是本地临时镜像，冻结后迁 contracts。
   - 未配置 URL 的端口保持 nil，对应端点仍 503 —— fail-closed 语义未变。
3. **真库端到端**
   - 命令：`ANALYTICS_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_analytics_test?sslmode=disable' go test ./... -count=1`（PG 17.5）。
   - 覆盖：同 run 两次 metrics 字节一致；同 run + 同 seed 两次 montecarlo 同一 `report_id` 且报告字节一致；缺 seed → 400 `seed_required`；因子归因 `Σcontributions + residual == total_return`（容差 `1e-8`）；Brinson `Σ分项 == total_return - benchmark_return`（同容差）；基准走 X1b；报告落 PG 后由**另一条连接**读回（排除进程内缓存）。
   - 另：PG 集成用例覆盖迁移幂等、completed 不可变（含触发器直连验证）、seed 的 NULL vs 0、benchmark 最新登记、归因 / MC 的同键冲突。
   - **破坏验证**：把 `ResultReader` 的缺 `status` 分支改成「当作 completed」→ `TestResultReader_K9Mapping/missing_status_fails_closed` 红（`want IncompleteError, got <nil>`）；用备份逐字节还原（文件 SHA-256 改前/还原后同为 `8EE5D88FED09EE50D66061E8FB1ACF9CD7A238937DCBD2B691EE255A30527FD3`）后同一用例绿。

**未落（如实登记，不算完成）**

1. **sensitivity 逐点回填**：`SensitivityQueue` 仍只提交；`analysis.sensitivity_runs.points` 与完成态推进依赖 G7 的 X5b（`tellurion-orchestration` QT-44：等父端点/W6）。按派发要求「不到就停」，本次不预设载荷形状。（2026-10-25 W8 已结清，见下方施工记录）
2. **R1 的 K9-correct 持久读面**：core 侧尚未落地；当前适配器对同步结果可用、对无 `status` 的异步结果 fail-closed。R1 落地后只换 URL/路径，端口与 DTO 不变。（**2026-10-25 R1 换面已结清**，见下方「R1 换面」记录：实际连 URL 都没换。）
3. **R2 artifact 形状与 owner**：wire 形状仍是本地临时镜像，未迁 contracts。
4. **PDF 导出**：**R5 已裁（2026-10-24）= 非目标**，只承诺 html / csv。
5. ~~**鉴权（裁-15，2026-10-25 已定，可开工）**~~ **✅ 已落（2026-10-25，W3）**：QT-50 ④ 的服务侧腿。口径：kit `v0.1.1` 的 net/http 适配（非 gin 中间件）；policy 表：6 个 `POST /api/analysis/*`（计算并落报告）= trader，`GET /api/analysis/reports/{id}` 与 `/export` = viewer；无 secret + 回环 = 开放，无 secret + 非回环 = **拒绝启动**。`AuthStatus = "enforced"`，原 `TestAuthNotWiredCurrentState` 已翻转成 401/403 断言。
   - **出站服务间凭证（裁-17 定稿，2026-10-25）**：三个 upstream reader 已支持 `Bearer`，装配项 `-core-token` / `-data-token` / `-artifact-token`（env `ANALYTICS_CORE_TOKEN` / `DATA_SERVICE_TOKEN` / `ANALYTICS_ARTIFACT_TOKEN`；artifact 缺省复用 core token）。**空值不发 Authorization**（回环 + 无 secret 的开发态仍免密）。上游用 **`service` 角色**长期 token（**不是** `trader`）；本服务作为被调用方暂无 `service` 需求（worker 未落）。
6. **契约迁移**：`tellurion-contracts` 的 `analysis` 包未发布，本地 DTO 镜像未迁走。

**验收现状**：⑤ 三条判据已在**真库 E2E** 层成立（不再是仅单测）；本单保持 🔶 —— sensitivity 逐点回填、R1/R2 契约化、鉴权仍未落（PDF 已裁为非目标），不写成已完成。（其中 sensitivity 逐点回填已由 2026-10-25 W8 结清）

#### 施工记录（2026-10-25，施工 ③：QT-43 收口——R1/R2 契约化 + 鉴权现状登记）

> 依据：R5（PDF）已裁为非目标（2026-10-24），本次只结剩余两项，不碰渲染器。

**已落**

1. **R1/R2 契约化（本地镜像 v1）**
   - `internal/analysis/upstream_contract.go`：`UpstreamContractVersion = "v1"` + 三个校验器 —— `validateResultContract`（R1：`status` 必须是 K9 三值，缺失 / 未知即 `409`，**不解析错误串**）、`validatePortfolioSnapshot`（R2 Brinson）、`validateFactorExposures`（R2 因子）。
   - 接进调用链：`requireResult`（所有读结果的端点）与 `Service.Attribution`。**校验点前移到算 `input_hash` 之前** —— 缺 `dataset_hash` 时同键会复用旧报告，只在计算函数里拦是不够的。
   - 新增必填项（旧实现只查 `holdings` / `factors` 非空）：`holdings.dataset_hash`、`factor_exposures.dataset_hash`、`benchmark_sector_weights`、`benchmark_sector_returns`。理由：前两者进 `input_hash`（缺了假复用）；后两者缺失会被静默按 `0` 算出一份「看起来正常」的归因。
   - 形状钉死：`TestUpstreamContractWireShape` 用 golden JSON 往返比对三个 DTO；`TestUpstreamContractArtifactFailClosed` 逐项钉必填；`TestServiceAttributionRequiresArtifactDatasetHash` 钉「拒算且不落库」。
2. **`run_result_incomplete` 机器可读**
   - `errorBody.Missing`（`omitempty`）：只有 `run_result_incomplete` 带，原样透出 `IncompleteError.Missing`；其余错误码不带。
   - `IncompleteError` 拆成 `Missing`（稳定标识，进 wire）+ `Detail`（人读定位，**不进 wire**）。原先混在 `Missing` 里的人读后缀（`"equity_curve of r2 (need >= 3 points)"`、`"K9 status (...)"`）已拆开，`missing` 现在是**闭集**（`api-schema.md` §6.3，12 个取值）。
   - 用例：`TestIncompleteErrorCarriesMissingField`（400/409 侧断言 `missing=portfolio_values`）。
3. **鉴权现状登记**
   - `internal/httpapi/auth_status.go`：`AuthStatus = "unwired"`；`TestAuthNotWiredCurrentState` 是**可执行锚点** —— 无凭证与伪造 `Bearer` 均断言 `200`，接线鉴权时该用例转红，届时改写而非删除。
   - `api-schema.md` 新增 §7（现状 / 安全边界 / 接线清单），并把 §2 的 401 / 403 两行显式标成「目标态，当前未实现」—— 不让人把目标当现状。
4. **契约文档**
   - `api-schema.md` §6（新增）：R1 结果面 / R2 artifact 面的字段表、必填项、版本策略（加字段 = MINOR，改名 / 删字段 = breaking）、fail-closed 规则；§6.3 是 `missing` 的唯一词表。
   - `api-schema.md` §1 / §4 与 `ARCHITECTURE.md` §8 / §9.7 R5：PDF 记为**已裁非目标**，三处口径一致。

**破坏验证（本次）**

- 改坏：`PortfolioSnapshot.DatasetHash` 的 wire tag 由 `dataset_hash` 改成 `dataset_hash_v2`（`internal/analysis/model.go`）。
- 应红：`go test ./internal/analysis -run TestUpstreamContract -count=1` → `FAIL's TestUpstreamContractWireShape/R2_portfolio_snapshot`，diff 显示重编码后多出 `"dataset_hash_v2":""`、少了 `"dataset_hash":"h1"`。
- 还原：备份逐字节拷回；`model.go` SHA-256 改前 / 还原后同为 `C5C63B682CF57529B5F55162CB9272A0CA9B63A08FC724492E91CDCCB5AAD0DA`。
- 应绿：同一命令 → `ok tellurion-analytics/internal/analysis`。

**未落（如实登记，不算完成；本单仍 🔶）**

1. **R1 的 K9-correct 持久读面**：核心在 core 侧（`ARCHITECTURE.md` §9.7 R1），本仓仍是 interim 适配器（读 core 现有 report 端点）。本次只是把**消费端形状与失败语义**冻结成 v1，不等于 R1 落地。（R1 已于 2026-10-25 结清，见下方「R1 换面」记录）
2. **R2 artifact 的 owner 与最终形状**：本次冻结的是「本仓读它」的契约版本。
   > **✅ owner 已裁（2026-10-25，主任口径）**：**owner = 本仓（analytics），不迁 contracts**；DTO 继续留在 `internal/analysis/model.go`，触发迁移的条件写死为「**core 或 orchestration 出现 import analytics DTO 的 Go 代码**」（HTTP 交互不算）。裁决全文见 `tellurion:docs/design/qt-decoupling/cross-repo-decision-record-2026-10-25.md` ③。
   > **依据（实测）**：contracts `docs/adr/0001-契约只增不破且零实现.md` 决策 ① 原文「**本仓只放被 ≥2 个仓使用的类型 / 接口 / 错误 / 常量**」；实测 core 对 `tellurion-analytics` 的 import = **0 处**（core 内 26 处 `PortfolioSnapshot` / `FactorExposure` 命中**全是 core 自己的私有符号**，core 内 `AnalysisReport` 零命中）；orchestration 与本仓的交互**只经 HTTP**（`internal/upstream/sensitivity.go` 走 `POST /api/runs/parents`）⇒ **没有第二个使用方**；contracts 仓当前包清单实测只有 `dataprotocol` / `domain` / `engine` / `fees`，**无 `analysis` 包**。
   > **口径不变**：`internal/analysis/upstream_contract.go` 的 `UpstreamContractVersion = "v1"` 与三个校验器（`validateResultContract` / `validatePortfolioSnapshot` / `validateFactorExposures`）语义**不动**；将来真迁时**端口方法集与校验语义不变**。
   > **✅ 已成文 + 有守卫（2026-10-26 H6）**：触发条件写进 `api-schema.md` **§6.4**；守卫 `TestUpstreamContractOwnerStaysLocal`（本仓零 contracts import）+ `TestUpstreamContractMigrationTriggerDocumented`（§6.4 在位且含两个要素）。
3. **sensitivity 逐点回填**：`SensitivityQueue` 仍只提交，`analysis.sensitivity_runs.points` 与完成态推进依赖 QT-44（等父端点/W6）。（2026-10-25 W8 已结清）
4. **鉴权**：本次只做**现状登记**（常量 + 可执行锚点 + 文档），**未接线任何鉴权中间件**，不产出 401 / 403；安全边界仍是回环监听。
5. **PDF**：非目标，不做。

**验收现状**：`go build ./...` / `go vet ./...` / `go test ./...` 全绿（输出见提交说明）；⑤ 三条判据维持真库 E2E 层成立。本单保持 🔶，原因已从「R1/R2 契约化 + 鉴权」收窄为「R1 持久读面、R2 owner / contracts 迁移、sensitivity 回填、鉴权接线」。

#### 施工记录（2026-10-25，W3：鉴权接线）

**已落**

1. **机制接 kit**：`internal/httpapi/auth.go` 的 `Authorizer` 由 `NewAuthorizer(*auth.TokenService)` 实现，内部走 kit `v0.1.1` 的 `auth/httpadapter`（Bearer → `Parse` → 拒 `kind!=access` → 校验 subject；角色 any-of）；`requireAuth` 三态：未装配 503、无凭证/坏 token 401、越权 403。
2. **策略表**：`internal/httpapi/policy.go`——6 个 POST = trader，2 个 GET = viewer；路由注册与策略表同源（`routes()`），`New` 逐个包 `requireAuth`。
3. **现状登记翻转**：`AuthStatus = "enforced"`（原 `unwired`）；`TestAuthNotWiredCurrentState` 改写为 `TestAuthEnforcedCurrentState`（无凭证/坏 token 401、6 个 POST viewer 403、trader 可执行、viewer 可读），**不是删除**。
4. **守卫**：`TestRouteTableEqualsPolicyTable` 双向对拍 8 条路由 ≡ 8 条策略。
5. **启动闸**：`cmd/analyticsservice` 新增 `-jwt-secret` / `JWT_SECRET` 与 `-insecure-exposure` / `AUTH_INSECURE_EXPOSURE`；`DecideStartup`——有 secret 开鉴权，无 secret + 回环 = 开放，无 secret + 非回环 = 拒绝启动。
6. **依赖登记**：`github.com/ruoxizhnya/tellurion-kit v0.1.1`。
7. **文档同笔**：`api-schema.md` §2/§7/头部、`ARCHITECTURE.md` §9.4/§9.7 R6/§9.8、ADR-0003 + 索引。

**破坏验证**

删掉 `internal/httpapi/policy.go` 的 `GET /api/analysis/reports/{id}/export` 条目 ⇒ `TestRouteTableEqualsPolicyTable` 红（route 8 vs policy 7）、`TestAuthEnforcedCurrentState` 红（该路由无凭证期望 401 实得 403）；逐字节还原（`sha256 21E9FEF1…`）后两条复跑绿。

**验收**：`go build ./... && go vet ./... && go test ./... -count=1` 全绿。

**未落（本单仍 🔶）**

1. ~~**R1 的 K9-correct 持久读面**：核心在 core 侧，本仓仍是 interim 适配器（见上一条施工记录）。~~（**2026-10-25 已结清**：core B6-3 落了持久读面，本仓已换面，见下方「R1 换面」记录）
2. **R2 artifact 的 owner 与最终形状**：owner 未定、未迁 `tellurion-contracts`。
3. **sensitivity 逐点回填**：依赖 QT-44（等父端点/W6）。（2026-10-25 W8 已结清）
4. **token 分发（跨仓）**：鉴权开启后 core / UI 调用本服务必须持 token（POST trader / GET viewer）；分发不在本仓。
5. **PDF**：非目标，不做。

#### 施工记录（2026-10-25，W8：sensitivity 逐点回填，X5b 父作业）

**已落**

1. **端口加轮询**：`analysis.SensitivityQueue` 从「只 Submit」扩成 `Submit + Status`；新增 `SensitivityChild` / `SensitivityProgress`（顺序 = 计划顺序）。`Status` 语义：父作业不存在 → `ErrSensitivityParentNotFound`（终态失败）；网络 / 非 2xx → `ErrDependencyUnavailable`（保持 pending，可重试）。
2. **HTTP 适配器**：`internal/upstream/sensitivity.go` —— `POST /api/runs/parents`（`kind=batch`，`base_run_spec={"run_id":…}` 作为 interim 锚点，`grid` 为 name→values）；`GET /api/runs/parents/{id}` 读 `counts` + `summary.Tasks`（无 summary 时回落 `children`）。幂等键 `analysis-sensitivity:<report_id>`，父身份仍由 orchestration 的 `plan_hash` 兜底。
3. **逐点回填**：`internal/analysis/sensitivity.go`
   - 提交后落 `analysis.sensitivity_runs`（`grid` + `parent_job_id`，`points` 为 NULL）；报告仍 `pending`。
   - **读路径推进**：`GET /reports/{id}` 对 pending/running 的 sensitivity 报告查一次父作业；父作业 `completed`/`failed` 时回填。无后台 goroutine，进程重启不丢进度、重复读取幂等。
   - 点顺序用本地笛卡尔展开（参数名字典序、最后一维最快，与 `plan.ExpandBatch` 同规则），再与父汇总顺序对齐。
4. **fail-closed（缺 point 不补齐、不猜形状）**
   - 父 `total` ≠ 本地展开点数、或返回点数 < 计划 ⇒ 报告 `failed`，**不写 points**（错误点名「refusing to synthesize」）。
   - 某个点没有 K9 `result_status` ⇒ 失败并点名该点，不写 points。
   - 任一点 `invalid` / `failed` / 结果取不到 ⇒ 写满逐点结论（含逐点 error）但报告 `failed`，不出「看起来完整」的网格。
   - 结果读取端口未接线 ⇒ `503 dependency_unavailable`（不把部署问题写成业务失败）。
5. **DDL / Store**：`analysis.sensitivity_runs` 新增 `parent_job_id TEXT NOT NULL DEFAULT ''`（`ALTER … ADD COLUMN IF NOT EXISTS`，只前滚）；`Store` 加 `PutSensitivity` / `GetSensitivity`，Memory 与 PG 同语义（points 只能从 NULL 写满一次，之后不可变）。
6. **HTTP**：`POST /api/analysis/sensitivity` 的 202 增加可选 `parent_job_id`（montecarlo 不带）；网格展开上限 100 点，超限 400 `invalid_params`（与 orchestration 的 `MaxBatchTasks` 同值）。
7. **装配**：`cmd/analyticsservice` 新增 `-orchestration-url` / `ANALYTICS_ORCHESTRATION_URL`；未配置仍 503。

**E2E（真进程 + 真库）**

- `TestSensitivityEndToEndWithOrchestrationProcess`：`go build` 真 orchestration 进程 + PG 16（WSL）→ 提交 → 父作业 pending → 报告 pending → 执行侧写回汇总（**W8 当时 worker 尚未落地，用 SQL 写 `run.parent_jobs`**；**2026-10-25 H5 已替换**，见下方 H5 记录）→ 报告 completed、逐点回填、参数顺序正确。**PASS**。
- `TestSensitivityEndToEndWithFakeParent`：恒常运行的契约级 E2E（严格按 W6 wire）。
- `TestEndToEndWithPostgres` / `TestPGSensitivityBackfill`：真库下落库与回填不可变。**PASS**。

**破坏验证**

- 改坏：`expandGrid` 的参数排序由字典序改成逆序（`sort.Reverse`）。
- 应红：`TestExpandGridOrderAndCap`（`point 1 = a=2,b=10, want a=1,b=20`）与 `TestServiceSensitivityBackfillsPoints`（`point 1 params = fee=0.02,lookback=10`）。
- 还原：备份逐字节拷回，`sensitivity.go` SHA-256 改前 / 还原后同为 `3D4874C6D3F9AA68E41675862EC10A5E5CC60785C3799360A21CB465FF51F711`。
- 应绿：同两条用例复跑 `ok`。

**门禁（真实输出）**

- Windows（无 DSN，PG / 真进程腿按设计 skip）：`go build ./...` / `go vet ./...` exit 0；`go test ./... -count=1` → `ok internal/analysis 0.064s`、`ok internal/httpapi 0.079s`、`ok internal/upstream 0.064s`。
- WSL（Go 1.25.0 + PG 16.15，`ANALYTICS_TEST_DSN` / `ORCHESTRATION_TEST_DSN` 均设置）：同一组命令全绿；`-run 'TestSensitivityEndToEndWithOrchestrationProcess|TestPGSensitivityBackfill|TestEndToEndWithPostgres' -v` → 三条全 `PASS`（真 orchestration 进程 0.74s）。
- 说明：本机 Windows 进程对 WSL 转发端口的连接受限（PowerShell 可连、Go 被拒），故真库腿在 WSL 内执行；不是代码问题。

**未落（如实）**

1. ~~**执行侧（worker）仍未落地**~~ → **✅ 2026-10-25（H5）**：core 侧 worker 装配已落（`tellurion:cmd/worker` + `tellurion:internal/orchestrationworker`），子 run 上报终态即由编排服务调 `parents.SyncSummary` 真实写回父汇总（`ApplyBatchSummary` 的同源实现）。本仓的真进程 E2E 已改为走 **worker HTTP 协议**（注册 → 领取 → 上报），**不再用 SQL 模拟**；本仓仍然只读、不执行回测。
2. **`base_run_spec` 只有 `run_id`**：完整 RunSpec 的装配属执行侧（R3 未裁），当前锚点只保证计划身份可算、网格可展开。
3. **失败报告的逐点 error 只在 `error` 文本 + `analysis.sensitivity_runs.points`**：报告体仍遵守「completed 才有 body」的 DDL 约束，API 不单独暴露 failed 报告的 points。
4. ~~R1 持久读面~~（**2026-10-25 R1 换面已结清**，见下方记录）、~~R2 owner / contracts 迁移~~（**2026-10-26 H6 已结清**：owner 裁 = 本仓，触发条件成文于 `api-schema.md` §6.4）、token 分发仍按上一条记录未落（**本仓腿已落，跨仓腿未落**，见 H6 记录）。

#### 施工记录（2026-10-25，H5：真进程 E2E 改走 worker HTTP 协议，去掉 SQL 模拟）

**已落**

1. **去模拟**：删除 `sensitivity_e2e_test.go` 的 `seedParentSummary` —— 不再直接用 SQL 改 `run.parent_jobs`。父汇总的推进改为真实链路：本仓注册 worker → 领取子 run → `POST /api/runs/jobs/{id}/result` 上报 `completed` → 编排服务内部调 `parents.SyncSummary` 写回父汇总。
2. **仍然只读**：本仓没有执行回测，`result_uri` 用 `test://<run_id>` 占位；真实执行在 core 的 `cmd/worker`（`tellurion` 仓），本仓不 import 它。
3. **共享队列残留**：worker 把领到的每个子 run 都上报完，只收集属于本次父作业的——上一次失败运行遗留的 pending 子 run 会被后续运行顺手清掉，不再造成假红（清理后**连续两次**运行均 PASS）。

**门禁（真实输出，WSL Go 1.25.0 + PG 16；`ANALYTICS_TEST_DSN` / `ORCHESTRATION_TEST_DSN` / `ORCHESTRATION_REPO` 均设置）**

- `gofmt -l .` 空；`go build ./...` / `go vet ./...` exit 0；`go test ./... -count=1` 全绿：`internal/analysis 0.470s`、`internal/httpapi 2.837s`、`internal/upstream 0.160s`。
- `go test ./internal/httpapi -run 'TestSensitivityEndToEndWith' -v` → `TestSensitivityEndToEndWithFakeParent PASS (0.01s)`、`TestSensitivityEndToEndWithOrchestrationProcess PASS (2.43s)`——**真进程腿实跑，不是 skip**。

**破坏验证（一条腿，改坏 → 红 → 逐字节还原 → 绿）**

- 改坏：`settleParentThroughWorker` 的领取循环在第一次上报后直接 `return`（只报一个子 run）。
- 应红：`TestSensitivityEndToEndWithOrchestrationProcess`（父记录停在 `running` / 逐点回填不足 2 点）。
- 还原：`sensitivity_e2e_test.go` 的 sha256 改前 / 还原后同为 `21e1e67031b459f2a4896b37a0459bdfcebb92feeb445950af8f97ac24bc5930`（逐字节相同），复跑绿。

**未落（如实）**

- 本仓仍不执行回测；E2E 的 worker 用 `test://` 结果 URI。真实引擎 + data-service 的端到端在 core 侧仍是替身引擎（见 `tellurion:docs/TASKS.md` QT-44 记录）。

#### 施工记录（2026-10-25，R1 换面：`ResultReader` 接 core 持久读面）

> **依据（先证伪再动手）**：core B6-3 `8537ffd`（`feat(core): backtest_jobs.result_status 三值持久读面`）实测 `git merge-base --is-ancestor 8537ffd main` = 真 ⇒ R1 前置已解除。R2 仍 ⛔：`go list -m -versions` 实测远端最新 tag 是 `v0.6.0`，且 `v0.6.0` 与 `origin/main` 的业务包只有 `dataprotocol` / `domain` / `engine` / `fees`，**没有 `analysis/`**。

**已落**

1. **消费端换面，但 URL / DTO / 端口方法集零改动**：core 把 K9 结论持久化成 `backtest_jobs.result_status`，`GET /api/backtest/{id}/report` 以该列为 `status` 的唯一权威（回落顺序：列 → 结果体 → `completed`），`completed/invalid` 行返 **200** 不再 404。§9.7 R1 原裁「落地后只换适配器的 URL/路径」——今天验证成立，且实际连路径都没换（core 把权威做进了同一个端点）。
2. **语义改判**：`internal/upstream.ResultReader` 不再是 interim。缺 `status` 从「core 异步结果的旧编码」改判成「上游没升级到 B6-3」，仍然 `409 run_result_incomplete`（`missing=status` 在 §6.3 闭集里不变）、**不解析错误串**这条没动；`IncompleteError.Detail` 与 `validateResultContract` 的文案同批去掉「R1 pending」。
3. **端口注释订正（实现与注释对不上）**：`internal/analysis/ports.go` 原写「pending/running 归 `ErrRunIncomplete`」，实测走的是 `ErrRunNotFound`——core 的端点要求队列态 `status=="completed"`，其余一律 404。按实测改口径，不改行为。
4. **对表用例**：`internal/upstream.TestResultReader_PersistentK9ReadPlane` 按 core 的 overlay 建模（handler 的出处点名到 `core:cmd/analysis/handlers_backtest.go`），6 条子用例钉两轴：异步有效（列 `completed` + 旧结果体无 status）→ 可读；异步 invalid（`completed/invalid` + reasons）→ `InvalidReasonsError`；invalid 但没给 reasons → 裸 `ErrRunInvalid`（不回落 completed）；列与体都无 status → core 回落 `completed`；引擎故障（`failed/failed`）→ 404 → `run_not_found`；还在跑（`running` + NULL）→ 404 → `run_not_found`。
5. **文档同笔**：`api-schema.md`（前置现状 / §6.1 `status` 权威 + 两条偏差 / §6 迁 contracts 的实测前置）、`dataflow.md`（读取边表 + 失败模式表新增「还在跑 / 引擎故障被读成 `run_not_found`」一行）、`ARCHITECTURE.md`（§8 X4a、§9.1 F1 加订正、§9.2 A 组 `ResultReader` 行、§9.7 R1 改 ✅、§9.8 三处 interim 口径与取舍 3）、`AGENTS.md` §1 状态。

**破坏验证（两条腿，实测退出码）**

- 腿 A：删掉 `GetResult` 的 `case analysis.ResultInvalid` 分支 ⇒ `TestResultReader_K9Mapping/invalid_with_reasons`、`TestResultReader_PersistentK9ReadPlane/async_invalid…`、`/invalid_without_reasons…` 三条红（`want InvalidReasonsError[data_gap_skipped], got run result incomplete: missing status`），`LEG_A_EXIT=1`；`cp` 逐字节还原后 `upstream.go` SHA-256 与改前一致（`2462b46afbb6a8c025704ad3e34381d7b25297865e3cc8f6cdb6615a0996748e`），复跑 `ok`（`LEG_A_RESTORED_EXIT=0`）。
- 腿 B：删掉 404 → `ErrRunNotFound` 那条分支 ⇒ `not_found`、`engine_failure…`、`still_running…` 三条红（`want run not found, got dependency unavailable`），`LEG_B_EXIT=1`；同法还原，SHA-256 一致后复跑 `ok`（`LEG_B_RESTORED_EXIT=0`）。

**门禁（真实输出，本机 Windows / Go 1.25.0，无 DSN ⇒ PG 与真进程腿按设计 skip）**

```
gofmt -l .                      → 无输出（GOFMT_EXIT=0）
go build ./...                  → BUILD_EXIT=0
go vet ./...                    → VET_EXIT=0
go test ./... -count=1          → TEST_EXIT=0
  ok  tellurion-analytics/internal/analysis   0.061s
  ok  tellurion-analytics/internal/httpapi    0.084s
  ok  tellurion-analytics/internal/upstream   0.074s
```

**未落（如实登记，本单仍 🔶）**

1. **两条 core 侧残留偏差**（本仓不猜、也不二次调用去补，见 `api-schema.md` §6.1）：① 持久读面按**队列态**放行 ⇒ pending / running 与引擎故障行读成 `404 run_not_found`，K9 的 `failed` 经持久面到不了 `409 run_result_failed`（今天只有 core 内存态路径给得出）；② Migration 059 把迁移前的完成行回填成 `completed`，旧代码写下的 invalid（当时落成 `status=failed`）读不回 invalid。要修先裁「读面按队列态还是结论态放行」，改在 core。
2. **本轮没有真 core 进程 + 真 PG 的 invalid 行端到端**：新用例是契约级对表（httptest 按 core overlay 建模）。代价：core 侧若改了 overlay，本仓这条只能靠「出处注释」提醒同步，不会被真链路自动逮到。
3. **R2 契约迁移仍 ⛔**：`tellurion-contracts` 的 `analysis` 包未发布（实测见上），`PortfolioSnapshot` / `FactorExposures` / `BacktestResponse` 的本地镜像 v1 继续留本仓。
4. token 分发（跨仓）仍不在本仓。

**验收现状**：QT-43 的 ⑤ 三条判据维持**真库 E2E** 层成立（W8 记录）；本单保持 🔶，未落项收窄为「R2 owner / contracts 迁移、token 分发、R1 的两条 core 侧残留偏差、真 core 进程的 invalid 行端到端」。

> **⚠️ 本段已被下方 H6 记录取代（2026-10-26）**：R2 owner 已落本仓（`api-schema.md` §6.4），token 的**本仓腿**已落（`-core-token` / `-data-token`，`ca95f1e`），剩「token 的跨仓签发/注入」；未落项以 H6 记录的收窄版为准。

#### 施工记录（2026-10-26，H6：R2 契约化落在本仓 + token 分发的本仓腿）

> **依据（先证伪再动手）**：派发原文的 R2 腿要求「按裁决 ③ 留在本仓，不迁 contracts」，且**三个校验器语义不动**。实测确认现状与裁决一致：① `internal/analysis/upstream_contract.go` 的 `UpstreamContractVersion = "v1"` 与三个校验器（`validateResultContract` / `validatePortfolioSnapshot` / `validateFactorExposures`）已在本仓落地；② core 对 `tellurion-analytics` 的 Go import 实测 **0 处**（core 内 `PortfolioSnapshot` 26 处命中全是 core 自己的私有符号 `pkg/alert/manager.go:76` 等，`FactorExposure` / `AnalysisReport` 零命中）；③ orchestration 与本仓**只经 HTTP** 交互（`internal/upstream/sensitivity.go` 走 `POST /api/runs/parents`）；④ contracts 仓当前包清单实测只有 `dataprotocol` / `domain` / `engine` / `fees`，**无 `analysis` 包**（远端最新 tag `v0.6.0` 与 `origin/main` 同）⇒ **没有第二个 Go 使用方**，触发条件未命中。

**已落**

1. **R2 契约化：owner 成文（§6.4）**：`docs/api-schema.md` 新增 **§6.4「DTO 的 owner 与「第二个使用方」的触发条件」**——owner = 本仓、不迁 contracts；触发条件写死为「**core 或 orchestration 出现 import analytics DTO 的 Go 代码**」，**HTTP 交互不算**；真触发时按 contracts ADR-0001 ① 迁，**端口方法集与校验语义不变**。同时订正 §6 抬头那句已过期的「仍未迁 contracts（该仓 analysis 包未发布）」——它把「owner 未定」写成了「等发布」，与裁决 ③ 的「不迁」口径不同（§6.4 承接）。
2. **R2 两条可执行判据**（`internal/analysis/upstream_contract_test.go`）：
   - `TestUpstreamContractOwnerStaysLocal`：go/ast 扫**全仓** .go 文件的 import，本仓**不得** import `tellurion-contracts` 的任何包（owner 留在本仓 ⇒ 零跨仓类型依赖）。判据从源码推导而非手写清单——手写清单会在有人新增一条 import 时过期，而那时守卫还是绿的。
   - `TestUpstreamContractMigrationTriggerDocumented`：读 `docs/api-schema.md` 正文，§6.4 必须在位且含两个要素（core / orchestration 的 **import** 措辞 + **HTTP 交互不算**）。防「触发条件只活在某个人脑子里」。
   - **三个校验器的语义与 `UpstreamContractVersion` 一字未动**（派发明令），既有用例 `TestUpstreamContractWireShape` / `TestUpstreamContractArtifactFailClosed` / `TestServiceAttributionRequiresArtifactDatasetHash` 全绿。
3. **`service` 服务间角色入策略表**（裁-17 / 新 ADR-0004）：`internal/httpapi/policy.go` 的 8 条端点全部并列加 `service`（6 个 POST = trader + admin + service；2 个 GET = viewer + trader + admin + service）。**本仓 8 条端点全是分析计算 / 报告读取，没有审批 / 留痕 / 配置 / 交易端点**，不存在 risk 那种「机器凭证代劳人的责任动作」的风险面，故全表开放（理由与代价见 ADR-0004）。**签发与分发不在本仓**——本仓只保证「有 token 就能用」。
4. **三条 `service` 守卫用例**（`internal/httpapi/policy_test.go`）：`TestPolicy_ServiceRoleScope`（service 调 8 条端点都不被闸拦下 **且** viewer 打 POST 仍 403 ⇒ 新增档位不放松既有语义）、`TestPolicy_ServiceRoleCoexistsWithHumanRoles`（service 与真人角色**同在一条 `Roles`**——kit 角色判定 any-of 不排序，顶替真人角色会让真人调用方直接 403）、`TestPolicy_ServiceRoleDeclaredOnEveryRoute`（全表都开；新增端点漏表态即红，逼着做一次显式决定）。
5. **出站凭证用例补齐**（`internal/upstream/upstream_test.go`）：`TestUpstream_SendsServiceToken` 原来注释写「三个 reader 都必须带 Bearer」但**只测了 `ResultReader` 一个**——注释与实现不符。已补齐 `BenchmarkReader`（resolve + snapshot 两次请求）与 `ArtifactReader`（holdings + 因子暴露两次请求），并保留「空 token 不发 Authorization」这一腿。三条出站面现在都被钉住。
6. **文档同笔**：`api-schema.md`（§2 角色行 / §7 鉴权现状登记 / §6 抬头）、`dataflow.md`（§2 时序第 0 步）、`ARCHITECTURE.md`（§5 依赖方向图与实况 / §6 决策索引 / §9.4 鉴权注 / §9.7 R2 + R6 两行 / §9.8 取舍 1 与 4）、`AGENTS.md`（§1 状态 / §4 依赖表 contracts 行与 go.mod 注）、`README.md`（鉴权行）、新增 `docs/adr/0004-service角色在本仓全表开放.md` + ADR 索引。

**破坏验证（三条腿，改坏 → 红 → 逐字节还原 → 绿）**

- 腿 A（文档漂移守卫）：删掉 `docs/api-schema.md` 的 **§6.4 整段** ⇒ `TestUpstreamContractMigrationTriggerDocumented` 红（`找不到 "### 6.4" —— 「第二个使用方的触发条件」必须成文（裁决 ③）`），`LEG_A_EXIT=1`；逐字节还原后 `api-schema.md` 的 sha256 改前 / 还原后同为 `A846E5835659C5C3D37CFB4C654E463381703BEF450168B64FECCA75154DADBF`，复跑绿（`LEG_A_RESTORED_EXIT=0`）。
- 腿 A'（守卫不假绿）：把 §6.4 里的触发措辞弱化（「core 或 orchestration 出现 import analytics DTO 的 Go 代码」→「某些条件下」，「HTTP 交互不算」→「HTTP 交互」）⇒ 同一用例红并**逐条点名**缺哪三个要素（`缺少 "core 或 orchestration"` / `缺少 "import analytics DTO"` / `缺少 "不算"`），`LEG_EXIT=1`；还原后 sha256 一致、复跑绿。
- 腿 B（策略表守卫）：把 `internal/httpapi/policy.go` 8 条端点的 `"service"` 全部去掉 ⇒ `TestPolicy_ServiceRoleDeclaredOnEveryRoute` 红并**逐条点名**哪条路由的 `Roles` 缺 `service`，`LEG_B_EXIT=1`；逐字节还原后 `policy.go` 的 sha256 改前 / 还原后同为 `0393F150DEF7D40D678E944406CB3CD6AF94E5D19AC715AE24C89BB250478C82`，复跑绿（`LEG_B_RESTORED_EXIT=0`）。

**门禁（真实输出，本机 Windows / Go 1.25.0）**

```
gofmt -l .                      → 无输出（GOFMT_EXIT=0）
go build ./...                  → BUILD_EXIT=0
go vet ./...                    → VET_EXIT=0
go test ./... -count=1          → TEST_EXIT=0
  ok  tellurion-analytics/internal/analysis   0.156s
  ok  tellurion-analytics/internal/httpapi    0.180s
  ok  tellurion-analytics/internal/upstream   0.151s
```

**门禁（真实输出，WSL / Go 1.25.0 + **真** PG 16.15；`ANALYTICS_TEST_DSN` 与 `ORCHESTRATION_TEST_DSN` 均设置 ⇒ PG 腿与真进程腿**不是 skip**）**

```
gofmt -l . → GOFMT_EXIT=0 ；go build ./... → BUILD_EXIT=0 ；go vet ./... → VET_EXIT=0
go test ./... -count=1 → TEST_EXIT=0
  ok  tellurion-analytics/internal/analysis   0.344s
  ok  tellurion-analytics/internal/httpapi    2.208s
  ok  tellurion-analytics/internal/upstream   0.038s
PG / 真进程腿逐条实跑（PASS，非 skip）：
  TestPGMigrateIdempotent / TestPGReportLifecycle / TestPGReportSeedRoundTrip /
  TestPGCompletedReportIsImmutableInDB / TestPGBenchmarks / TestPGAttributionAndMonteCarlo /
  TestPGSensitivityBackfill / TestEndToEndWithPostgres / TestSensitivityEndToEndWithOrchestrationProcess
```

**未落（如实登记）**

1. **token 的签发与分发在 core / 运维侧（跨仓，本仓不落）**：本仓只保证「**有 token 就能用**」——不造 token、不存 token、不分发 token、不建服务账号注册表。签发入口是 kit `v0.1.2` 的 `IssueServiceToken`；注入靠 core `deploy/k8s/analytics-deployment.yaml` 的 `secretKeyRef`（core QT-71 ⑦，本仓无部署面）。派发明令**不许在本仓造 token 分发机制**。
2. ⚠️ **已知跨仓缺口（不在本仓，本条是本单最需要被下游知道的一条）**：kit `auth/httpadapter.Authenticate`（`v0.1.1` ~ `v0.1.3` 三个版本实测一致）要求 subject 可解析为 `int64`，而裁-17 的生产口径 `sub=service:<repo>` 是**非数字** subject ⇒ 生产 token 会被 kit 判 401「malformed token subject」，**压根到不了本仓的角色判定**。⇒ 本仓的守卫用例证明的是**本仓策略表**正确，**不**证明生产 token 能走通。core 的 `pkg/auth` 已为 service token 开特例（`RoleService` 分支不解析 user id），**net/http 适配这一侧还没有**。该缺口归 kit（放宽 subject 校验）或改裁-17 的 subject 口径，**已报协调者**（与 `tellurion-risk` 的同型缺口同一处，risk 侧记在其 ADR-0006 ④；kit 侧记在其 `docs/TASKS.md` KT-09 结案记录的「跨仓前置（未落）」条）。
3. **本仓未消费 contracts**：`go.mod` 零 contracts 依赖（守卫 `TestUpstreamContractOwnerStaysLocal` 钉住）。这不是「等契约发布就迁走」的临时态——owner 已裁 = 本仓（裁决 ③）。
4. **R1 的两条 core 侧残留偏差**（队列态放行 ⇒ pending / running 与引擎故障行读成 `404 run_not_found`；Migration 059 把迁移前的完成行回填成 `completed`）**仍不在本仓**，要修先裁「读面按队列态还是结论态放行」，改在 core。
5. **本轮仍没有真 core 进程 + 真 PG 的 invalid 行端到端**（沿用 R1 换面记录的缺口，本轮两腿都没碰这条链路）。

**验收现状**：QT-43 的 ⑤ 三条判据维持**真库 E2E** 层成立；本单仍保持 🔶，未落项收窄为「token 分发的跨仓腿（R1 的两条 core 侧残留偏差、真 core 进程的 invalid 行端到端、kit 的 service token subject 缺口）」。

---

> **开工前置（refactor-plan-review M5/M8，2026-10-20 登记；2026-10-22 更新执行状态）**：本表任何单开工前，必须先过 core `tellurion:docs/TASKS.md`「五、X 线」的**公共前置**两条：
> ① **M5 已结案（2026-10-21）** → `tellurion:docs/design/qt-decoupling/repo-inventory.md` §1.2：**六仓 ≠ 六进程**，本仓今天不升格容器；X4a 的纯函数迁移不需要容器，C 组 HTTP 面是升格时的目标形状。
> ② **M8 接口对表已落（2026-10-22）** → 本仓 [`ARCHITECTURE.md`](ARCHITECTURE.md) §9（模板 = `tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §4.1；**本行原指的 `target-module-topology.md` UnifiedDataPoint 节是幻影引用，已订正**）。
> 正文以 core 彼处为准，此处只留指针、不镜像。

## 施工序（候选单，开工前必须展开成六字段）

> 这张表是**施工顺序**，不是任务台账：每条在执行前要展开成六字段单（目标/上下文/要求/约束/验收/边界），并写明冻结契约的路径与节号。
> 顺序有依赖，**不要跳级**：跳序的代价是返工（例：X1a 的协议没冻结就写 aggregator，等于把协议写死在一个服务里）。
> 2026-10-21：候选单已展开为「一、本仓任务」的正式六字段条目（表中「上游单号」指向的 QT 单正文现在就在本仓）。

| 阶段 | 内容 | 上游单号 | 前置 |
|---|---|---|---|
| X4a | metrics + benchmark（同步，只读上游） | `QT-43` | 本仓立项 |
| X4b | attribution（Brinson / 因子） | `QT-43` | X4a |
| X4c | sensitivity + montecarlo（异步 job，`seed` 必填） | `QT-43` | X4a |
| X4d | 报告导出（html / csv；**pdf 已裁为非目标**，R5 2026-10-24） | `QT-43` | X4b |

## 二、跨仓指针（不在本仓，勿双写）

| 单号 | 主题 | 在哪 |
|---|---|---|
| QT-43 | X4 分析服务（基准 / 归因 / 敏感性 / 蒙特卡洛） | 已迁入本仓「一、本仓任务」（2026-10-21；core `tellurion:docs/TASKS.md` 留同号指针） |
| QT-08 | 本仓 CI / AGENTS / 台账由该单一并补齐 | `tellurion:docs/TASKS.md` |

---

## 维护本文件

- **新增单**：写全六字段；六字段里必须显式引用**冻结契约的路径与节号**（契约仓的包路径、设计文档的节号），不许写"参考相关实现"。
- **派工前先证伪前提**：读码核对工单写的前提；不符就停下回报 + 给拆法，不硬做、不扩范围。
- **跨仓引用写 `repo:path`**（如 `tellurion:docs/TASKS.md`），不做可点击相对链接。
- **顺序纪律**：同一波次内撞同一批文件的单按单号串行，一单一 commit。
