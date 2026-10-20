---
status: active
last-verified: 2026-10-20
verified-by: 功能边界固化（三份契约：db / api / dataflow）
---

# 架构 — tellurion-analytics

## 1. 定位

分析服务：基准 / 归因 / 敏感性 / 蒙特卡洛。**纯函数、只读上游**（不写别人的表，只写自己的 `analysis.*`）。

它出仓的理由不是"计算重"（core 也能算），而是这些分析**互相独立、可并行、结果可缓存**，且天然是"给报告用的"，不进入决策热路径。

## 2. 分层

```
上游只读输入（结果 / 基准 / 因子值）
        ↓
   计算层（纯函数 + 显式 seed）
        ├── 同步：metrics · benchmark · attribution · correlation
        └── 异步：sensitivity · montecarlo（job）
        ↓
   analysis.reports / _benchmarks / _sensitivity_runs / _mc_runs / _attribution
```

## 3. 组件职责

| 组件 | 职责 | 不做 |
|---|---|---|
| 指标 | 绩效指标（补齐收益类） | 不改上游已算的值 |
| 基准 | 超额 / IR / 跟踪误差 | 不抓行情 |
| 归因 | Brinson / 因子归因 | 不做因子研究 |
| 敏感性 / 蒙特卡洛 | 网格与分布（异步、seed 显式） | 不读系统时钟做随机 |

## 4. 三份契约（功能的边界就在这三份文件里）

| 维度 | 文件 | 回答什么问题 |
|---|---|---|
| 数据形状 | [`db-schema.md`](db-schema.md) | 本仓拥有哪些表？谁能写？ |
| 接口形状 | [`api-schema.md`](api-schema.md) | 对外暴露什么？错误码与幂等怎么做？ |
| 流向与顺序 | [`dataflow.md`](dataflow.md) | 数据怎么流？哪里 fail-closed？ |

## 5. 依赖方向

```
contracts（结果 DTO）+ kit（logging/http/db/statistics）
        ↑
tellurion-analytics ──(只读)──► core / run（结果）
        └──(只读)──► tellurion-data（基准、因子值）
```

## 6. 关键决策

- [`adr/0001-纯函数只读.md`](adr/0001-纯函数只读.md)

## 7. 非目标（明确不做）

- 不做数据抓取
- 不做回测
- 不做因子研究
- 不做实时分析

## 8. 分期与状态

| 阶段 | 内容 | 状态 |
|---|---|---|
| X4a | metrics + benchmark（同步） | ⬜ |
| X4b | attribution（Brinson / 因子） | ⬜ |
| X4c | sensitivity + montecarlo（异步 + seed） | ⬜ |
| X4d | 报告导出（html / csv / pdf） | ⬜ |
