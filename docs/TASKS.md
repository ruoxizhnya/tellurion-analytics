---
status: active
last-verified: 2026-11-11
verified-by: 2026-11-11 全面审查 W7 台账立单（A5 授权）——新立 2 条六字段单（`AN-GRACEFUL-SHUTDOWN` / `AN-REPORT-CONFLICT-409`），证据坐标引 ODR-072 / ODR-069（`tellurion-docs:docs/archive/odr/`）；W5-F6 原派的 `AN-READYZ-PROBE` 经实测证伪撤销（analytics compose 已是 HTTP `/healthz`，弱探针实属 strategy，见 ODR-072 订正注记）；本轮只立单不修复。此前 2026-11-11 `ANALYTICS-METRIC-DRIFT` ✅（跨仓 ADR-039 §3 D4-T2 量测前置：`testdata/core-run-metrics.golden.json`（`kind=synthetic_nav`，core 权威实现产出）+ `TestComputedMetrics_NoDriftAgainstCoreGolden` 三指标对拍 + 阈值边界用例；破坏验证真做真还原；真实 run artifact 腿 ⏸ 见归档重开条件）；QT-43 收口 ✅（2026-11-10：⑤ 三条判据逐条点名用例 + 真库/真 core 腿均已成立；剩余项逐条定性为 ⑥ 边界外或不可逆历史数据——token 跨仓分发、Migration 059 存量回填、R2 owner 已裁；SVC-AN-G3 维持 ⛔）；R1 偏差①收口（2026-11-10：core K4 按裁决 ⑲ 落地 404 `run_not_found` / 409 `run_result_incomplete` / 409 `run_result_failed` 三态码，本仓 `ResultReader` 按契约 code 映射、未知 409 fail-closed；3 条新用例 + 一条破坏验证真做真还原）；`SVC-AN-G3` ⛔ 前置复核（2026-11-10：core 非测试 Go 对 analytics 零命中，且 `SVC-CORE-AN-RETIRE` 结论为「保留并冻结」+ K7 生产写者为 0 ⇒ 无消费者，维持 ⛔）；QT-43 真 core 进程 + 真 PG invalid 行端到端实跑（2026-11-10：core 持久读面 200 `status=invalid` → 本仓 409 `run_result_invalid` + reasons 原样透传，反向对照 404 / 503 各一条，库与进程已清）；QT-43 复核（未落项与 2026-10-26 一字不差）；SVC-ORCH-G1 调用方腿（`SensitivityQueue` 携带 service token、`ANALYTICS_ORCHESTRATION_TOKEN` 接线，2026-11-09）；SVC-AN-G1/G2/G4 真库腿补齐（便携 PostgreSQL 16.2 @ 55432：/readyz 200→503→200、迁移重放、全套 -count=1 绿）；**2026-11-10 主任裁决 A（ADR-0005）**：本仓定位独立只读分析服务/参考实现，core `metrics`/`reporting` 不迁移（保留冻结），`SVC-AN-G3` 关闭、K7 挂起
---

# TASKS — tellurion-analytics 未完成项

> **归档说明（2026-11-11）**：本文件已瘦身为「只装未做的事」。2026-11-10 及以前已收口（✅）的任务原文、施工记录、验收输出与破坏验证，已逐字移入 [`docs/archive/tasks-archive-2026-11.md`](archive/tasks-archive-2026-11.md)；该文件顶部有「检索索引」表（ID / 标题 / 收口日期 / commit / 证据链接）。检索：`grep -n "<任务ID或commit>" docs/archive/tasks-archive-2026-11.md`。
>
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `⏸ 挂起（外部依赖或已裁决暂不实施；**不计入开放项**，重开须另立单并满足该条「重开条件」）` / `✅ 完成（附日期，随即移入归档）`

## 一、未做 / 挂起 / 外部依赖

> 本节 = 本轮（2026-11-11）台账重整后剩下的挂起项（`K7`）+ 全面审查 W7 新立 2 单（`AN-GRACEFUL-SHUTDOWN` / `AN-REPORT-CONFLICT-409`）。此前已收口 / 已关闭单的正文与施工记录全部在归档文件里。

### K7 · `RegisterBenchmark` 零生产调用 [P2] ⏸

- **① 目标**：为「基准登记」这个能力找到真实写消费者，或明确它长期不接。
- **② 上下文**：`SVC-AN-G3` 经 2026-11-10 主任裁决 A（`docs/adr/0005-独立只读参考实现-core不迁移.md`）关闭——本仓定位为独立只读分析服务 / 参考实现，core 不迁移 `metrics` / `reporting`，`SVC-CORE-AN-RETIRE` 结论为「保留并冻结」。因此 `RegisterBenchmark` 至今**没有生产调用方**，登记不伪造消费者。
- **③ 要求**：若重开——先有确定的写入方（core 报告面 / UI / 编排），再补真实调用腿与对拍，最后才把它从挂起转开放。
- **④ 约束与非目标**：不为「消灭零调用」造内部消费者；不因此重启 `SVC-AN-G3`。
- **⑤ 验收**：出现真实调用方且有一条端到端用例。
- **⑥ 边界**：本仓端点；消费者归属由立单时确定。
- **重开条件**：出现基准写消费者时**另立单**。

### AN-GRACEFUL-SHUTDOWN · 分析服务无优雅停机，SIGTERM 直接掐断在途请求 [P1] ⬜

