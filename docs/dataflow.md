---
status: active
last-verified: 2026-10-24
verified-by: QT-43 施工 ②（PG 持久化 + 上游读取适配器接线）
---

# Dataflow — tellurion-analytics

> 目的：把「数据从哪来、在谁手里变形、到哪去」写死。**任何跨仓数据交换都必须能在本图上找到一条边**；找不到 = 这段代码在偷偷建第二条路。
> **现状（2026-10-24）**：计算边与**读取边**都已实现（`internal/analysis` + `internal/upstream` + `internal/httpapi` 8 端点）；读取边由进程参数配置：`-core-url`（结果）/ `-data-url`（X1b 基准）/ `-artifact-url`（holdings / 因子暴露）。**未配置的边**保持 nil，调用即 `503 dependency_unavailable`。sensitivity 的**逐点回填**仍未接（等 G7/X5b）。

## 1. 主链路

```
回测结果（core / run）        基准行情、因子值（data）
        │                              │
        └──────────┬───────────────────┘
                   ↓  只读
          tellurion-analytics  ── 写 analysis.*
                   │
                   ├── 同步：metrics / benchmark / attribution / correlation
                   └── 异步 job：sensitivity / montecarlo（seed 必填）
                   ↓
              report（JSON / HTML / CSV）──► ui
```

## 2. 关键时序

1. `POST /api/analysis/metrics {run_id}` → 拉结果 DTO → 分区（`from_result` 原样 / `computed` 补算）→ 落 `analysis.reports`
2. `POST /api/analysis/benchmark {run_id, benchmark_code, window?}` → 查 `analysis.benchmarks` 最新登记 → 取基准序列（data）→ 对齐 → 超额 / TE / IR / beta / 滚动 → 落报告
3. `POST /api/analysis/attribution {run_id, method}` → brinson 读 holdings artifact / factor 读因子暴露 → 分项 + residual → 落 `analysis.reports` + `analysis.attribution`
4. `POST /api/analysis/montecarlo {run_id, n, seed, method?}` → 建 pending 报告 → **本仓异步**重抽样（`math/rand` + 显式 seed）→ 推进 completed / failed → 落 `analysis.mc_runs`
5. `POST /api/analysis/sensitivity {run_id, param_grid}` → **把网格提交给 X5（QT-44）** → 建 pending 报告；逐点结果回填与 `analysis.sensitivity_runs` 落库**依赖 QT-44**（R3）；**本仓不跑引擎**
6. `POST /api/analysis/correlation {run_ids[]}` → 逐个拉结果 → 对齐截断 → Pearson 矩阵 → 落报告（**不返回部分矩阵**）
7. `GET /api/analysis/reports/{id}` → 读报告；导出走 `/export?format=html|csv`（确定性渲染）

**读取边（`internal/upstream`）**

| 端口 | 上游 | 失败语义 |
|---|---|---|
| `ResultReader` | core `GET /api/backtest/{id}/report`（R1 落地前的 interim；只认带 `status` 的结果） | 404 → `run_not_found`；缺 `status` → `run_result_incomplete`（**不解析错误串**）；网络 / 5xx → `dependency_unavailable` |
| `BenchmarkReader` | tellurion-data X1b：`GET /datasets/resolve` → `GET /datasets/{hash}` | 任何取不到 / basis 不一致 → `benchmark_data_unavailable` |
| `HoldingsProvider` / `FactorExposureProvider` | interim artifact 面：`GET /runs/{id}/portfolio-snapshot`、`/runs/{id}/factor-exposures` | 404 / 空体 → `run_result_incomplete`（点名缺哪个）；网络 / 5xx → `dependency_unavailable` |

**状态推进**：同步端点一步到 `completed`；异步端点先落 `pending`，由执行器推进到 `completed` 或 `failed`。`completed` 是**终态、不可变**——同键重复提交返回已存报告（字节一致）。

## 3. 失败模式（每一处 fail-closed 都要能指名）

| 失败点 | 症状 | 兜底 |
|---|---|---|
| 缺 seed | 结果不可复现 | 400 `seed_required`，拒算 |
| 上游结果不完整 | 指标算出来是错的 | 409 `run_result_incomplete`，点名缺哪个输入（`portfolio_values` / `benchmark_series` / `run result`） |
| 结果读取面把 invalid / failed 当 completed | 产出「看起来正常」的假报告 | 三值分别拒算（`ARCHITECTURE.md` §9.1 F1）；**不解析 job 错误串**；缺 `status` 的异步结果直接 409 |
| 上游读取面把「没有 status」的旧编码当成功 | 同上 | `ResultReader` 只接受带 K9 `status` 的结果（见上表） |
| 上游端口未配置 | 端点不可用 | 503 `dependency_unavailable`（进程启动日志会列出哪些端口已接线）；**不伪造结果、不返回空报告** |
| sensitivity 没有 X5 job 队列 | 网格跑不出来 | 503；**不在本仓起回测循环**（R3） |
| Brinson 没有 holdings artifact | 归因算不出来 | provider 未配置 ⇒ 503；配置了但 artifact 缺失 ⇒ 409 `run_result_incomplete`；**不用成交明细倒推持仓**（R2） |
| 基准口径不同（全收益 vs 价格） | 超额失真 | 409 `benchmark_data_unavailable`（登记口径 vs 序列口径不一致即拒）；基准登记时钉住口径 + hash |
| 报告格式变更 | 历史报告打不开 | 保留旧读取器 |
| 报告未完成就导出 | 半成品外流 | 409 `report_not_ready` |

## 4. 不变量（可被测试钉住的）

1. 同 `(run_id, analysis_type, params_hash, input_hash, seed, metrics_version)` → 同 `report_id`；重放返回**已存的**同一份报告（字节一致，不重算）。PG 上 completed 行不可变（应用层 + 触发器双保险，见 `db-schema.md` §1 / ADR-0002）。
2. 本仓不修改上游任何表（只读，可用事务权限验证）。
3. 任何报告都能追溯到 `run_id` + `metrics_version`。
4. 归因恒等式成立（容差 `1e-8`）：factor 的 `Σcontributions + residual == total_return`；brinson 的 `Σ(allocation+selection+interaction) == total_return - benchmark_return`。residual **不摊进**贡献，且必须落 `analysis.attribution.residual`。
5. 同 seed ⇒ 蒙特卡洛报告**字节一致**（`math/rand.New(rand.NewSource(seed))`，不读时钟）。
6. 相关性矩阵对称、对角 `1.0`；任一 run 不可用即整体拒绝，**不返回部分矩阵**。
7. 未接线端口不会产出任何报告（失败只返回错误，不落行）。

> 上述不变量在 `internal/analysis/*_test.go`、`internal/upstream/*_test.go` 与 `internal/httpapi/api_test.go` 里各自有钉住的用例（恒等式、seed 复现、`report_id` 敏感性、`completed` 不可变、503 线码）；`internal/httpapi/e2e_test.go` 在真库上复验同一组红线。

## 5. 与契约文档的关系

- 表结构 → `docs/db-schema.md`
- 接口形状 → `docs/api-schema.md`
- 本文件只讲**流向与顺序**，不重复字段定义。
