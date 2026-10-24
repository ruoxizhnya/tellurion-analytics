package analysis

import (
	"encoding/json"
	"reflect"
	"testing"
)

// 上游读取契约（R1 / R2）的 wire 形状由本用例钉住：字段改名 / 删字段 / 改大小写
// 都会红。契约文本是 docs/api-schema.md §6（唯一真相源）；改形状先改文档。
func TestUpstreamContractWireShape(t *testing.T) {
	if UpstreamContractVersion != "v1" {
		t.Fatalf("UpstreamContractVersion = %q；改版本必须同步 docs/api-schema.md §6", UpstreamContractVersion)
	}

	t.Run("R1 result", func(t *testing.T) {
		const golden = `{"id":"r1","status":"completed","start_date":"2026-01-01","end_date":"2026-02-01","total_return":0.1,"annual_return":0.2,"sharpe_ratio":1.2,"sortino_ratio":1.5,"max_drawdown":-0.08,"max_drawdown_date":"2026-01-02","win_rate":0.5,"total_trades":10,"win_trades":5,"lose_trades":5,"avg_holding_days":3.5,"calmar_ratio":2.5,"portfolio_values":[{"date":"2026-01-02T00:00:00Z","total_value":100}]}`
		var out BacktestResponse
		assertWireShape(t, golden, &out)
		if out.Status != ResultCompleted || len(out.PortfolioValues) != 1 {
			t.Fatalf("decoded result = %+v", out)
		}
	})

	t.Run("R2 portfolio snapshot", func(t *testing.T) {
		const golden = `{"holdings":[{"symbol":"A","sector":"tech","weight":0.6,"period_return":0.12}],"benchmark_sector_weights":{"tech":0.5},"benchmark_sector_returns":{"tech":0.08},"benchmark_return":0.09,"dataset_hash":"h1"}`
		var out PortfolioSnapshot
		assertWireShape(t, golden, &out)
		if out.DatasetHash != "h1" || len(out.Holdings) != 1 {
			t.Fatalf("decoded snapshot = %+v", out)
		}
	})

	t.Run("R2 factor exposures", func(t *testing.T) {
		const golden = `{"factors":[{"name":"value","exposure":1,"factor_return":0.06}],"dataset_hash":"f1"}`
		var out FactorExposures
		assertWireShape(t, golden, &out)
		if out.DatasetHash != "f1" || len(out.Factors) != 1 {
			t.Fatalf("decoded exposures = %+v", out)
		}
	})
}

// assertWireShape 与 golden 往返比较：字段名被改 / 被删 ⇒ 解码丢值或重编码多键。
func assertWireShape(t *testing.T, golden string, dst any) {
	t.Helper()
	if err := json.Unmarshal([]byte(golden), dst); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	encoded, err := json.Marshal(dst)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var want, got any
	if err := json.Unmarshal([]byte(golden), &want); err != nil {
		t.Fatalf("decode golden (any): %v", err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode re-encoded (%s): %v", encoded, err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("wire shape drift:\n golden: %s\n got:    %s", golden, encoded)
	}
}

func TestUpstreamContractResultRequiresK9Status(t *testing.T) {
	mustIncompleteMissing(t, validateResultContract(&BacktestResponse{}), "status")
	mustIncompleteMissing(t, validateResultContract(&BacktestResponse{Status: "pending"}), "status")
	if err := validateResultContract(&BacktestResponse{Status: ResultCompleted}); err != nil {
		t.Fatalf("completed must pass shape check, got %v", err)
	}
}

func TestUpstreamContractArtifactFailClosed(t *testing.T) {
	valid := PortfolioSnapshot{
		Holdings:               []Holding{{Symbol: "A", Sector: "tech", Weight: 1, PeriodReturn: 0.1}},
		BenchmarkSectorWeights: map[string]float64{"tech": 1},
		BenchmarkSectorReturns: map[string]float64{"tech": 0.08},
		BenchmarkReturn:        0.08,
		DatasetHash:            "h1",
	}
	if err := validatePortfolioSnapshot(valid); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	missingHash := valid
	missingHash.DatasetHash = ""
	mustIncompleteMissing(t, validatePortfolioSnapshot(missingHash), "holdings.dataset_hash")
	missingWeights := valid
	missingWeights.BenchmarkSectorWeights = nil
	mustIncompleteMissing(t, validatePortfolioSnapshot(missingWeights), "benchmark_sector_weights")
	missingReturns := valid
	missingReturns.BenchmarkSectorReturns = nil
	mustIncompleteMissing(t, validatePortfolioSnapshot(missingReturns), "benchmark_sector_returns")

	exposures := FactorExposures{
		Factors:     []FactorExposure{{Name: "value", Exposure: 1, FactorReturn: 0.05}},
		DatasetHash: "f1",
	}
	if err := validateFactorExposures(exposures); err != nil {
		t.Fatalf("valid exposures rejected: %v", err)
	}
	exposures.DatasetHash = ""
	mustIncompleteMissing(t, validateFactorExposures(exposures), "factor_exposures.dataset_hash")
}

// 缺 dataset_hash 必须在算 input_hash 前拒算：否则同键会复用旧报告（假复用）。
func TestServiceAttributionRequiresArtifactDatasetHash(t *testing.T) {
	store := NewMemory()
	svc := newTestService(t, Options{
		Store:   store,
		Results: &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}},
		Holdings: &fakeHoldings{snapshot: PortfolioSnapshot{
			Holdings:               []Holding{{Symbol: "A", Sector: "tech", Weight: 1, PeriodReturn: 0.1}},
			BenchmarkSectorWeights: map[string]float64{"tech": 1},
			BenchmarkSectorReturns: map[string]float64{"tech": 0.08},
		}},
		Factors: &fakeFactors{exposures: FactorExposures{
			Factors: []FactorExposure{{Name: "value", Exposure: 1, FactorReturn: 0.05}},
		}},
	})

	_, err := svc.Attribution(t.Context(), AttributionRequest{RunID: "r1", Method: "brinson"})
	mustIncompleteMissing(t, err, "holdings.dataset_hash")

	_, err = svc.Attribution(t.Context(), AttributionRequest{RunID: "r1", Method: "factor"})
	mustIncompleteMissing(t, err, "factor_exposures.dataset_hash")

	if len(store.reports) != 0 {
		t.Fatalf("fail-closed must not persist a report, got %d", len(store.reports))
	}
}
