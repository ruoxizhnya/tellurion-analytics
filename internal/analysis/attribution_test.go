package analysis

import (
	"errors"
	"math"
	"testing"
)

func TestBuildFactorAttributionIdentity(t *testing.T) {
	resp := &BacktestResponse{Status: ResultCompleted, TotalReturn: 0.10}
	payload, err := buildFactorAttribution(resp, FactorExposures{
		Factors: []FactorExposure{
			{Name: "value", Exposure: 0.5, FactorReturn: 0.10},
			{Name: "momentum", Exposure: 0.2, FactorReturn: 0.10},
		},
		DatasetHash: "f1",
	})
	if err != nil {
		t.Fatalf("buildFactorAttribution: %v", err)
	}
	sum := 0.0
	for _, c := range payload.Contributions {
		sum += c.Contribution
	}
	if math.Abs(sum+payload.Residual-payload.TotalReturn) > 1e-12 {
		t.Fatalf("Σcontributions + residual != total_return: %v + %v != %v",
			sum, payload.Residual, payload.TotalReturn)
	}
	if math.Abs(payload.Residual-0.03) > 1e-12 {
		t.Fatalf("residual = %v, want 0.03", payload.Residual)
	}
	// 按名字排序，输出确定
	if payload.Contributions[0].Name != "momentum" || payload.Contributions[1].Name != "value" {
		t.Fatalf("contributions not sorted: %+v", payload.Contributions)
	}
}

func TestBuildBrinsonAttributionIdentity(t *testing.T) {
	resp := &BacktestResponse{Status: ResultCompleted, TotalReturn: 0.09}
	snapshot := PortfolioSnapshot{
		Holdings: []Holding{
			{Symbol: "A", Sector: "tech", Weight: 0.5, PeriodReturn: 0.12},
			{Symbol: "B", Sector: "cons", Weight: 0.5, PeriodReturn: 0.06},
		},
		BenchmarkSectorWeights: map[string]float64{"tech": 0.6, "cons": 0.4},
		BenchmarkSectorReturns: map[string]float64{"tech": 0.10, "cons": 0.04},
		BenchmarkReturn:        0.076,
		DatasetHash:            "h1",
	}
	payload, err := buildBrinsonAttribution(resp, snapshot)
	if err != nil {
		t.Fatalf("buildBrinsonAttribution: %v", err)
	}
	sum := 0.0
	for _, c := range payload.Contributions {
		sum += c.Allocation + c.Selection + c.Interaction
	}
	// 恒等式（容差 1e-8，docs/ARCHITECTURE.md §9.4）
	if math.Abs(sum-(payload.TotalReturn-*payload.BenchmarkReturn)) > 1e-8 {
		t.Fatalf("brinson identity broken: %v != %v", sum, payload.TotalReturn-*payload.BenchmarkReturn)
	}
	if math.Abs(payload.Residual) > 1e-12 {
		t.Fatalf("residual = %v, want 0", payload.Residual)
	}

	tech := payload.Contributions[0]
	if tech.Name != "cons" { // 行业按字典序：cons < tech
		t.Fatalf("sector order: %+v", payload.Contributions)
	}
	tech = payload.Contributions[1]
	wantAlloc := (0.5 - 0.6) * (0.10 - 0.076)
	wantSelect := 0.6 * (0.12 - 0.10)
	wantInteract := (0.5 - 0.6) * (0.12 - 0.10)
	if math.Abs(tech.Allocation-wantAlloc) > 1e-12 ||
		math.Abs(tech.Selection-wantSelect) > 1e-12 ||
		math.Abs(tech.Interaction-wantInteract) > 1e-12 {
		t.Fatalf("tech components = %+v", tech)
	}
}

func TestAttributionMissingInputNamesWhatIsMissing(t *testing.T) {
	resp := &BacktestResponse{Status: ResultCompleted, TotalReturn: 0.1}
	_, err := buildBrinsonAttribution(resp, PortfolioSnapshot{})
	mustIncompleteMissing(t, err, "holdings")

	_, err = buildFactorAttribution(resp, FactorExposures{})
	mustIncompleteMissing(t, err, "factor_exposures")
}

func TestServiceAttributionFailureModes(t *testing.T) {
	reader := &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}}

	// 方法不支持
	svc := newTestService(t, Options{Results: reader, Holdings: &fakeHoldings{}, Factors: &fakeFactors{}})
	_, err := svc.Attribution(t.Context(), AttributionRequest{RunID: "r1", Method: "ic"})
	if !errors.Is(err, ErrMethodNotSupported) {
		t.Fatalf("want ErrMethodNotSupported, got %v", err)
	}

	// provider 未接线 ⇒ 503 哨兵，而不是 incompleteness
	svc = newTestService(t, Options{Results: reader})
	_, err = svc.Attribution(t.Context(), AttributionRequest{RunID: "r1", Method: "brinson"})
	if !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("want ErrDependencyUnavailable, got %v", err)
	}

	// provider 返回缺输入 ⇒ 409 且点名
	svc = newTestService(t, Options{Results: reader, Holdings: &fakeHoldings{err: &IncompleteError{Missing: "holdings"}}})
	_, err = svc.Attribution(t.Context(), AttributionRequest{RunID: "r1", Method: "brinson"})
	mustIncompleteMissing(t, err, "holdings")
}

func TestServiceAttributionPersistsResidual(t *testing.T) {
	store := NewMemory()
	svc := newTestService(t, Options{
		Store:   store,
		Results: &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}},
		Factors: &fakeFactors{exposures: FactorExposures{
			Factors:     []FactorExposure{{Name: "value", Exposure: 0.4, FactorReturn: 0.25}},
			DatasetHash: "f1",
		}},
	})
	rep, err := svc.Attribution(t.Context(), AttributionRequest{RunID: "r1", Method: "factor"})
	if err != nil {
		t.Fatalf("Attribution: %v", err)
	}
	rec, ok := store.attributions[rep.ReportID]
	if !ok {
		t.Fatalf("attribution record not persisted")
	}
	if math.Abs(rec.Residual-0.0) > 1e-12 || len(rec.Contributions) == 0 {
		t.Fatalf("attribution record = %+v", rec)
	}
	if rec.Method != "factor" {
		t.Fatalf("method = %q", rec.Method)
	}
}
