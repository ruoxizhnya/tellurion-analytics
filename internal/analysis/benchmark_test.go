package analysis

import (
	"errors"
	"math"
	"testing"
)

func TestBuildBenchmarkPayloadExcessAndBeta(t *testing.T) {
	pv := make([]PortfolioValue, 0, 5)
	bench := make([]BenchmarkPoint, 0, 5)
	p, b := 100.0, 100.0
	for i := 0; i < 5; i++ {
		pv = append(pv, PortfolioValue{Date: baseDate.AddDate(0, 0, i), TotalValue: p})
		bench = append(bench, BenchmarkPoint{Date: baseDate.AddDate(0, 0, i), Value: b})
		p *= 1.10
		b *= 1.05
	}
	resp := &BacktestResponse{Status: ResultCompleted, PortfolioValues: pv}
	ref := BenchmarkRef{Code: "CSI300", AdjustmentBasis: "total_return", DatasetHash: "h1"}
	series := BenchmarkSeries{Ref: ref, Points: bench}

	payload, err := buildBenchmarkPayload(resp, series, ref, 3)
	if err != nil {
		t.Fatalf("buildBenchmarkPayload: %v", err)
	}
	wantExcess := (math.Pow(1.10, 4) - 1) - (math.Pow(1.05, 4) - 1)
	if math.Abs(payload.ExcessReturn-wantExcess) > 1e-12 {
		t.Fatalf("excess = %v, want %v", payload.ExcessReturn, wantExcess)
	}
	// 主动收益恒为 0.05（浮点噪声量级）：跟踪误差必须是噪声级，不是真实波动
	if payload.TrackingError > 1e-12 {
		t.Fatalf("tracking error = %v, want ~0", payload.TrackingError)
	}
	if payload.Rolling.Window != 3 || len(payload.Rolling.Series) != 2 {
		t.Fatalf("rolling = %+v, want window 3 with 2 points", payload.Rolling)
	}
	wantRolling := (math.Pow(1.10, 3) - 1) - (math.Pow(1.05, 3) - 1)
	if math.Abs(payload.Rolling.Series[0].ExcessReturn-wantRolling) > 1e-12 {
		t.Fatalf("rolling excess = %v, want %v", payload.Rolling.Series[0].ExcessReturn, wantRolling)
	}
	if !payload.Rolling.Series[1].Date.Equal(baseDate.AddDate(0, 0, 4)) {
		t.Fatalf("rolling end date = %v", payload.Rolling.Series[1].Date)
	}
}

func TestBetaComputedFromReturns(t *testing.T) {
	// 组合收益恒为基准收益的 2 倍 ⇒ beta = 2
	portfolioReturns := []float64{0.02, 0.04, 0.01, 0.03, -0.01}
	benchmarkReturns := []float64{0.01, 0.02, 0.005, 0.015, -0.005}

	pv := []PortfolioValue{{Date: baseDate, TotalValue: 100}}
	bench := []BenchmarkPoint{{Date: baseDate, Value: 100}}
	p, b := 100.0, 100.0
	for i := range portfolioReturns {
		p *= 1 + portfolioReturns[i]
		b *= 1 + benchmarkReturns[i]
		pv = append(pv, PortfolioValue{Date: baseDate.AddDate(0, 0, i+1), TotalValue: p})
		bench = append(bench, BenchmarkPoint{Date: baseDate.AddDate(0, 0, i+1), Value: b})
	}
	ref := BenchmarkRef{Code: "CSI300", AdjustmentBasis: "total_return"}
	payload, err := buildBenchmarkPayload(
		&BacktestResponse{Status: ResultCompleted, PortfolioValues: pv},
		BenchmarkSeries{Ref: ref, Points: bench}, ref, 2,
	)
	if err != nil {
		t.Fatalf("buildBenchmarkPayload: %v", err)
	}
	if math.Abs(payload.Beta-2.0) > 1e-9 {
		t.Fatalf("beta = %v, want 2.0", payload.Beta)
	}
	if payload.TrackingError <= 0 || payload.InformationRatio <= 0 {
		t.Fatalf("tracking error / IR = %v / %v", payload.TrackingError, payload.InformationRatio)
	}
}

