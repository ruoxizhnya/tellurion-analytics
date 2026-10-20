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

**现状（必须先知道）**：core 的迁移是「**启动时按序执行一组幂等 DDL**」——唯一真相是 `pkg/storage/postgres.go` 的 `migrationStatements()` 数组（`migrate()` 在服务启动时逐条跑）；**没有版本表、没有 down、没有执行记录**（有意取舍，理由见 core 的 `migrations/README.md`）。`migrations/`（14 个）与 `docs/migrations/`（25 个）里的 `.sql` 是**历史镜像，不执行**——改它们不会改库。

**本仓约定**（从建仓第一天执行）：

- 迁移文件只放 `migrations/`，命名 `<序号>_<动作>.sql`，**本仓内**递增（不与别的仓共享序列，也不复用号）。
- **执行机制随第一个建表单定，二选一（不能"都不选"）**：
  1. **core 式**：Go 里的 `migrationStatements()` 数组 + 启动执行，幂等、无版本表——与 core 一致、运维零新增；
  2. **版本化**：引入迁移库 + `schema_migrations` 表（可 down、有执行记录）——服务变多后更稳，但要一次性 baseline。
  **取向**：X1~X3 期间优先 ①（少一层依赖），服务数 ≥3 或出现"必须先降级"的需求时重估 ②。
- **只前滚**：已合并的迁移不改；修错靠新增一条。
- 每条必须可重入（`IF NOT EXISTS` / `IF EXISTS`），且能在空库上一次跑完。
- 跨 schema 取数只走**只读视图或 API**；出现 `join` 别人的表就是边界腐化的第一信号。
- 结构变更的同步义务：本文件 + `docs/ARCHITECTURE.md` 的「数据归属」节必须同一次提交里更新。

## 5. 待定

- 报告体存 JSONB 还是对象存储：本地栈先 JSONB + 体积上限。
