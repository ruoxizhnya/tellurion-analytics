---
status: active
last-verified: 2026-10-20
verified-by: 功能边界固化（三份契约：db / api / dataflow）
---

# Dataflow — tellurion-analytics

> 目的：把「数据从哪来、在谁手里变形、到哪去」写死。**任何跨仓数据交换都必须能在本图上找到一条边**；找不到 = 这段代码在偷偷建第二条路。

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

1. `POST /api/analysis/metrics {run_id}` → 拉结果 DTO → 算指标 → 落 `analysis.reports`
2. `POST /api/analysis/benchmark` → 取基准序列（data）→ 超额 / IR / 跟踪误差
3. `POST /api/analysis/montecarlo {run_id, n, seed}` → 建 job → 异步跑 → 落 `mc_runs`（含 seed）
4. `GET /api/analysis/reports/{id}` → 读报告；导出走 `/export`

## 3. 失败模式（每一处 fail-closed 都要能指名）

| 失败点 | 症状 | 兜底 |
|---|---|---|
| 缺 seed | 结果不可复现 | 400，拒算 |
| 上游结果不完整 | 指标算出来是错的 | 409，拒算 |
| 基准口径不同（全收益 vs 价格） | 超额失真 | 基准登记时钉住口径 + hash |
| 报告格式变更 | 历史报告打不开 | 保留旧读取器 |

## 4. 不变量（可被测试钉住的）

1. 同 `(run_id, params, seed)` → 同报告字节（确定性）。
2. 本仓不修改上游任何表（只读，可用事务权限验证）。
3. 任何报告都能追溯到 `run_id` + 口径版本。

## 5. 与契约文档的关系

- 表结构 → `docs/db-schema.md`
- 接口形状 → `docs/api-schema.md`
- 本文件只讲**流向与顺序**，不重复字段定义。