- **① 目标**：`cmd/analyticsservice` 收到 SIGTERM/SIGINT 时先停止接受新连接、给在途请求一个有界 drain 窗口再退出，而不是被信号直接杀死。
- **② 上下文（2026-11-11 全面审查 W5-F2，证据 ODR-072 `tellurion-docs:docs/archive/odr/odr-072-audit-wave5-security-reliability-deps.md`）**：`cmd/analyticsservice/main.go:136` 已建 `http.Server`，`:146` `srv.ListenAndServe()` 带 `err != http.ErrServerClosed` 兜底，但**全文件无 `signal.Notify` / `srv.Shutdown` / `NotifyContext`**（grep 零命中）⇒ `ErrServerClosed` 分支是死代码，SIGTERM 走默认处置直接终止进程。同批四仓（data/risk/analytics/strategy）同形缺陷，见各仓 `*-GRACEFUL-SHUTDOWN` 单；A4 停机演练先行。
- **③ 要求**：① 用 `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` 或等价机制捕获终止信号；② 信号到达后调 `srv.Shutdown(drainCtx)`，drain 窗口有界（建议 ≤10s）；③ drain 超时则 `srv.Close()` 兜底并非零退出；④ 正常 `ListenAndServe` 返回 `ErrServerClosed` 时退出码 0（顺手把 `!=` 改 `errors.Is` 更稳）；⑤ 启动日志点名 drain 窗口时长。
- **④ 约束与非目标**：不改路由/鉴权/限流/上游 fail-closed 语义；不引入新依赖（标准库 `os/signal` + `context`）；不动 `ResultReader` 与上游装配；不改成「无限等待在途请求」。
- **⑤ 验收**：真起服务（A4 授权）→ 发一个慢请求 → SIGTERM → 断言该请求在 drain 窗口内正常完成、进程退出码 0；破坏验证 = 去掉信号捕获改回裸 `ListenAndServe` ⇒ 慢请求被掐断（连接 reset / 非零退出），还原复绿。`go build/vet/test ./... -count=1` 全绿。
- **⑥ 边界**：本仓 `cmd/analyticsservice/main.go` 的 run/serve 段；其余三仓停机归各自单。
- **owner**：待定

### AN-REPORT-CONFLICT-409 · 写侧不可变冲突 `ErrReportConflict` 被误归 500 internal [P3] ⬜

- **① 目标**：让「同 `report_id` 异内容」这类写侧不可变冲突回 **409**（带稳定 wire code），而不是落进 `classify` 的 `default` 被报成 500 `internal`——调用方据此能区分「我重复提交了不同内容」（不可重试）与「服务真坏了」（可重试）。
- **② 上下文（2026-11-11 全面审查 W2-F12，证据 ODR-069 `tellurion-docs:docs/archive/odr/odr-069-audit-wave2-core-architecture.md`）**：`internal/analysis/errors.go:18` 定义 `ErrReportConflict`；`internal/analysis/pgstore.go:126`（注释「已存在且 completed：只允许同键同内容（no-op），否则 ErrReportConflict」）及 `:194/:267/:295/:349/:354/:360` 多处 `return ErrReportConflict`（PutReport/PutAttribution/PutMonteCarlo 写侧）。但 `internal/httpapi/api.go:357-393` 的 `classify()` switch **没有 `ErrReportConflict` 分支**（grep 零命中）⇒ 落 `default: return http.StatusInternalServerError, "internal", nil`。语义上这是 409 Conflict（资源已存在且不可变），却报成 500。`docs/api-schema.md §2` 的 409 词表（`run_result_*` / `benchmark_data_unavailable` / `report_not_ready`）也未登记写侧冲突码。
- **③ 要求**：① `classify()` 增 `case errors.Is(err, analysis.ErrReportConflict): return http.StatusConflict, "report_conflict", nil`（wire code 名以 api-schema 词表为准，施工时定稿）；② `docs/api-schema.md §2/§3` 登记该 409 码与「同键同内容 no-op / 同键异内容 409 不可重试」的幂等语义；③ 补一条用例：PutReport 同键异内容 ⇒ 409 `report_conflict`（不是 500）。
- **④ 约束与非目标**：不改 `ErrReportConflict` 的触发条件与 pgstore 写侧逻辑（只改 HTTP 归类）；不动读侧 `run_result_*` 三态码；不引入新哨兵；不改 `reports_completed_immutable` 约束。
- **⑤ 验收**：新用例断言写侧冲突 = 409 + 登记码；破坏验证 = 删掉 `classify` 的新分支 ⇒ 用例红（回落 500），还原复绿；`go build/vet/test ./... -count=1` 全绿；api-schema 词表与代码一致。
- **⑥ 边界**：本仓 `internal/httpapi/api.go` 的 `classify` + `docs/api-schema.md` 词表 + 一条用例；写侧领域逻辑不动。
- **owner**：待定

## 二、跨仓指针（不在本仓，勿双写）

| 事项 | 去哪 |
|---|---|
| core 侧指标/报告实现（保留冻结）与退役结论 | `tellurion:docs/TASKS.md` |
| 编排侧调用与 token | `tellurion-orchestration:docs/TASKS.md` |

## 维护本文件

- **新增单**：写全六字段；六字段里必须显式引用**冻结契约的路径与节号**（契约仓的包路径、设计文档的节号），不许写"参考相关实现"。
- **派工前先证伪前提**：读码核对工单写的前提；不符就停下回报 + 给拆法，不硬做、不扩范围。
- **跨仓引用写 `repo:path`**（如 `tellurion:docs/TASKS.md`），不做可点击相对链接。
- **顺序纪律**：同一波次内撞同一批文件的单按单号串行，一单一 commit。
