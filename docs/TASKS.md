---
status: active
last-verified: 2026-11-10
verified-by: QT-43 收口 ✅（2026-11-10：⑤ 三条判据逐条点名用例 + 真库/真 core 腿均已成立；剩余项逐条定性为 ⑥ 边界外或不可逆历史数据——token 跨仓分发、Migration 059 存量回填、R2 owner 已裁；SVC-AN-G3 维持 ⛔）；R1 偏差①收口（2026-11-10：core K4 按裁决 ⑲ 落地 404 `run_not_found` / 409 `run_result_incomplete` / 409 `run_result_failed` 三态码，本仓 `ResultReader` 按契约 code 映射、未知 409 fail-closed；3 条新用例 + 一条破坏验证真做真还原）；`SVC-AN-G3` ⛔ 前置复核（2026-11-10：core 非测试 Go 对 analytics 零命中，且 `SVC-CORE-AN-RETIRE` 结论为「保留并冻结」+ K7 生产写者为 0 ⇒ 无消费者，维持 ⛔）；QT-43 真 core 进程 + 真 PG invalid 行端到端实跑（2026-11-10：core 持久读面 200 `status=invalid` → 本仓 409 `run_result_invalid` + reasons 原样透传，反向对照 404 / 503 各一条，库与进程已清）；QT-43 复核（未落项与 2026-10-26 一字不差）；SVC-ORCH-G1 调用方腿（`SensitivityQueue` 携带 service token、`ANALYTICS_ORCHESTRATION_TOKEN` 接线，2026-11-09）；SVC-AN-G1/G2/G4 真库腿补齐（便携 PostgreSQL 16.2 @ 55432：/readyz 200→503→200、迁移重放、全套 -count=1 绿）；**2026-11-10 主任裁决 A（ADR-0005）**：本仓定位独立只读分析服务/参考实现，core `metrics`/`reporting` 不迁移（保留冻结），`SVC-AN-G3` 关闭、K7 挂起
---

# TASKS — tellurion-analytics 未完成项

> **归档说明（2026-11-11）**：本文件已瘦身为「只装未做的事」。2026-11-10 及以前已收口（✅）的任务原文、施工记录、验收输出与破坏验证，已逐字移入 [`docs/archive/tasks-archive-2026-11.md`](archive/tasks-archive-2026-11.md)；该文件顶部有「检索索引」表（ID / 标题 / 收口日期 / commit / 证据链接）。检索：`grep -n "<任务ID或commit>" docs/archive/tasks-archive-2026-11.md`。
>
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `⏸ 挂起（外部依赖或已裁决暂不实施；**不计入开放项**，重开须另立单并满足该条「重开条件」）` / `✅ 完成（附日期，随即移入归档）`

## 一、未做 / 挂起 / 外部依赖

> 本节 = 本轮（2026-11-11）台账重整后剩下的唯一挂起项（`K7`）。此前已收口 / 已关闭单的正文与施工记录全部在归档文件里。

### K7 · `RegisterBenchmark` 零生产调用 [P2] ⏸

- **① 目标**：为「基准登记」这个能力找到真实写消费者，或明确它长期不接。
- **② 上下文**：`SVC-AN-G3` 经 2026-11-10 主任裁决 A（`docs/adr/0005-独立只读参考实现-core不迁移.md`）关闭——本仓定位为独立只读分析服务 / 参考实现，core 不迁移 `metrics` / `reporting`，`SVC-CORE-AN-RETIRE` 结论为「保留并冻结」。因此 `RegisterBenchmark` 至今**没有生产调用方**，登记不伪造消费者。
- **③ 要求**：若重开——先有确定的写入方（core 报告面 / UI / 编排），再补真实调用腿与对拍，最后才把它从挂起转开放。
- **④ 约束与非目标**：不为「消灭零调用」造内部消费者；不因此重启 `SVC-AN-G3`。
- **⑤ 验收**：出现真实调用方且有一条端到端用例。
- **⑥ 边界**：本仓端点；消费者归属由立单时确定。
- **重开条件**：出现基准写消费者时**另立单**。

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
