package analysis

import (
	"math"
	"sort"
	"time"
)

// DefaultRollingWindow 是滚动超额对比的默认窗口（交易日）。
const DefaultRollingWindow = 20

// BenchmarkRequest 是 POST /api/analysis/benchmark 的入参。
type BenchmarkRequest struct {
	RunID         string `json:"run_id"`
	BenchmarkCode string `json:"benchmark_code"`
	Window        int    `json:"window,omitempty"`
}

// RollingPoint 是滚动窗口末端点的区间超额。
type RollingPoint struct {
	Date         time.Time `json:"date"`
	ExcessReturn float64   `json:"excess_return"`
}

// RollingExcess 是滚动超额序列。
type RollingExcess struct {
	Window int            `json:"window"`
	Series []RollingPoint `json:"series"`
}

// BenchmarkPayload 是基准对比报告体。
type BenchmarkPayload struct {
	Benchmark        BenchmarkRef  `json:"benchmark"`
	ExcessReturn     float64       `json:"excess_return"`
	TrackingError    float64       `json:"tracking_error"`
	InformationRatio float64       `json:"information_ratio"`
	Beta             float64       `json:"beta"`
	Rolling          RollingExcess `json:"rolling"`
}

// alignedObservation 是组合净值与基准值在同一天的对齐点。
type alignedObservation struct {
	date      time.Time
	portfolio float64
	benchmark float64
}

// alignSeries 按日期取交集（都按时间升序），基准值必须为正。
func alignSeries(pv []PortfolioValue, bench []BenchmarkPoint) []alignedObservation {
	byDate := make(map[time.Time]float64, len(bench))
	for _, p := range bench {
		byDate[p.Date.UTC()] = p.Value
	}
	out := make([]alignedObservation, 0, len(pv))
	for _, p := range pv {
		v, ok := byDate[p.Date.UTC()]
		if !ok || p.TotalValue <= 0 || v <= 0 {
			continue
		}
		out = append(out, alignedObservation{date: p.Date, portfolio: p.TotalValue, benchmark: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].date.Before(out[j].date) })
	return out
}

// buildBenchmarkPayload 计算超额 / 跟踪误差 / 信息比率 / Beta / 滚动超额。
func buildBenchmarkPayload(resp *BacktestResponse, series BenchmarkSeries, ref BenchmarkRef, window int) (BenchmarkPayload, error) {
	if series.Ref.AdjustmentBasis != "" && series.Ref.AdjustmentBasis != ref.AdjustmentBasis {
		return BenchmarkPayload{}, &BenchmarkBasisError{
			Registered: ref.AdjustmentBasis,
			Returned:   series.Ref.AdjustmentBasis,
		}
	}
	obs := alignSeries(resp.PortfolioValues, series.Points)
	if len(obs) < 2 {
		return BenchmarkPayload{}, &IncompleteError{Missing: "benchmark_series"}
	}
	if window <= 0 || window > len(obs)-1 {
		return BenchmarkPayload{}, invalidf("window must be in [1, %d]", len(obs)-1)
	}

	portfolioReturns := make([]float64, 0, len(obs)-1)
	benchmarkReturns := make([]float64, 0, len(obs)-1)
	active := make([]float64, 0, len(obs)-1)
	for i := 1; i < len(obs); i++ {
		rp := (obs[i].portfolio - obs[i-1].portfolio) / obs[i-1].portfolio
		rb := (obs[i].benchmark - obs[i-1].benchmark) / obs[i-1].benchmark
		portfolioReturns = append(portfolioReturns, rp)
		benchmarkReturns = append(benchmarkReturns, rb)
		active = append(active, rp-rb)
	}

	excess := cumulativeReturn(portfolioReturns) - cumulativeReturn(benchmarkReturns)
	activeStd := sampleStdDev(active)
	trackingError := activeStd * math.Sqrt(252)
	informationRatio := 0.0
	if activeStd != 0 {
		informationRatio = mean(active) / activeStd * math.Sqrt(252)
	}

	beta := 0.0
	if v := sampleStdDev(benchmarkReturns); v != 0 {
		beta = covariance(portfolioReturns, benchmarkReturns) / (v * v)
	}

	rolling := make([]RollingPoint, 0, len(active)-window+1)
	for i := window - 1; i < len(active); i++ {
		start := i - window + 1
		rolling = append(rolling, RollingPoint{
			Date:         obs[i+1].date,
			ExcessReturn: cumulativeReturn(portfolioReturns[start:i+1]) - cumulativeReturn(benchmarkReturns[start:i+1]),
		})
	}

	return BenchmarkPayload{
		Benchmark:        ref,
		ExcessReturn:     excess,
		TrackingError:    trackingError,
		InformationRatio: informationRatio,
		Beta:             beta,
		Rolling:          RollingExcess{Window: window, Series: rolling},
	}, nil
}

// BenchmarkBasisError 表示数据服务返回的基准口径与登记口径不一致。
type BenchmarkBasisError struct {
	Registered string
	Returned   string
}

func (e *BenchmarkBasisError) Error() string {
	return "benchmark data unavailable: adjustment_basis mismatch (registered " + e.Registered + ", got " + e.Returned + ")"
}

func (e *BenchmarkBasisError) Unwrap() error { return ErrBenchmarkDataUnavailable }

// covariance 是样本协方差（n-1）；少于 2 点为 0。
func covariance(x, y []float64) float64 {
	if len(x) != len(y) || len(x) < 2 {
		return 0
	}
	mx, my := mean(x), mean(y)
	sum := 0.0
	for i := range x {
		sum += (x[i] - mx) * (y[i] - my)
	}
	return sum / float64(len(x)-1)
}