func TestBuildBenchmarkPayloadRejectsBasisMismatch(t *testing.T) {
	resp := &BacktestResponse{Status: ResultCompleted, PortfolioValues: []PortfolioValue{
		{Date: baseDate, TotalValue: 100},
		{Date: baseDate.AddDate(0, 0, 1), TotalValue: 101},
	}}
	ref := BenchmarkRef{Code: "CSI300", AdjustmentBasis: "total_return"}
	series := BenchmarkSeries{Ref: BenchmarkRef{AdjustmentBasis: "price"}}
	_, err := buildBenchmarkPayload(resp, series, ref, 1)
	if !errors.Is(err, ErrBenchmarkDataUnavailable) {
		t.Fatalf("want ErrBenchmarkDataUnavailable, got %v", err)
	}
}

func TestBuildBenchmarkPayloadValidatesWindowAndAlignment(t *testing.T) {
	pv := []PortfolioValue{
		{Date: baseDate, TotalValue: 100},
		{Date: baseDate.AddDate(0, 0, 1), TotalValue: 101},
	}
	bench := []BenchmarkPoint{
		{Date: baseDate, Value: 100},
		{Date: baseDate.AddDate(0, 0, 1), Value: 101},
	}
	resp := &BacktestResponse{Status: ResultCompleted, PortfolioValues: pv}
	ref := BenchmarkRef{Code: "CSI300", AdjustmentBasis: "total_return"}

	if _, err := buildBenchmarkPayload(resp, BenchmarkSeries{Ref: ref, Points: bench}, ref, 2); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("window overflow: want ErrInvalidParams, got %v", err)
	}
	// 只对齐到 1 个点 ⇒ 缺曲线
	_, err := buildBenchmarkPayload(resp, BenchmarkSeries{Ref: ref, Points: bench[:1]}, ref, 1)
	mustIncompleteMissing(t, err, "benchmark_series")
}

func TestAlignSeriesSkipsNonPositiveAndSorts(t *testing.T) {
	pv := []PortfolioValue{
		{Date: baseDate.AddDate(0, 0, 2), TotalValue: 102},
		{Date: baseDate, TotalValue: 100},
		{Date: baseDate.AddDate(0, 0, 1), TotalValue: 0},
	}
	bench := []BenchmarkPoint{
		{Date: baseDate.AddDate(0, 0, 2), Value: 102},
		{Date: baseDate, Value: 100},
		{Date: baseDate.AddDate(0, 0, 1), Value: 101},
	}
	obs := alignSeries(pv, bench)
	if len(obs) != 2 {
		t.Fatalf("aligned points = %d, want 2", len(obs))
	}
	if !obs[0].date.Equal(baseDate) || !obs[1].date.Equal(baseDate.AddDate(0, 0, 2)) {
		t.Fatalf("alignment order: %+v", obs)
	}
}

func TestServiceBenchmarkFailClosed(t *testing.T) {
	store := NewMemory()
	ref := BenchmarkRef{Code: "CSI300", AdjustmentBasis: "total_return", DatasetHash: "h1", RegisteredAt: baseDate}
	if err := store.RegisterBenchmark(t.Context(), ref); err != nil {
		t.Fatalf("RegisterBenchmark: %v", err)
	}
	svc := newTestService(t, Options{
		Store:      store,
		Results:    &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}},
		Benchmarks: &fakeBenchmarkReader{err: ErrBenchmarkDataUnavailable},
	})
	_, err := svc.Benchmark(t.Context(), BenchmarkRequest{RunID: "r1", BenchmarkCode: "CSI300"})
	if !errors.Is(err, ErrBenchmarkDataUnavailable) {
		t.Fatalf("want ErrBenchmarkDataUnavailable, got %v", err)
	}

	// 未登记基准 ⇒ 404 哨兵，不自动抓数据
	_, err = svc.Benchmark(t.Context(), BenchmarkRequest{RunID: "r1", BenchmarkCode: "SPX"})
	if !errors.Is(err, ErrBenchmarkNotRegistered) {
		t.Fatalf("want ErrBenchmarkNotRegistered, got %v", err)
	}
}
