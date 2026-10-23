package analysis

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestMonteCarloDeterministicPerSeed(t *testing.T) {
	resp := testResult("r1", 40)
	first, err := buildMonteCarloPayload(resp, 200, 42, MethodBootstrap)
	if err != nil {
		t.Fatalf("buildMonteCarloPayload: %v", err)
	}
	second, err := buildMonteCarloPayload(resp, 200, 42, MethodBootstrap)
	if err != nil {
		t.Fatalf("buildMonteCarloPayload: %v", err)
	}
	b1, _ := json.Marshal(first)
	b2, _ := json.Marshal(second)
	if !bytes.Equal(b1, b2) {
		t.Fatalf("same seed produced different results:\n%s\n%s", b1, b2)
	}

	other, err := buildMonteCarloPayload(resp, 200, 43, MethodBootstrap)
	if err != nil {
		t.Fatalf("buildMonteCarloPayload: %v", err)
	}
	b3, _ := json.Marshal(other)
	if bytes.Equal(b1, b3) {
		t.Fatalf("different seeds produced identical results")
	}
	if first.Seed != 42 || first.N != 200 || first.Method != MethodBootstrap {
		t.Fatalf("payload identity = %+v", first)
	}
	if first.P05 > first.P50 || first.P50 > first.P95 {
		t.Fatalf("percentiles out of order: %+v", first)
	}
}

func TestMonteCarloShuffleIsDeterministic(t *testing.T) {
	resp := testResult("r1", 40)
	a, err := buildMonteCarloPayload(resp, 50, 7, MethodShuffle)
	if err != nil {
		t.Fatalf("shuffle: %v", err)
	}
	b, err := buildMonteCarloPayload(resp, 50, 7, MethodShuffle)
	if err != nil {
		t.Fatalf("shuffle: %v", err)
	}
	if a.Mean != b.Mean || a.StdDev != b.StdDev {
		t.Fatalf("shuffle not reproducible: %+v vs %+v", a, b)
	}
}

func TestServiceMonteCarloSeedRules(t *testing.T) {
	reader := &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}}
	svc := newTestService(t, Options{Results: reader})

	if _, err := svc.MonteCarlo(t.Context(), MonteCarloRequest{RunID: "r1", N: 10}); !errors.Is(err, ErrSeedRequired) {
		t.Fatalf("missing seed: want ErrSeedRequired, got %v", err)
	}
	// seed=0 是合法的显式种子（F6），不是 time-based
	seed := int64(0)
	rep, err := svc.MonteCarlo(t.Context(), MonteCarloRequest{RunID: "r1", N: 10, Seed: &seed})
	if err != nil {
		t.Fatalf("seed=0: %v", err)
	}
	if rep.Status != ReportPending {
		t.Fatalf("POST must return pending, got %s", rep.Status)
	}
	// InlineExecutor：返回时计算已完成，可从 Store 读到 completed
	stored, err := svc.GetReport(t.Context(), rep.ReportID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if stored.Status != ReportCompleted {
		t.Fatalf("stored status = %s", stored.Status)
	}
	var payload MonteCarloPayload
	if err := json.Unmarshal(stored.Body, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload.Seed != 0 {
		t.Fatalf("stored seed = %d", payload.Seed)
	}

	if _, err := svc.MonteCarlo(t.Context(), MonteCarloRequest{RunID: "r1", N: 0, Seed: &seed}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("n=0: want ErrInvalidParams, got %v", err)
	}
	if _, err := svc.MonteCarlo(t.Context(), MonteCarloRequest{RunID: "r1", N: MaxMonteCarloN + 1, Seed: &seed}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("n overflow: want ErrInvalidParams, got %v", err)
	}
	if _, err := svc.MonteCarlo(t.Context(), MonteCarloRequest{RunID: "r1", N: 10, Seed: &seed, Method: "jackknife"}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("bad method: want ErrInvalidParams, got %v", err)
	}
}

func TestServiceMonteCarloReplayIsByteIdentical(t *testing.T) {
	store := NewMemory()
	reader := &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}}
	svc := newTestService(t, Options{Store: store, Results: reader})
	seed := int64(99)
	first, err := svc.MonteCarlo(t.Context(), MonteCarloRequest{RunID: "r1", N: 20, Seed: &seed})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.MonteCarlo(t.Context(), MonteCarloRequest{RunID: "r1", N: 20, Seed: &seed})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.ReportID != second.ReportID {
		t.Fatalf("report_id drift: %s vs %s", first.ReportID, second.ReportID)
	}
	stored, err := svc.GetReport(t.Context(), first.ReportID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if stored.Status != ReportCompleted {
		t.Fatalf("stored status = %s", stored.Status)
	}
	if !bytes.Equal(stored.Body, second.Body) {
		t.Fatalf("replay body differs from stored report body")
	}
	if rec, ok := store.mcRuns[first.ReportID]; !ok || rec.Seed != 99 {
		t.Fatalf("mc record = %+v, ok=%v", rec, ok)
	}
}
