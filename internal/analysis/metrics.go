package analysis

import (
	"math"
	"sort"
)

// MetricsPayload 是 POST /api/analysis/metrics 的报告体（F5 分区）。
//
// FromResult 原样搬运上游已算的指标（权威值，**不得**重算 / 覆盖）；
// Computed 只补算上游没有的收益类风险指标。Beta 需要基准序列，metrics 端点
// 没有基准输入，故 Beta 归 benchmark 报告（docs/ARCHITECTURE.md §9.4）。
type MetricsPayload struct {
	MetricsVersion string          `json:"metrics_version"`
	FromResult     ResultMetrics   `json:"from_result"`
	Computed       ComputedMetrics `json:"computed"`
	Sources        MetricsSources  `json:"sources"`
}

// ResultMetrics 是上游结果里已有的指标（原样）。
type ResultMetrics struct {
	TotalReturn     float64 `json:"total_return"`
	AnnualReturn    float64 `json:"annual_return"`
	SharpeRatio     float64 `json:"sharpe_ratio"`
	SortinoRatio    float64 `json:"sortino_ratio"`
	MaxDrawdown     float64 `json:"max_drawdown"`
	MaxDrawdownDate string  `json:"max_drawdown_date,omitempty"`
	WinRate         float64 `json:"win_rate"`
	TotalTrades     int     `json:"total_trades"`
	WinTrades       int     `json:"win_trades"`
	LoseTrades      int     `json:"lose_trades"`
	AvgHoldingDays  float64 `json:"avg_holding_days"`
	CalmarRatio     float64 `json:"calmar_ratio"`
}

// ComputedMetrics 是本仓补算的指标。
type ComputedMetrics struct {
	Volatility float64 `json:"volatility"`
	VaR95      float64 `json:"var_95"`
	CVaR95     float64 `json:"cvar_95"`
}

// MetricsSources 标注每个分区的数据来源（F5：缺失才补算，并标注来源）。
type MetricsSources struct {
	FromResult string   `json:"from_result"`
	Computed   []string `json:"computed"`
}

// calculateReturns 与 core 同口径：逐点 (curr-prev)/prev，prev<=0 时跳过该点。
// 回测是封闭系统（无申赎），不做现金流修正。
func calculateReturns(values []PortfolioValue) []float64 {
	if len(values) < 2 {
		return nil
	}
	out := make([]float64, 0, len(values)-1)
	for i := 1; i < len(values); i++ {
		prev := values[i-1].TotalValue
		if prev <= 0 {
			continue
		}
		out = append(out, (values[i].TotalValue-prev)/prev)
	}
	return out
}

// buildMetricsPayload 只补算 volatility / VaR95 / CVaR95。
//
// 空 / 不足的输入由调用方在计算前拦住（409 run_result_incomplete）；
// 这里再兜一层，防止把零值当有效结果写进报告。
func buildMetricsPayload(resp *BacktestResponse) (MetricsPayload, error) {
	if len(resp.PortfolioValues) < 2 {
		return MetricsPayload{}, &IncompleteError{Missing: "portfolio_values"}
	}
	returns := calculateReturns(resp.PortfolioValues)
	if len(returns) == 0 {
		return MetricsPayload{}, &IncompleteError{Missing: "portfolio_returns"}
	}
	computed := ComputedMetrics{
		Volatility: calculateVolatility(returns),
		VaR95:      calculateVaR(returns, 0.95),
		CVaR95:     calculateCVaR(returns, 0.95),
	}
	return MetricsPayload{
		MetricsVersion: MetricsVersion,
		FromResult: ResultMetrics{
			TotalReturn:     resp.TotalReturn,
			AnnualReturn:    resp.AnnualReturn,
			SharpeRatio:     resp.SharpeRatio,
			SortinoRatio:    resp.SortinoRatio,
			MaxDrawdown:     resp.MaxDrawdown,
			MaxDrawdownDate: resp.MaxDrawdownDate,
			WinRate:         resp.WinRate,
			TotalTrades:     resp.TotalTrades,
			WinTrades:       resp.WinTrades,
			LoseTrades:      resp.LoseTrades,
			AvgHoldingDays:  resp.AvgHoldingDays,
			CalmarRatio:     resp.CalmarRatio,
		},
		Computed: computed,
		Sources: MetricsSources{
			FromResult: "run_result",
			Computed:   []string{"volatility", "var_95", "cvar_95"},
		},
	}, nil
}

// calculateVolatility 年化波动率 = 样本标准差 × sqrt(252)（与 core 同口径）。
func calculateVolatility(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	return sampleStdDev(returns) * math.Sqrt(252)
}

// calculateVaR 取排序后 int(n*(1-confidence)) 位（与 core 同口径）。
func calculateVaR(returns []float64, confidence float64) float64 {
	if len(returns) == 0 {
		return 0
	}
	sorted := append([]float64(nil), returns...)
	sort.Float64s(sorted)
	idx := int(float64(len(sorted)) * (1 - confidence))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// calculateCVaR 是 VaR 分位内（含 idx）的均值（与 core 同口径）。
func calculateCVaR(returns []float64, confidence float64) float64 {
	if len(returns) == 0 {
		return 0
	}
	sorted := append([]float64(nil), returns...)
	sort.Float64s(sorted)
	idx := int(float64(len(sorted)) * (1 - confidence))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	if idx == 0 {
		return sorted[0]
	}
	sum := 0.0
	for i := 0; i <= idx; i++ {
		sum += sorted[i]
	}
	return sum / float64(idx+1)
}
