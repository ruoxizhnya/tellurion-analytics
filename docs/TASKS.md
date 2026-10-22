---
status: active
last-verified: 2026-10-22
verified-by: M8 接口对表落盘（QT-43 前置 ② 完成）
---

# TASKS — tellurion-analytics 未完成项

> 本仓**唯一**任务台账。跨仓任务只在 §二 留指针，**不镜像正文**（双写必然漂移）。
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `✅ 完成（附日期）`
> 每张单六字段（① 目标 ② 上下文 ③ 要求 ④ 约束与非目标 ⑤ 验收 ⑥ 边界），判据见 `tellurion:AGENTS.md` §8.5。
> 完成即标 ✅（附日期）并移入 `docs/archive/TASKS-YYYY-MM-DD-archived.md`；本文件只留未完成项。

---

## 一、本仓任务

> **约定变更（2026-10-21）**：原「占位仓不写正式六字段任务单」作废——X 线任务单已展开为本仓正式任务（见下）。M5/M8 开工前置不变；实现仍未开工，开工前仍须过前置两条。

### QT-43 · X4 分析服务（基准 / 归因 / 敏感性 / 蒙特卡洛）[P1] ⬜

> **迁入说明（2026-10-21）**：本单自 core 仓 `tellurion:docs/TASKS.md`「五、X 线」迁入（任务台账批量重分布：X 线单展开到各服务仓为正式六字段单，「占位仓不写正式任务单」旧约定同时作废）。六字段正文逐字保留、不改写；core 侧同号条目已改为指针。
>
> **M8 前置完成（2026-10-22）**：接口对表见 [`ARCHITECTURE.md`](ARCHITECTURE.md) §9。开工前先读 §9.1 的六条前置事实——结果读取面无 K9-correct 持久读路径（F1）、Brinson / 因子归因缺 holdings / 暴露输入（F2）、sensitivity 依赖 QT-44（F3）、基准面依赖 X1b（F4）、指标不得重算（F5）、`seed=0` 不等于 time-based（F6）。另：② 里「benchmark/attribution 仅一行注释」经实测订正——设计引的那行其实是 cache warming 注释，BE-05/BE-06 生产代码 **0 命中**（§9.1「零实现核对」）。本单**仍 ⬜**：对表是开工前置，不是实现；R1 / R2 / R3 未落地前 X4a / X4b / X4c 各自阻塞。
> 对表自检（2026-10-22）：35 条 core `path:line` 按 HEAD `075fdcb` 逐条命中；C 组 8 端点 / 13 错误码与 `api-schema.md` 双向一致；破坏验证三腿（改错行号 / 删一行端点 / 删一个错误码）各自红，还原后绿。

- **① 目标**：补齐 BE-05/BE-06 的真实缺口，且分析只读 run 结果、零 import 引擎。
- **② 上下文**：`analytics-service-design.md`（D21）；回测族 grep benchmark/attribution 仅一行注释；MC/bootstrap 全无；`mc_runs.seed` 必填。
- **③ 要求**：① 搬现有 metrics/compare/correlation/export；② 新增基准对比 + Brinson/因子归因；③ 参数敏感性（网格）+ 蒙特卡洛/bootstrap（seed 留痕）；④ 只按 `run_id` 拉结果；⑤ `analysis.*` schema 唯一 owner。
- **④ 约束与非目标**：纯函数 + 只读；不 import `pkg/backtest`；不在分析层做交易决策。
- **⑤ 验收**：同一 run + 同一 seed 两次分析结果一致；归因分项和 ≈ 总收益（容差写明）；缺 seed 直接拒绝。
- **⑥ 边界**：只做分析与指标；报表呈现归 UI 仓。


---

> **开工前置（refactor-plan-review M5/M8，2026-10-20 登记；2026-10-22 更新执行状态）**：本表任何单开工前，必须先过 core `tellurion:docs/TASKS.md`「五、X 线」的**公共前置**两条：
> ① **M5 已结案（2026-10-21）** → `tellurion:docs/design/qt-decoupling/repo-inventory.md` §1.2：**六仓 ≠ 六进程**，本仓今天不升格容器；X4a 的纯函数迁移不需要容器，C 组 HTTP 面是升格时的目标形状。
> ② **M8 接口对表已落（2026-10-22）** → 本仓 [`ARCHITECTURE.md`](ARCHITECTURE.md) §9（模板 = `tellurion:docs/design/qt-decoupling/data-service-contract-and-ownership.md` §4.1；**本行原指的 `target-module-topology.md` UnifiedDataPoint 节是幻影引用，已订正**）。
> 正文以 core 彼处为准，此处只留指针、不镜像。

## 施工序（候选单，开工前必须展开成六字段）

> 这张表是**施工顺序**，不是任务台账：每条在执行前要展开成六字段单（目标/上下文/要求/约束/验收/边界），并写明冻结契约的路径与节号。
> 顺序有依赖，**不要跳级**：跳序的代价是返工（例：X1a 的协议没冻结就写 aggregator，等于把协议写死在一个服务里）。
> 2026-10-21：候选单已展开为「一、本仓任务」的正式六字段条目（表中「上游单号」指向的 QT 单正文现在就在本仓）。

| 阶段 | 内容 | 上游单号 | 前置 |
|---|---|---|---|
| X4a | metrics + benchmark（同步，只读上游） | `QT-43` | 本仓立项 |
| X4b | attribution（Brinson / 因子） | `QT-43` | X4a |
| X4c | sensitivity + montecarlo（异步 job，`seed` 必填） | `QT-43` | X4a |
| X4d | 报告导出（html / csv；**pdf 未定**，见 `ARCHITECTURE.md` §9.7 R5） | `QT-43` | X4b |

## 二、跨仓指针（不在本仓，勿双写）

| 单号 | 主题 | 在哪 |
|---|---|---|
| QT-43 | X4 分析服务（基准 / 归因 / 敏感性 / 蒙特卡洛） | 已迁入本仓「一、本仓任务」（2026-10-21；core `tellurion:docs/TASKS.md` 留同号指针） |
| QT-08 | 本仓 CI / AGENTS / 台账由该单一并补齐 | `tellurion:docs/TASKS.md` |

---

## 维护本文件

- **新增单**：写全六字段；六字段里必须显式引用**冻结契约的路径与节号**（契约仓的包路径、设计文档的节号），不许写"参考相关实现"。
- **派工前先证伪前提**：读码核对工单写的前提；不符就停下回报 + 给拆法，不硬做、不扩范围。
- **跨仓引用写 `repo:path`**（如 `tellurion:docs/TASKS.md`），不做可点击相对链接。
- **顺序纪律**：同一波次内撞同一批文件的单按单号串行，一单一 commit。
