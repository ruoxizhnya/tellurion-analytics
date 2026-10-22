---
status: active
last-verified: 2026-10-22
verified-by: M8 接口对表（QT-43 前置 ②）
---

# API Schema — tellurion-analytics

> **实现状态（2026-10-20）**：本仓目前**只有占位 `doc.go`，没有任何 handler**——下列端点**全部未实现**。
> 本文件是**目标契约**，不是现状说明书；开工顺序见 `docs/TASKS.md` 的「施工序」。

> **契约地位**：HTTP，**全部只读语义**（POST 表示"提交计算"，不改上游）。重计算走异步 job。
> 本文件是**本仓对外接口的唯一真相源**。代码与它不一致时，先判断谁错：契约错 → 改本文 + 立 ADR；实现错 → 改代码。
> 跨仓消费方（core / agent / ui / 其他服务）按本文件编码，**不读本仓实现**。
> **请求 / 响应形状**：本文只冻结**路径、错误码、幂等与兼容**；逐端点的目标签名、DTO 字段与版本策略见 `ARCHITECTURE.md` §9（M8 接口对表），两处必须同步改，不复制形状。
> **鉴权**：按 core `QT-50 ④` 统一（JWT + RBAC），默认 fail-closed；本仓不另定鉴权契约。
> **⛔ 前置**：`POST /api/analysis/sensitivity` 依赖 X5 的 job 队列（QT-44），`POST /api/analysis/attribution` 的 Brinson 依赖 holdings artifact——两者在 `ARCHITECTURE.md` §9.7 R2/R3 落地前**不实现**（形状不冻结）。

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
| GET | `/api/analysis/reports/{id}/export?format=html\|csv` | 导出（**pdf 未定**，见 `ARCHITECTURE.md` §9.7 R5） |

## 2. 错误码与 fail-closed 语义

> 错误码与 `ARCHITECTURE.md` §9.4 **双向一一对应**；本节是 code 的**唯一词表**，新增 code 必须先改这里。

| 情况 | 返回 | 说明 |
|---|---|---|
| 无凭证 / token 过期 | 401 `unauthenticated`（body 随 QT-50 ④ 统一面） | 不重试，补齐凭证 |
| 角色不足 | 403 `forbidden` | 不重试 |
| 请求体 / 参数非法（含 `n<=0`、空网格、ID 数越界） | 400 `invalid_params` | 修正后重试 |
| 归因方法不支持 | 400 `method_not_supported` | 只接受 `brinson` / `factor` |
| 缺 `seed`（蒙特卡洛） | 400 `seed_required` | **fail-closed**：没有种子就不算（`seed=0` 是合法显式种子） |
| `run_id` 不存在 | 404 `run_not_found` | 不返回空报告（空报告会被误读为"零收益"） |
| 基准未登记 | 404 `benchmark_not_registered` | 不自动抓数据 |
| 报告不存在 | 404 `report_not_found` | 不重试 |
| 回测结果 **invalid** | 409 `run_result_invalid` + `invalid_reasons` | **拒算**；不得当 completed（K9 三值原样透传） |
| 回测结果 **failed** | 409 `run_result_failed` + `error` | 拒算；不当"可重试成功" |
| 上游结果不完整（缺曲线 / holdings / 因子暴露） | 409 `run_result_incomplete` | 拒算，不猜；明细点名缺哪个输入 |
| 基准已登记但取不到序列 | 409 `benchmark_data_unavailable` | 不回落别的基准 / 不拿价格指数冒充全收益 |
| 报告未完成（导出 / 读取报告体） | 409 `report_not_ready` | 轮询 `GET /reports/{id}` |

**原则**：宁可报错，也不产出一份"看起来正常"的错误报告。`invalid` / `failed` / `incomplete` 三者**分开报**——它们对调用方的处置完全不同（换 run / 修上游 / 等数据）。

## 3. 幂等性与重试

- 计算类端点按 `(run_id, analysis_type, params_hash, input_hash, seed, metrics_version)` 派生**确定性** `report_id`：同键重复提交复用已有报告（字节一致），不重复计算。
- `input_hash` 覆盖所有读取输入（结果体 / 基准 `dataset_hash` / holdings artifact / 因子数据集）：**输入换版 ⇒ 新报告**，不复用旧结论。
- `params_hash` 是请求参数（基准代码 / 归因方法 / 网格 / n 等）的 canonical hash；不同参数 ⇒ 不同 `report_id`。
- 异步 job 的提交返回 `{report_id, status:"pending"}`，轮询用 `GET /reports/{id}`；**作业态（pending/running/completed/failed）不是 K9 三值**，报告只在 completed 时存在。
- `POST /correlation` **不返回部分矩阵**：任一 run 不可用（缺 / invalid / failed / 点数不足）即整体拒绝。
- 只读 GET 与导出可任意重试；导出未完成报告 → 409。

## 4. 版本与兼容

> 逐端点 / 逐符号的版本策略见 `ARCHITECTURE.md` §9.6；本节只留三条总则。

- 报告**格式**演进要保留旧版本读取（历史报告不能失效）。
- 指标定义变更算 breaking：老报告不重算，新报告注明口径版本（`metrics_version`）。
- 端点只增不删。导出格式：html / csv 稳定；**pdf 未定**（无渲染器，见 `ARCHITECTURE.md` §9.7 R5），冻结前不承诺。

## 5. 不提供什么（边界）

- 不做数据抓取（基准行情归数据服务）
- 不做回测 / 策略执行；sensitivity 只把网格**提交给 X5**，不在本仓跑引擎
- 不做因子研究（`ic_analysis` / `factor_returns` 那套留在 core；两表实为 **public 无前缀**，不写 `quant.`）
- 不做实时分析（全部按 run 的离线计算）
- **不解析 job 错误串还原三值**：结果读取必须走 K9-correct 的读取面（`ARCHITECTURE.md` §9.1 F1 / R1），本仓不靠 `"backtest produced an invalid run: ..."` 这种字符串猜状态。
