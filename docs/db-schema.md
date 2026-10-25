---
status: active
last-verified: 2026-10-25
verified-by: W8 sensitivity 逐点回填（sensitivity_runs 加 parent_job_id；points 一次写满）
---

# DB Schema — tellurion-analytics

> **所有权声明**：本仓**拥有 `analysis` schema 的 DDL**：报表、基准登记、敏感性、蒙特卡洛批次、归因结果。是这组表的唯一写者。
> 上级依据：`tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §1 —— **DDL 只有一个 owner**，同一个 PG 实例、每服务一个 schema，将来拆库是"按 schema 搬走"而不是重写。
> 本文件是本仓表结构的**唯一真相源**：代码里的建表语句与它不一致时，**改代码**；要改表结构，先改本文件（结构级变更还要立 ADR）。
> **现状（2026-10-24）**：5 张表的 DDL + `reports_completed_immutable` 触发器在 `internal/analysis/ddl.go`；`PG.Migrate` 在进程启动时单事务按序执行（`cmd/analyticsservice` 的 `-dsn` 有值时）。下表是 DDL 的逐字对表，不是"计划"。

## 1. 归属表

> DDL 唯一来源：`internal/analysis/ddl.go`。`analysis.reports` 的 `UNIQUE NULLS NOT DISTINCT` 需要 **PG15+**：默认唯一约束不拦 NULL 重复，非 MC 报告的 `seed` 为 NULL 时会重复落行。

| 表 | 主键 | 关键列与约束 | 现状 |
|---|---|---|---|
| `analysis.reports` | `report_id` | `run_id`、`analysis_type`（CHECK 六值）、`status`（CHECK pending/running/completed/failed）、`metrics_version`、`params_hash CHAR(64)`、`input_hash CHAR(64)`、`seed BIGINT`（可空）、`body JSONB`、`error TEXT DEFAULT ''`、`created_at` / `updated_at`；UNIQUE `NULLS NOT DISTINCT (run_id, analysis_type, params_hash, input_hash, seed, metrics_version)`；**CHECK `(status='completed') = (body IS NOT NULL)`**、**CHECK `status<>'failed' OR error<>''`**；`BEFORE UPDATE` 触发器 `reports_completed_immutable` 拒绝改 completed 行（SQLSTATE 55000） | PG 已接线 |
| `analysis.benchmarks` | `(code, adjustment_basis, dataset_hash)` | `source TEXT DEFAULT ''`、`registered_at`；**只存引用，不存行情本体**（行情归 data）；新 `dataset_hash` = 新一行，不覆盖 | PG 已接线 |
| `analysis.sensitivity_runs` | `report_id` → `analysis.reports` | `run_id`、`grid JSONB NOT NULL`、`points JSONB`（逐点结果，可空直到有结果）、`parent_job_id TEXT NOT NULL DEFAULT ''`（X5b 父作业，2026-10-25 加，`ALTER … ADD COLUMN IF NOT EXISTS`） | PG 已接线（提交落行 + 逐点回填已实现） |
| `analysis.mc_runs` | `report_id` → `analysis.reports` | **`seed BIGINT NOT NULL`**、`n INTEGER CHECK (n>0)`、`method`（CHECK bootstrap/shuffle）、`input_hash CHAR(64)`、`summary JSONB NOT NULL` | PG 已接线（写入已实现） |
| `analysis.attribution` | `report_id` → `analysis.reports` | `method`（CHECK brinson/factor）、`contributions JSONB NOT NULL`、**`residual DOUBLE PRECISION NOT NULL`**、`input_hash CHAR(64)` | PG 已接线（写入已实现） |

另有 `reports_run_type_idx`（`run_id, analysis_type`）便于按 run 反查报告。

**completed 不可变（双层）**：应用层 `PG.PutReport` 先判断（同键同内容 no-op、异内容 `ErrReportConflict`）；数据库层 `reports_completed_immutable` 触发器兜底——绕过应用层直接 `UPDATE` 也会被拒。理由与取舍见 [`adr/0002-报告不可变由PG触发器兜底.md`](adr/0002-报告不可变由PG触发器兜底.md)。`CHAR(64)` 列在读取侧用 `rtrim`，避免短值被空格填充污染。

**sensitivity 的 points（2026-10-25）**：提交时只落 `grid` + `parent_job_id`（`points` 为 NULL）；父作业落定后由 `PG.PutSensitivity` 一次写满。写满后不可变（同内容 no-op、异内容 `ErrReportConflict`）。缺 point 时**不写 points**，报告落 `failed`——「半张网格」比没有更危险。

**只读输入**：回测结果（`run` / 内核输出）、基准行情（数据服务）、因子值（数据服务）。**本仓不写它们的表。**

> **目标键与写入语义见** `ARCHITECTURE.md` §9.5（M8 对表）。`analysis.reports` 以 `report_id` 为 PK、六元组唯一；`analysis.mc_runs.seed` NOT NULL；`analysis.benchmarks` 的 `adjustment_basis` 与 `dataset_hash` 进登记身份。本文件仍是 **DDL 唯一真相源**。

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

- **执行机制已定：取向 ①（core 式）**——DDL 是 Go 数组 `internal/analysis/ddl.go: migrationStatements`，由 `PG.Migrate` 在进程启动时单事务按序执行，幂等、无版本表、无 down。**不建 `migrations/*.sql`**（本仓没有历史镜像要维护）。
- **只前滚**：已合并的语句不改；修错靠新增一条。
- 每条必须可重入（`IF NOT EXISTS` / `IF EXISTS` / `CREATE OR REPLACE`），且能在空库上一次跑完。
- 跨 schema 取数只走**只读视图或 API**；出现 `join` 别人的表就是边界腐化的第一信号。
- 结构变更的同步义务：本文件 + `docs/ARCHITECTURE.md` 的「数据归属」节必须同一次提交里更新。

## 5. 待定

- 报告体体积上限（`body JSONB`）：`api-schema.md` 只限请求体 1 MiB；报告体上限与超限处置（拒绝 vs 外部对象存储）尚未裁决。
