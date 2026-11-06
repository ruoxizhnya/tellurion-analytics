package httpapi

import (
	"sync"
	"time"
)

// 限流口径（docs/TASKS.md SVC-AN-G1 ⑥）：按身份令牌桶，默认 50 rps / burst 200。
//
// 量级是「不打挂自己」：分析端点单次成本 = 读上游 + 纯函数计算，本仓不承诺
// 更大的容量。超限回 429 + Retry-After，与 401/403 严格分开——鉴权先于限流，
// 无凭证恒 401，不会被限流掩盖成 429。
const (
	defaultRatePerSecond = 50
	defaultRateBurst     = 200
)

type rateBucket struct {
	tokens float64
	last   time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*rateBucket
}

func newRateLimiter(perSecond, burst int) *rateLimiter {
	if perSecond <= 0 {
		perSecond = defaultRatePerSecond
	}
	if burst <= 0 {
		burst = defaultRateBurst
	}
	return &rateLimiter{
		rate:    float64(perSecond),
		burst:   float64(burst),
		buckets: make(map[string]*rateBucket),
	}
}

// allow 取一个令牌；桶按首次访问惰性创建。
func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &rateBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// rateKey 按身份限流；开放访问（无鉴权）把全部调用方归到同一个桶。
func rateKey(principal string) string {
	if principal == "" {
		return "open-access"
	}
	return principal
}
