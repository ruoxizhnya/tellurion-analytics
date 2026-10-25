package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ruoxizhnya/tellurion-kit/auth"

	"tellurion-analytics/internal/analysis"
)

// serviceRole 是裁-17 定的服务间角色名（core 代机器调用本仓时用）。
//
// ⚠️ **token 形状的已知跨仓缺口（不在本单）**：本用例用 kit `IssueTokens` 签的数字
// subject token（`subject="1"`），因为 kit `auth/httpadapter.Authenticate`（`v0.1.1`
// ~ `v0.1.3` 三个版本都一样）要求 subject 可解析为 `int64`；而裁-17 的生产口径
// `sub=service:<repo>`（kit `v0.1.2` 的 `IssueServiceToken`）是**非数字** subject ⇒
// 会被 kit 的守卫判 401「malformed token subject」，压根到不了本仓的角色判定。
// ⇒ 本用例证明的是**本仓策略表**（角色档位与作用域）正确，**不**证明生产 token 能走通；
// 那条缺口归 kit（放宽 subject 校验）或改裁-17 的 subject 口径，**已报协调者**
// （与 `tellurion-risk` 的同型缺口同一处，risk 侧记在其 ADR-0006 ④）。
//
// 本仓**不因这个角色升 kit 版本**：service 只是普通 access token 上的一个角色串
// （kit 只按 kind=access + 角色 any-of 判定），与 `tellurion-data` 的同一处置一致。
const serviceRole = "service"

// issueServiceToken 签一个带 `service` 角色的 access token。
//
// username 记成 `service:core`（审计 actor 语义），subject 仍是 kit 要求的数字。
func issueServiceToken(t *testing.T, svc *auth.TokenService) string {
	t.Helper()
	tok, _, err := svc.IssueTokens(1, "service:core", serviceRole)
	if err != nil {
		t.Fatalf("issue service token: %v", err)
	}
	return tok
}

// TestPolicy_ServiceRoleScope 钉住裁-17：`service` 角色能调本仓的**每一条**端点，
// 且**不放松**既有语义（viewer 打 POST 仍 403、viewer 读 GET 仍可读）。
//
// 为什么本仓 8 条端点**全部**开 `service`（对比 risk 只开两条）：
// 本仓没有任何「人负责的动作」端点——8 条全是分析计算 / 报告读取，没有审批、留痕、
// 配置或交易面（risk 有 `/audit` + `/approve` + `/breakers`，core 有
// `/api/execution/orders`）。裁-17 要防的是「机器凭证代劳人的责任动作」，本仓不存在
// 这条风险面，所以服务间角色对全表开放。
//
// 「新增档位不放松既有语义」这半条也要钉：加 `service` 不是把写端点降级成「什么角色
// 都能写」——viewer 打 POST 仍必须是 403。
func TestPolicy_ServiceRoleScope(t *testing.T) {
	svc := auth.NewTokenService(auth.Config{JWTSecret: []byte("analytics-service-role-test")})
	if !svc.Enabled() {
		t.Fatal("token service must be enabled")
	}
	srv, _ := newTestServerWithAuthorizer(t, analysis.Options{
		Results: &stubReader{results: map[string]*analysis.BacktestResponse{"r1": sampleResult("r1")}},
	}, NewAuthorizer(svc))

	service := issueServiceToken(t, svc)
	viewer := analyticsToken(t, svc, "alice", "viewer")

	// service 调每一条端点 ⇒ **不是** 401 / 403。
	//
	// 端点各自的业务结果（200 / 202 / 400 / 404 / 409 / 503）取决于该端点的桩与上游，
	// 本用例只管**鉴权闸放行**这一件事，所以只断言「没被闸拦下」。
	for _, pattern := range New(nil).routePatterns() {
		method, path := splitPattern(pattern)
		body := `{"run_id":"r1"}`
		if method != http.MethodPost {
			body = "" // GET 无请求体；带一个空 JSON 只会徒增噪音。
		}
		status, resp := doRequest(t, srv, method, path, body, service)
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			t.Errorf("service 调 %s 被鉴权闸拦下：%d %s —— service 角色必须在策略表里", pattern, status, resp)
		}
	}

	// 新增档位不放松既有语义：viewer 打 POST 仍 403（写口径没降级）。
	for _, pattern := range New(nil).routePatterns() {
		method, path := splitPattern(pattern)
		if method != http.MethodPost {
			continue
		}
		status, body := doRequest(t, srv, method, path, `{"run_id":"r1"}`, viewer)
		wantCode(t, status, body, http.StatusForbidden, "forbidden")
	}
}

// TestPolicy_ServiceRoleCoexistsWithHumanRoles 钉住「any-of 且并列」的写法：
// `service` 必须与真人角色**同在一条 `Roles`**。
//
// 角色列表不做排序，所以「service 顶替 trader/admin」会让真人调用方直接 403
// （功能回退），「只留真人角色」则服务间调用 403。两条都要防。
func TestPolicy_ServiceRoleCoexistsWithHumanRoles(t *testing.T) {
	for route, rule := range PolicyEntries() {
		have := map[string]bool{}
		for _, role := range rule.Roles {
			have[role] = true
		}
		if !have[serviceRole] {
			t.Errorf("route %q 没声明 role %q —— 服务间调用会 403", route, serviceRole)
			continue
		}
		want := []string{"trader", "admin"}
		if strings.HasPrefix(route, "GET ") {
			want = append(want, "viewer")
		}
		for _, w := range want {
			if !have[w] {
				t.Errorf("route %q 的 Roles %v 缺真人角色 %q —— service 不能顶替真人角色", route, rule.Roles, w)
			}
		}
	}
}

// TestPolicy_ServiceRoleDeclaredOnEveryRoute 钉住 service 的**作用域**：本仓 8 条端点
// 全部声明 service。
//
// 这条与「将来新增端点」的关系：新端点若**不**开 service，本用例红 ⇒ 逼着做一次
// 显式表态（要么开、要么在表里注明为什么不开），而不是「忘了表态就默默不开」。
// 与 risk 的 `TestPolicy_ServiceRoleDeclaredExactlyTwice`（限定 service 只出现在两条
// 端点上）是同一条纪律的两面：一个钉「不许多开」，一个钉「不许漏开」。
func TestPolicy_ServiceRoleDeclaredOnEveryRoute(t *testing.T) {
	policy := PolicyEntries()
	if len(policy) != 8 {
		t.Fatalf("策略表有 %d 条端点，want 8 —— 端点数变了先改本用例", len(policy))
	}
	for route, rule := range policy {
		declared := false
		for _, role := range rule.Roles {
			if role == serviceRole {
				declared = true
				break
			}
		}
		if !declared {
			t.Errorf("route %q 的 Roles %v 没有 role %q", route, rule.Roles, serviceRole)
		}
	}
}
