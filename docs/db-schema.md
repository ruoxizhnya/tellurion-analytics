---
status: active
last-verified: 2026-10-20
verified-by: 功能边界固化（三份契约：db / api / dataflow）
---

# DB Schema — tellurion-analytics

> **所有权声明**：本仓**拥有 `analysis` schema 的 DDL**：报表、基准登记、敏感性、蒙特卡洛批次、归因结果。是这组表的唯一写者。
> 上级依据：`tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §1 —— **DDL 只有一个 owner**，同一个 PG 实例、每服务一个 schema，将来拆库是"按 schema 搬走"而不是重写。
> 本文件是本仓表结构的**唯一真相源**：代码里的建表语句与它不一致时，**改代码**；要改表结构，先改本文件（结构级变更还要立 ADR）。

## 1. 归属表

| 表 | 内容 | 现状 |
|---|---|---|
| `analysis.reports` | 分析任务结果（`run_id` → 报告 JSON/HTML） | 待建 |
| `analysis.benchmarks` | 基准序列**登记**（代码、来源、hash） | 待建（基准行情本体归数据服务，此处只存引用） |
| `analysis.sensitivity_runs` | 参数敏感性网格结果 | 待建 |
| `analysis.mc_runs` | 蒙特卡洛 / bootstrap 批次（**`seed` 必填**） | 待建 |
| `analysis.attribution` | 归因结果（Brinson / 因子） | 待建，与 `quant.ic_analysis` / `factor_returns` 区分：那是因子研究，这是**回测结果归因** |

**只读输入**：回测结果（`run` / 内核输出）、基准行情（数据服务）、因子值（数据服务）。**本仓不写它们的表。**

## 2. 只读输入（不属于本仓，禁止写入）

| 数据 | 真实 owner |
|---|---|
| 回测结果 / 指标 | core / `run` |
| 基准行情、因子值 | `tellurion-data` |
| 规则快照 | `tellurion-risk` |

本仓按 `run_id` 拉结果，**零 import `pkg/backtest` 引擎**（只依赖结果 DTO）。

## 3. 红线

1. **只读上游**：POST 只是"提交计算"，不改上游任何数据。
2. **确定性红线**：蒙特卡洛 / bootstrap 必须种子化留痕（`mc_runs.seed` NOT NULL），否则同参不可复现（ADR-D15 的 Seed 治理）。
3. **不重算上游已算的指标**：绩效指标以结果里的为准，缺失的才补算，并标注来源。
4. 重计算必须异步（不得阻塞 HTTP 请求）。

## 4. 建表与迁移

**现状（必须先知道）**：core 今天**没有迁移执行器**——建表靠 `pkg/storage/*.go` 里的内联 `CREATE TABLE IF NOT EXISTS`，另有 `migrations/`（14 个）与 `docs/migrations/`（25 个）两处**手工执行**的 SQL。这是拆分服务时必须先收口的债，不要在本文假设某个执行器已经存在。

**本仓约定**（从建仓第一天执行）：

- 只用 `migrations/` 一处，命名 `<序号>_<动作>.sql`，**本仓内**递增（不与别的仓共享序列，也不复用号）。
- **只前滚**：已合并的迁移文件不改；修错靠新增一条。
- 每条必须可重入（`IF NOT EXISTS` / `IF EXISTS`），且能在空库上一次跑完。
- 跨 schema 取数只走**只读视图或 API**；出现 `join` 别人的表就是边界腐化的第一信号。
- 迁移执行器与 applied 版本表随**第一个真正需要建表的单**一起落地；落地前本仓不产生"半迁移"。
- 结构变更的同步义务：本文件 + `docs/ARCHITECTURE.md` 的「数据归属」节必须同一次提交里更新。

## 5. 待定

- 报告体存 JSONB 还是对象存储：本地栈先 JSONB + 体积上限。
