package httpapi

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/ruoxizhnya/tellurion-kit/auth"
	kithttp "github.com/ruoxizhnya/tellurion-kit/auth/httpadapter"
)

// 鉴权闸的失败分类。实现只负责把 kit 的判定映射到这两个哨兵。
var (
	ErrUnauthenticated = errors.New("no valid credentials")
	ErrForbidden       = errors.New("insufficient role")
)

// Authorizer 是本仓所有端点的鉴权闸。nil = 闸未装配，端点必须拒绝服务。
//
// principal 是限流分桶的身份（用户名，其次 subject）；开放访问返回空串。
type Authorizer interface {
	Authorize(r *http.Request) (principal string, err error)
}

// WithAuthorizer 装配端点鉴权闸。
func WithAuthorizer(a Authorizer) Option {
	return func(s *Server) { s.authorizer = a }
}

// NewAuthorizer 用 kit 的 net/http 适配构造鉴权闸：token 解析 / kind=access /
// 角色 any-of 都在 kit，本仓只提供 policy 表（policy.go）。
// TokenService 未启用（无 secret + 回环的显式开放）时放行。
func NewAuthorizer(svc *auth.TokenService) Authorizer {
	return kitAuthorizer{svc: svc}
}

type kitAuthorizer struct {
	svc *auth.TokenService
}

func (a kitAuthorizer) Authorize(r *http.Request) (string, error) {
	if a.svc == nil || !a.svc.Enabled() {
		return "", nil
	}
	key := kithttp.RouteKey(r)
	rule, declared := analyticsPolicy.Rule(key)
	if !declared {
		// 路由表 ≡ 策略表由守卫测试钉住；运行时仍 fail-closed。
		return "", fmt.Errorf("%w: no authorization policy for %q", ErrForbidden, key)
	}
	claims, err := kithttp.Authenticate(a.svc, r)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	if err := kithttp.Authorize(claims, rule); err != nil {
		if errors.Is(err, kithttp.ErrForbidden) {
			return "", fmt.Errorf("%w: %v", ErrForbidden, err)
		}
		return "", fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	return principalOf(claims), nil
}

// principalOf 取限流身份：优先用户名，其次 subject（service token 的
// subject 形如 service:<repo>，没有用户名）。
func principalOf(claims *auth.Claims) string {
	if claims == nil {
		return ""
	}
	if name := strings.TrimSpace(claims.Username); name != "" {
		return name
	}
	return claims.Subject
}

// requireAuth 是 fail-closed 闸：未装配 Authorizer → 503；无凭证 / 坏 token → 401；
// 角色不足 → 403；通过鉴权后超限 → 429。只有 Authorize 返回 nil 才进入限流与 handler。
//
// 顺序固定为「鉴权 → 限流」：401/403 与 429 必须可区分，且无凭证的请求不消耗
// 任何身份桶的令牌（也不该被限流掩盖成 429）。
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authorizer == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody{
				Error: "endpoints require an authorizer; none is configured",
				Code:  "auth_unavailable",
			})
			return
		}
		principal, err := s.authorizer.Authorize(r)
		switch {
		case err == nil:
			if !s.limiter.allow(rateKey(principal), time.Now()) {
				w.Header().Set("Retry-After", "1")
				writeJSON(w, http.StatusTooManyRequests, errorBody{Error: "rate limit exceeded", Code: "rate_limited"})
				return
			}
			next.ServeHTTP(w, r)
		case errors.Is(err, ErrUnauthenticated):
			writeJSON(w, http.StatusUnauthorized, errorBody{Error: "authentication required", Code: "unauthenticated"})
		case errors.Is(err, ErrForbidden):
			writeJSON(w, http.StatusForbidden, errorBody{Error: "insufficient role", Code: "forbidden"})
		default:
			log.Printf("httpapi: authorizer error: %v", err)
			writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error", Code: "internal"})
		}
	})
}
