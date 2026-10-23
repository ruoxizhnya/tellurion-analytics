package analysis

import (
	"errors"
	"testing"
)

func TestReportIDIsDeterministicAndKeySensitive(t *testing.T) {
	seed := int64(7)
	other := int64(7)
	zero := int64(0)

	base := ReportID("r1", TypeMetrics, "p1", "i1", nil, MetricsVersion)
	if base != ReportID("r1", TypeMetrics, "p1", "i1", nil, MetricsVersion) {
		t.Fatalf("report_id is not deterministic")
	}
	if base == ReportID("r1", TypeAttribution, "p1", "i1", nil, MetricsVersion) {
		t.Fatalf("analysis_type is not part of report_id")
	}
	if base == ReportID("r2", TypeMetrics, "p1", "i1", nil, MetricsVersion) {
		t.Fatalf("run_id is not part of report_id")
	}
	if base == ReportID("r1", TypeMetrics, "p2", "i1", nil, MetricsVersion) {
		t.Fatalf("params_hash is not part of report_id")
	}
	if base == ReportID("r1", TypeMetrics, "p1", "i2", nil, MetricsVersion) {
		t.Fatalf("input_hash is not part of report_id")
	}
	// nil 与 0 必须区分（F6）
	if base == ReportID("r1", TypeMetrics, "p1", "i1", &zero, MetricsVersion) {
		t.Fatalf("nil seed collides with seed=0")
	}
	if ReportID("r1", TypeMetrics, "p1", "i1", &seed, MetricsVersion) !=
		ReportID("r1", TypeMetrics, "p1", "i1", &other, MetricsVersion) {
		t.Fatalf("same seed produced different report_id")
	}
	if base == ReportID("r1", TypeMetrics, "p1", "i1", nil, "v2") {
		t.Fatalf("metrics_version is not part of report_id")
	}
}

func TestHashInputsIsUnambiguous(t *testing.T) {
	if hashInputs("a", "bc") == hashInputs("ab", "c") {
		t.Fatalf("length prefix missing: concatenation collision")
	}
}

func TestMemoryStoreCompletedReportIsImmutable(t *testing.T) {
	store := NewMemory()
	rep := Report{
		ReportID:       "rep1",
		AnalysisType:   TypeMetrics,
		Status:         ReportCompleted,
		MetricsVersion: MetricsVersion,
		Body:           []byte(`{"a":1}`),
	}
	if err := store.PutReport(t.Context(), rep); err != nil {
		t.Fatalf("PutReport: %v", err)
	}
	if err := store.PutReport(t.Context(), rep); err != nil {
		t.Fatalf("idempotent PutReport: %v", err)
	}
	changed := rep
	changed.Body = []byte(`{"a":2}`)
	if err := store.PutReport(t.Context(), changed); !errors.Is(err, ErrReportConflict) {
		t.Fatalf("want ErrReportConflict, got %v", err)
	}
}

func TestMemoryStorePendingCanAdvanceToCompleted(t *testing.T) {
	store := NewMemory()
	rep := Report{ReportID: "rep2", AnalysisType: TypeMonteCarlo, Status: ReportPending, MetricsVersion: MetricsVersion}
	if err := store.PutReport(t.Context(), rep); err != nil {
		t.Fatalf("PutReport pending: %v", err)
	}
	rep.Status = ReportCompleted
	rep.Body = []byte(`{"ok":true}`)
	if err := store.PutReport(t.Context(), rep); err != nil {
		t.Fatalf("advance to completed: %v", err)
	}
	got, err := store.GetReport(t.Context(), "rep2")
	if err != nil || got.Status != ReportCompleted {
		t.Fatalf("stored = %+v, err=%v", got, err)
	}
}

func TestMemoryStoreLatestBenchmark(t *testing.T) {
	store := NewMemory()
	older := BenchmarkRef{Code: "CSI300", AdjustmentBasis: "total_return", DatasetHash: "old", RegisteredAt: baseDate}
	newer := BenchmarkRef{Code: "CSI300", AdjustmentBasis: "total_return", DatasetHash: "new", RegisteredAt: baseDate.AddDate(0, 0, 1)}
	if err := store.RegisterBenchmark(t.Context(), older); err != nil {
		t.Fatalf("register older: %v", err)
	}
	if err := store.RegisterBenchmark(t.Context(), newer); err != nil {
		t.Fatalf("register newer: %v", err)
	}
	got, err := store.LatestBenchmark(t.Context(), "CSI300")
	if err != nil {
		t.Fatalf("LatestBenchmark: %v", err)
	}
	if got.DatasetHash != "new" {
		t.Fatalf("latest = %+v", got)
	}
	if _, err := store.LatestBenchmark(t.Context(), "SPX"); !errors.Is(err, ErrBenchmarkNotRegistered) {
		t.Fatalf("want ErrBenchmarkNotRegistered, got %v", err)
	}
}
