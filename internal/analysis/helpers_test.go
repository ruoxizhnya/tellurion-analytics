package analysis

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeReader struct {
	results map[string]*BacktestResponse
	errs    map[string]error
}

func (f *fakeReader) GetResult(_ context.Context, runID string) (*BacktestResponse, error) {
	if err, ok := f.errs[runID]; ok && err != nil {
		return nil, err
	}
	if resp, ok := f.results[runID]; ok {
		return resp, nil
	}
	return nil, ErrRunNotFound
}

type fakeBenchmarkReader struct {
	series BenchmarkSeries
	err    error
}

func (f *fakeBenchmarkReader) GetSeries(_ context.Context, _, _ string, _, _ time.Time) (BenchmarkSeries, error) {
	if f.err != nil {
		return BenchmarkSeries{}, f.err
	}
	return f.series, nil
}

type fakeHoldings struct {
	snapshot PortfolioSnapshot
	err      error
}

func (f *fakeHoldings) GetPortfolioSnapshot(_ context.Context, _ string) (PortfolioSnapshot, error) {
	if f.err != nil {
		return PortfolioSnapshot{}, f.err
	}
	return f.snapshot, nil
}

type fakeFactors struct {
	exposures FactorExposures
	err       error
}

func (f *fakeFactors) GetFactorExposures(_ context.Context, _ string) (FactorExposures, error) {
	if f.err != nil {
		return FactorExposures{}, f.err
	}
	return f.exposures, nil
}

type fakeQueue struct {
	submitted []SensitivityJob
	err       error
	progress  SensitivityProgress
	statusErr error
}

func (f *fakeQueue) Submit(_ context.Context, job SensitivityJob) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.submitted = append(f.submitted, job)
	return "job-1", nil
}

func (f *fakeQueue) Status(_ context.Context, _ string) (SensitivityProgress, error) {
	if f.statusErr != nil {
		return SensitivityProgress{}, f.statusErr
	}
	return f.progress, nil
}

var baseDate = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

// testResult 构造 n 个净值点的有效结果（收益交替，避免退化方差）。
func testResult(id string, n int) *BacktestResponse {
	values := make([]PortfolioValue, 0, n)
	value := 100.0
	for i := 0; i < n; i++ {
		values = append(values, PortfolioValue{Date: baseDate.AddDate(0, 0, i), TotalValue: value})
		if i%2 == 0 {
			value *= 1.01
		} else {
			value *= 0.995
		}
	}
	return &BacktestResponse{
		ID:              id,
		Status:          ResultCompleted,
		TotalReturn:     0.10,
		AnnualReturn:    0.20,
		SharpeRatio:     1.234,
		SortinoRatio:    1.567,
		MaxDrawdown:     -0.08,
		MaxDrawdownDate: "2026-02-01",
		WinRate:         0.5,
		TotalTrades:     10,
		WinTrades:       5,
		LoseTrades:      5,
		AvgHoldingDays:  3.5,
		CalmarRatio:     2.5,
		PortfolioValues: values,
	}
}

func newTestService(t *testing.T, opts Options) *Service {
	t.Helper()
	if opts.Store == nil {
		opts.Store = NewMemory()
	}
	opts.Now = func() time.Time { return baseDate }
	svc, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

func mustIncompleteMissing(t *testing.T, err error, want string) {
	t.Helper()
	var incomplete *IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("want *IncompleteError, got %v", err)
	}
	if incomplete.Missing != want {
		t.Fatalf("missing = %q, want %q", incomplete.Missing, want)
	}
}
