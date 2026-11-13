---
service-card: 1
repo: tellurion-analytics
kind: service
lifecycle: production
owner: ruoxizhnya
impact: none

requires:
  - tellurion-kit
required-by: []
calls:
  - tellurion
  - tellurion-data
  - tellurion-orchestration
called-by: []

truth:
  readme: README.md
  agents: AGENTS.md
  architecture: docs/ARCHITECTURE.md
  api: docs/api-schema.md
  db: docs/db-schema.md
  dataflow: docs/dataflow.md
  runbook: docs/runbook.md
  tasks: docs/TASKS.md

status: evergreen
last-verified: 2026-11-11
verified-by: 2026-11-11 实读 README / AGENTS / ARCHITECTURE / api-schema / db-schema / dataflow / runbook / go.mod / deploy/compose.yaml，并在 core 全仓搜索确认无 ANALYTICS_URL 出边；端口、端点数量、表数量未写入
---

# 服务卡：tellurion-analytics

> **一句话**：只读分析服务——基准 / 归因 / 敏感性 / 蒙特卡洛；纯函数算完只写 `analysis.*`，不碰上游任何表。
>
> `service` · `production` · `none` · `ruoxizhnya`

## 1. 这是什么

它把「给定已落库的回测结果与行情，算出基准比较、归因分解、敏感性曲面、蒙特卡洛分布」做成一组可复现的只读接口：输入是数据集快照 hash 与 run 标识，计算是纯函数，输出落进自己的 `analysis.*`。它不发起交易、不产因子、不改上游。

**它拥有**：`analysis` schema 的 DDL、写权与唯一写者（`internal/analysis.PG`）；归因 / 敏感性的 DTO（R1/R2 owner 已裁在本仓）。
**它不拥有**：回测结果与行情（只读 core / data）、因子定义与值、编排状态、策略资产。

## 2. 它不做什么

- 不写上游任何 schema：core / data / risk / strategy 的表一律只读，「只读」限定的是上游，不是自己。
- 不持有跨请求状态：结果必须落 `analysis.*`，不留在内存。
- 不 import core 的实现包：与 core `pkg/backtest/metrics` 只对照公式，不共享代码。
- 不抓取行情：取数归 `tellurion-data`。
- 不签发 token：服务间 `service` 角色的签发与分发不在本仓，只保证「有 token 就能按策略表用」。
- 不重算上游已给的结果，也不替上游判 `status`：`backtest_jobs.result_status` 是唯一权威。

## 3. 与谁相连

### 调用它（入边）

| 来源 | 协议 | 用来做什么 | 本仓挂了会怎样 |
|---|---|---|---|
| 无（当前零调用方） | — | core 不调用本仓是 ADR-0005 的有意裁决（`SVC-AN-G3` 已关闭） | 主链路无影响；只有直接调用分析端点的人或脚本失败 |

> 本仓不是前端入口的后端：UI 只经 core 的 `/api`，不会直达这里。

### 它调用（出边）

| 目标 | 协议 | 用来做什么 | 目标挂了会怎样 |
|---|---|---|---|
| `tellurion` | HTTP | 读回测结果与持久化读面、读持仓 / 因子暴露 artifact | 对应分析请求 `503 dependency_unavailable`，不产出假结果 |
| `tellurion-data` | HTTP | 读基准行情与数据集快照 | 同上，fail-closed |
| `tellurion-orchestration` | HTTP | 提交 / 查询敏感性逐点回填的父作业 | 敏感性回填停在未完成态；报告计算本身不受影响 |
| PostgreSQL | PG | `analysis.*` 的 DDL、读与写 | 未配 DSN 时退到 Memory Store（仅本地）；配了但连不上则就绪失败 |

> 不写 URL、端口、超时。地址见 `deploy/compose.yaml` 与进程环境变量；接口形状见 `docs/api-schema.md`。

## 4. 运行时身份

- **常驻进程**：`cmd/analyticsservice`（唯一 HTTP 监听）；无独立 worker。
- **存活/就绪语义**：
  - `GET /healthz`：进程存活（规范名，代码 `healthzPath`；容器 `HEALTHCHECK` 消费）。
  - `GET /readyz`：PG 可达且迁移完成；未配置上游端口**不算**未就绪，而是对应端点运行期 `503 dependency_unavailable`。
- **鉴权**：有 `JWT_SECRET` 时全端点过策略表；无 secret + 回环开放，无 secret + 非回环**拒绝启动**。
- **部署真相**：`deploy/compose.yaml`、`Dockerfile`、`README.md` 快速开始。
- **端口**：不在此卡；见上述部署真相。

## 5. 数据与契约归属

| 维度 | 归属 | 真相源 |
|---|---|---|
| DB schema | `analysis` 的唯一写者与 DDL | `docs/db-schema.md` |
| 跨仓 DTO / 契约 | 本仓 DTO owner 在本仓（裁决 ③）；不 import contracts，也未引入 | `docs/api-schema.md` §6.4 |
| 对外 HTTP | 分析计算 + 结果读取 | `docs/api-schema.md` |

> **未裁事项**：core `pkg/backtest/metrics` 与本仓指标实现是两份公式；「core 运行时权威 + analytics 只读参考」这一口径**尚未写成 ADR 条款**，见 `tellurion-docs:docs/design/ddd-boundary-review-2026-11-11.md` C4/D4。本卡不替它拍板。

## 6. 坏了怎么办

- **影响面**：`none`——当前零调用方，主链路与前端不受影响；只影响直接调用分析端点的人 / 脚本，以及等待敏感性回填的编排父作业。
- **先确认（按顺序）**：
  1. `GET /healthz` 是否 200；不是则看进程日志与启动参数（尤其无 secret + 非回环的拒起）。
  2. `GET /readyz` 是否 200；503 时看 PG 连通与 `analysis.*` 迁移。
  3. 单端点 `503 dependency_unavailable` 时看对应上游 URL 是否注入、上游是否健康；按 runbook §6「上游排查」走。
- **升级路径**：`owner` → 跨仓争议 → core `docs/TASKS.md`
- **runbook**：`docs/runbook.md`（起停 §1、日志与迁移 §2~§4、上游排查 §6）。

## 7. 5 分钟后去哪

| 我想…… | 读这个 |
|---|---|
| 知道它承诺什么接口、错误码与鉴权档位 | `docs/api-schema.md` |
| 知道 `analysis.*` 有哪些表、谁能写 | `docs/db-schema.md` |
| 知道数据怎么流、哪里 fail-closed | `docs/dataflow.md` |
| 起停 / 排障 / 迁移重放 | `docs/runbook.md` |
| 当前在做什么 | `docs/TASKS.md` |
| 架构与决策（含 ADR-0005 定位裁决） | `docs/ARCHITECTURE.md` / `docs/adr/` |
