package httpapi

import kithttp "github.com/ruoxizhnya/tellurion-kit/auth/httpadapter"

// 本仓的「端点 × 最低角色」策略表（裁-15）。机制在 kit（token 解析 / kind=access /
// 角色 any-of），策略留本仓；新增 / 改名端点必须先改这里，再改路由表。
//
// 口径：6 个 POST = trader，2 个 GET = viewer。角色是 any-of，因此每档都要把
// 更高角色显式列进来（kit 不做角色排序）。
var analyticsPolicy = kithttp.MustNewPolicy(map[string]kithttp.Rule{
	"POST /api/analysis/metrics":     {Roles: []string{"trader", "admin"}},
	"POST /api/analysis/benchmark":   {Roles: []string{"trader", "admin"}},
	"POST /api/analysis/attribution": {Roles: []string{"trader", "admin"}},
	"POST /api/analysis/sensitivity": {Roles: []string{"trader", "admin"}},
	"POST /api/analysis/montecarlo":  {Roles: []string{"trader", "admin"}},
	"POST /api/analysis/correlation": {Roles: []string{"trader", "admin"}},

	"GET /api/analysis/reports/{id}":        {Roles: []string{"viewer", "trader", "admin"}},
	"GET /api/analysis/reports/{id}/export": {Roles: []string{"viewer", "trader", "admin"}},
})

// PolicyEntries 返回策略表副本，供守卫测试与路由表双向对拍。
func PolicyEntries() map[string]kithttp.Rule { return analyticsPolicy.Entries() }
