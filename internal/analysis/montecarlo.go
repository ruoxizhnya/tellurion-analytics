package analysis

import (
	"math"
	"math/rand"
	"sort"
)

// MaxMonteCarloN 是单次蒙特卡洛的抽样上限（超上限 ⇒ 400 invalid_params）。
const MaxMonteCarloN = 10000

// 蒙特卡洛方法（只接受这两个值；未知方法 ⇒ 400 invalid_params）。
const (
	MethodBootstrap = "bootstrap"
	MethodShuffle   = "shuffle"
)

// MonteCarloRequest 是 POST /api/analysis/montecarlo 的入参。
// Seed 是 *int64：缺省 / null ⇒ 400 seed_required；0 是合法的显式种子（F6）。
type MonteCarloRequest struct {
	RunID  string `json:"run_id"`
	N      int    `json:"n"`
	Seed   *int64 `json:"seed"`
	Method string `json:"method,omitempty"`
}

// MonteCarloPayload 是蒙特卡洛报告体（只放分布摘要，不放逐次样本）。
type MonteCarloPayload struct {
	N      int     `json:"n"`
	Seed   int64   `json:"seed"`
	Method string  `json:"method"`
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"std_dev"`
	P05    float64 `json:"p05"`
	P50    float64 `json:"p50"`
	P95    float64 `json:"p95"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// buildMonteCarloPayload 用显式种子重抽样。
//
// PRNG 冻结为 math/rand.New(rand.NewSource(seed))（与引擎同源；换算法 = breaking）。
// 不读系统时钟、不读系统随机源。
func buildMonteCarloPayload(resp *BacktestResponse, n int, seed int64, method string) (MonteCarloPayload, error) {
	returns := calculateReturns(resp.PortfolioValues)
	if len(returns) == 0 {
		return MonteCarloPayload{}, &IncompleteError{Missing: "portfolio_returns"}
	}

	rng := rand.New(rand.NewSource(seed))
	samples := make([]float64, 0, n)
	switch method {
	case MethodBootstrap:
		for i := 0; i < n; i++ {
			draw := make([]float64, len(returns))
			for j := range draw {
				draw[j] = returns[rng.Intn(len(returns))]
			}
			samples = append(samples, cumulativeReturn(draw))
		}
	case MethodShuffle:
		for i := 0; i < n; i++ {
			perm := rng.Perm(len(returns))
			draw := make([]float64, len(returns))
			for j, idx := range perm {
				draw[j] = returns[idx]
			}
			samples = append(samples, cumulativeReturn(draw))
		}
	default:
		return MonteCarloPayload{}, invalidf("unknown method %q", method)
	}

	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	return MonteCarloPayload{
		N:      n,
		Seed:   seed,
		Method: method,
		Mean:   mean(samples),
		StdDev: sampleStdDev(samples),
		P05:    percentile(sorted, 0.05),
		P50:    percentile(sorted, 0.50),
		P95:    percentile(sorted, 0.95),
		Min:    sorted[0],
		Max:    sorted[len(sorted)-1],
	}, nil
}

// percentile 取最近秩：idx = round(p × (n-1))，越界夹到两端。
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Round(p * float64(len(sorted)-1)))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
