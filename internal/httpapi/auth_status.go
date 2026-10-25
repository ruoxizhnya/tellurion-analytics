package httpapi

// AuthStatus 是本服务鉴权现状的唯一代码登记（docs/api-schema.md §7）。
//
// "enforced" = 裁-15 已接线：Server 的每个端点都过 requireAuth（kit auth/httpadapter），
// 未装配 Authorizer → 503，无凭证 → 401，角色不足 → 403；策略表见 policy.go。
// 改鉴权必须同时改本常量、重写 TestAuthEnforcedCurrentState，并同步 docs/api-schema.md §7
// 与 docs/TASKS.md——不要只改代码。
const AuthStatus = "enforced"
