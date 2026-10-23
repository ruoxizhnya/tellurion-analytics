package analysis

import (
	"math"
	"testing"
)

func TestBuildMetricsPayloadPartitionsFromResult(t *testing.T) {
	resp := &BacktestResponse{
		Status:       ResultCompleted,
		TotalReturn:  0.42,
		SharpeRatio:  9.99, // 必须原样透传，不得用补算值覆盖
		SortinoRatio: 8.88,
		MaxDrawdown:  -0.17,
		PortfolioValues: []PortfolioValue{
			{TotalValue: 100},
			{TotalValue: 110},
			{TotalValue: 99},
		},
	}
	payload, err := buildMetricsPayload(resp)
	if err != nil {
		t.Fatalf("buildMetricsPayload: %v", err)
	}
	if payload.FromResult.SharpeRatio != 9.99 || payload.FromResult.SortinoRatio != 8.88 {
		t.Fatalf("from_result overwritten: %+v", payload.FromResult)
	}
	if payload.FromResult.TotalReturn != 0.42 || payload.FromResult.MaxDrawdown != -0.17 {
		t.Fatalf("from_result mismatch: %+v", payload.FromResult)
	}
	if payload.MetricsVersion != MetricsVersion {
		t.Fatalf("metrics_version = %q", payload.MetricsVersion)
	}

	// returns = [+0.1, -0.1]（与 core 同口径）
	wantVol := math.Sqrt(0.02) * math.Sqrt(252)
	if math.Abs(payload.Computed.Volatility-wantVol) > 1e-12 {
		t.Fatalf("volatility = %v, want %v", payload.Computed.Volatility, wantVol)
	}
	// 排序 [-0.1, 0.1]，idx = int(2*0.05) = 0
	if payload.Computed.VaR95 != -0.1 {
		t.Fatalf("var_95 = %v, want -0.1", payload.Computed.VaR95)
	}
	if payload.Computed.CVaR95 != -0.1 {
		t.Fatalf("cvar_95 = %v, want -0.1", payload.Computed.CVaR95)
	}
}

func TestBuildMetricsPayloadFailsClosedOnMissingCurve(t *testing.T) {
	_, err := buildMetricsPayload(&BacktestResponse{Status: ResultCompleted})
	mustIncompleteMissing(t, err, "portfolio_values")

	_, err = buildMetricsPayload(&BacktestResponse{
		Status:          ResultCompleted,
		PortfolioValues: []PortfolioValue{{TotalValue: 0}, {TotalValue: 100}},
	})
	mustIncompleteMissing(t, err, "portfolio_returns")
}

func TestCalculateVaRAndCVaRIndexBoundary(t *testing.T) {
	// 20 个点，95% 分位 idx = int(20*0.05) = 1
	returns := make([]float64, 20)
	for i := range returns {
		returns[i] = float64(i) / 100 // 0, 0.01 ... 0.19
	}
	if got := calculateVaR(returns, 0.95); got != 0.01 {
		t.Fatalf("var_95 = %v, want 0.01", got)
	}
	if got := calculateCVaR(returns, 0.95); got != (0.0+0.01)/2 {
		t.Fatalf("cvar_95 = %v, want 0.005", got)
	}
}
