package httpapi

import kithttp "github.com/ruoxizhnya/tellurion-kit/auth/httpadapter"

// 本仓的「端点 × 最低角色」策略表（裁-15）。机制在 kit（token 解析 / kind=access /
// 角色 any-of），策略留本仓；新增 / 改名端点必须先改这里，再改路由表。
//
// 口径：读端点 viewer，写端点 trader。角色是 any-of，因此每档都要把更高角色显式
// 列进来（kit 不做角色排序）。
//
// service 档（裁-17）：`service` 是**服务间**角色（core 代机器调本仓时用），不是
// 「比 admin 低一档」的人角色——它与人角色**正交**，只在本仓「分析服务」这一条
// 服务间链路上开：本仓 8 条端点**全部**是分析计算 / 报告读取，**没有任何**审批、
// 留痕、配置或交易端点（对比 risk 的 `/audit` + `approve` + `breakers`、core 的
// `/api/execution/orders`），所以不存在「机器凭证代劳人的责任动作」这条风险。
//
// ⚠️ 角色 any-of ⇒ **service 必须与 viewer / trader / admin 并列写进同一条
// Roles**，不能替换掉它们：kit 不做角色排序，service 不是一个「更高 / 更低」的
// 位置。少了真人角色会让真人调用方被拒；少了 service 则服务间调用 403。
//
// service token 的**签发与分发不在本仓**（core QT-71 / kit KT-09）：本仓只保证
// 「有 token 就能用」，不造 token、不存 token、不分发 token。
var analyticsPolicy = kithttp.MustNewPolicy(map[string]kithttp.Rule{
	"POST /api/analysis/metrics":     {Roles: []string{"trader", "admin", "service"}},
	"POST /api/analysis/benchmark":   {Roles: []string{"trader", "admin", "service"}},
	"POST /api/analysis/attribution": {Roles: []string{"trader", "admin", "service"}},
	"POST /api/analysis/sensitivity": {Roles: []string{"trader", "admin", "service"}},
	"POST /api/analysis/montecarlo":  {Roles: []string{"trader", "admin", "service"}},
	"POST /api/analysis/correlation": {Roles: []string{"trader", "admin", "service"}},

	"GET /api/analysis/reports/{id}":        {Roles: []string{"viewer", "trader", "admin", "service"}},
	"GET /api/analysis/reports/{id}/export": {Roles: []string{"viewer", "trader", "admin", "service"}},
})

// PolicyEntries 返回策略表副本，供守卫测试与路由表双向对拍。
func PolicyEntries() map[string]kithttp.Rule { return analyticsPolicy.Entries() }
