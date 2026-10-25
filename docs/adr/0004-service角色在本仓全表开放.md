# 0004 · `service` 服务间角色在本仓全表开放

- **状态**：已裁决（来源：裁-17，2026-10-25；派发块 H6）
- **场景**：裁-17 定稿用**专用 `service` 角色**的长期 access token 做服务间调用，否决复用 `trader`：`trader` 同时能下实盘单与 emergency-flatten，把这张 token 放进任意服务的配置等于把下单权复制出去（core `tellurion:docs/TASKS.md` QT-71 ②）。跨仓腿由各服务仓自己改自己的策略表——risk 已落（`87c6aa9`：只开 `resolve` / `events` 两条）、data 已落（`33b68d1`：只开 `/datasets/resolve` 与 `/datasets/{hash}`）。**analytics 这一腿**：core 代机器调本仓时带 `service` 角色的 token，本仓策略表要认这个角色，否则服务间调用恒 403。
- **决策**：
  1. **`service` 与人角色正交，不是「比 admin 低一档」**：kit 的角色判定是 any-of 且**不做排序**，`service` 不是档位表上的一行，而是与 `viewer` / `trader` / `admin` 并列的另一种调用主体。因此它必须**与真人角色并列写进同一条 `Roles`**，不能替换它们——只留真人角色则服务间调用 403，只留 `service` 则真人调用方被拒（功能回退）。
  2. **本仓 8 条端点全部开 `service`**：本仓**没有任何「人负责的动作」端点**——8 条全是分析计算 / 报告读取，没有审批、留痕、配置或交易面（对比 risk 的 `/audit` + `/approve` + `/breakers`、core 的 `/api/execution/orders`）。裁-17 要防的风险是「机器凭证代劳人的责任动作」，本仓不存在这条风险面。角色口径：6 个 POST = `trader` / `admin` / `service`，2 个 GET = `viewer` / `trader` / `admin` / `service`。
  3. **新增档位不放松既有语义**：`viewer` 打 POST 仍 403（写口径没降级），`viewer` 读 GET 仍可读。加 `service` 不是把写端点变成「什么角色都能写」。
  4. **签发与分发不在本仓**：本仓只保证「**有 token 就能用**」——不造 token、不存 token、不分发 token、不建服务账号注册表。签发入口是 kit `v0.1.2` 的 `IssueServiceToken`，签发与注入属 core QT-71 / 部署清单（core `deploy/k8s/`）。
  5. **不因这个角色升 kit 版本**：`service` 只是普通 access token 上的一个角色串（kit 只按 `kind=access` + 角色 any-of 判定），本仓仍 pin `v0.1.1`（与 `tellurion-data` 的同一处置）。
- **理由**：为什么不是「只开某几条」——本仓没有像 risk 那样的「人负责的动作」端点可排除，逐条排除只会留下一条凭直觉挑出来的分界线（下次新增端点时没有可复用的判据）；开全表反而让规则可陈述、可守。全表开 `service` 也不扩大**能力面**：本仓本来就只读上游、只写自己的 `analysis.*`，`service` 拿到的权限与 `trader` 在本仓完全等价（都是「能算能读」），而 `trader` 在 core 那边能下单——这正是把 `service` 与 `trader` 分开的理由，分开后本仓这一侧反而没有「机器凭证能做的事比人多」的问题。
- **代价 / 后果**：
  1. **token 形状的已知跨仓缺口（不在本单）**：kit `auth/httpadapter.Authenticate`（`v0.1.1` ~ `v0.1.3` 三个版本都一样）要求 subject 可解析为 `int64`，而裁-17 的生产口径 `sub=service:<repo>` 是**非数字** subject ⇒ 会被 kit 的守卫判 401「malformed token subject」，**压根到不了本仓的角色判定**。⇒ 本仓的守卫用例证明的是**本仓策略表**（角色档位与作用域）正确，**不**证明生产 token 能走通；那条缺口归 kit（放宽 subject 校验）或改裁-17 的 subject 口径，**已报协调者**（与 `tellurion-risk` 的同型缺口同一处，risk 侧记在其 ADR-0006 ④）。core 侧的 `pkg/auth` 已经为 service token 开了特例（`RoleService` 分支不解析 user id），net/http 适配这一侧还没有。
  2. 本仓策略表从此承认第四种角色名，后续新增端点必须**显式决定**开不开 `service`——守卫用例 `TestPolicy_ServiceRoleDeclaredOnEveryRoute` 钉住「全表都开」，新增端点若忘了表态就红（逼着做一次决定，而不是默默不开）。
  3. 守卫用例用 kit `IssueTokens` 签**数字 subject** 的 token（`subject="1"`）来跑通角色判定，因为生产形状的 token 目前过不了 kit 的 subject 守卫（见上）。本仓**不**实现任何绕过 kit 守卫的旁路——那是 kit 的机制语义。
- **替代关系**：扩展 0003（鉴权接入：kit net/http 适配 + 本仓策略表）——机制（kit 适配 + 本仓策略表）不变，本条只定 `service` 角色的**适用范围**；0003 §3 的角色口径按本条补充。
