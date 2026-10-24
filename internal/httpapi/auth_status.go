package httpapi

// AuthStatus 是本服务鉴权现状的唯一代码登记（docs/api-schema.md §7）。
//
// "unwired" = QT-50 ④ / R6 未落：Server 不装鉴权中间件、不读 Authorization、
// 不产出 401 / 403；安全边界依赖部署侧只监听回环（cmd/analyticsservice 默认
// 127.0.0.1:8087）。接线鉴权时改本常量、重写 TestAuthNotWiredCurrentState，
// 并同步 docs/api-schema.md §7 与 docs/TASKS.md——不要只改代码。
const AuthStatus = "unwired"
