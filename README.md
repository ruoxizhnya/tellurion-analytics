# tellurion-analytics

> module: `tellurion-analytics` ｜ 状态：**预留（触发式）**——当前为占位仓，**未启动**。触发条件同 X 线通则；启动前先立单。

分析服务：归因、基准对比、绩效指标、报表。来源为 core 的 `cmd/analysis`（已有 `main`，:8085）+ 归因/基准纯函数。

归因/基准的纯函数目前散在 core，**具体位置待核实**（`pkg/analytics` 不存在）——搬之前先做一次盘点，不凭包名臆造。

## 边界

**可以**
- 持有 `analysis` schema 的 DDL
- 做重计算（归因、滚动指标）——这类天然适合离线/批量

**不可以**
- 在回测主循环里同步调用（会拖慢每一根 bar）
- 复制 core 的指标实现（同一指标只允许一份定义）

## 依赖方向

```
tellurion-analytics
```

- 契约与工具一律以 **tag** 依赖（`github.com/ruoxizhnya/tellurion-contracts` / `-kit`），
  **禁止 `replace` 指向仓外路径**（单仓不可构建）。
- `go.work` 一律不入库。

## 施工入口

本仓所有任务登记在 **`tellurion` 仓的 `docs/TASKS.md`**（单一台账，避免双写）。
在台账出现本仓的单号之前，**不要在此写实现**。

## 权威文档

| 主题 | 位置 |
|---|---|
| 仓清单与命名 | `tellurion:docs/design/qt-decoupling/repo-inventory.md` |
| 分仓拓扑与阶梯 | `tellurion:docs/design/qt-decoupling/repo-topology-and-naming.md` |
| P0 修复基线 | `tellurion:docs/design/qt-decoupling/p0-round2-review.md` |
