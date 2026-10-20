---
status: active
last-verified: 2026-10-20
verified-by: 功能边界固化（三份契约：db / api / dataflow）
---

# API Schema — tellurion-analytics

> **契约地位**：HTTP，**全部只读语义**（POST 表示"提交计算"，不改上游）。重计算走异步 job。
> 本文件是**本仓对外接口的唯一真相源**。代码与它不一致时，先判断谁错：契约错 → 改本文 + 立 ADR；实现错 → 改代码。
> 跨仓消费方（core / agent / ui / 其他服务）按本文件编码，**不读本仓实现**。

## 1. 接口面

| 方法 | 路径 | 入参 → 出参 |
|---|---|---|
| POST | `/api/analysis/metrics` | `{run_id}` → 绩效指标全集（含收益类补齐） |
| POST | `/api/analysis/benchmark` | `{run_id, benchmark_code}` → 超额 / IR / 跟踪误差 / 滚动对比 |
| POST | `/api/analysis/attribution` | `{run_id, method: brinson\|factor}` → 归因表 |
| POST | `/api/analysis/sensitivity` | `{run_id, param_grid}` → 敏感性矩阵（**异步 job**） |
| POST | `/api/analysis/montecarlo` | `{run_id, n, seed}` → 分布 / 置信区间（**异步 job**） |
| POST | `/api/analysis/correlation` | `{run_ids[]}` → 相关性矩阵 |
| GET | `/api/analysis/reports/{id}` | 报告（JSON / HTML） |
| GET | `/api/analysis/reports/{id}/export?format=html\|csv\|pdf` | 导出 |

## 2. 错误码与 fail-closed 语义

| 情况 | 返回 | 说明 |
|---|---|---|
| `run_id` 不存在 | 404 + `run_not_found` | 不返回空报告（空报告会被误读为"零收益"） |
| 缺 `seed`（蒙特卡洛） | 400 + `seed_required` | **fail-closed**：没有种子就不算 |
| 基准未登记 | 404 + `benchmark_not_registered` | 不自动抓数据 |
| 上游结果不完整 | 409 + `run_result_incomplete` | 拒绝计算，不猜 |

**原则**：宁可报错，也不产出一份"看起来正常"的错误报告。

## 3. 幂等性与重试

- 计算类端点按 `(run_id, params_hash, seed)` 幂等：同参数重复提交复用已有报告。
- 只读 GET 可任意重试。
- 异步 job 的提交返回 `report_id`，轮询用 GET。

## 4. 版本与兼容

- 报告**格式**演进要保留旧版本读取（历史报告不能失效）。
- 指标定义变更算 breaking：老报告不重算，新报告注明口径版本。
- 端点只增不删。

## 5. 不提供什么（边界）

- 不做数据抓取（基准行情归数据服务）
- 不做回测 / 策略执行
- 不做因子研究（`quant.ic_analysis` 那套留在 core）
- 不做实时分析（全部按 run 的离线计算）
