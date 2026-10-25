package analysis

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestExpandGridOrderAndCap(t *testing.T) {
	points, err := expandGrid([]GridParam{
		{Name: "b", Values: []float64{10, 20}},
		{Name: "a", Values: []float64{1, 2}},
	})
	if err != nil {
		t.Fatalf("expandGrid: %v", err)
	}
	want := []string{"a=1,b=10", "a=1,b=20", "a=2,b=10", "a=2,b=20"}
	if len(points) != len(want) {
		t.Fatalf("points = %d, want %d", len(points), len(want))
	}
	for i, got := range want {
		if paramString(points[i]) != got {
			t.Fatalf("point %d = %s, want %s", i, paramString(points[i]), got)
		}
	}

	if _, err := expandGrid([]GridParam{
		{Name: "a", Values: make([]float64, 101)},
	}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("101 points: want ErrInvalidParams, got %v", err)
	}
}

func TestServiceSensitivityBackfillsPoints(t *testing.T) {
	reader := &fakeReader{results: map[string]*BacktestResponse{
		"r1":      testResult("r1", 40),
		"child-1": testResult("child-1", 40),
		"child-2": testResult("child-2", 40),
		"child-3": testResult("child-3", 40),
		"child-4": testResult("child-4", 40),
	}}
	queue := &fakeQueue{progress: SensitivityProgress{
		Status:  "completed",
		Total:   4,
		Settled: 4,
		Points: []SensitivityChild{
			{RunID: "child-1", ResultStatus: ResultCompleted},
			{RunID: "child-2", ResultStatus: ResultCompleted},
			{RunID: "child-3", ResultStatus: ResultCompleted},
			{RunID: "child-4", ResultStatus: ResultCompleted},
		},
	}}
	svc := newTestService(t, Options{Results: reader, Queue: queue})

	sub, err := svc.Sensitivity(t.Context(), "r1", []GridParam{
		{Name: "lookback", Values: []float64{10, 20}},
		{Name: "fee", Values: []float64{0.01, 0.02}},
	})
	if err != nil {
		t.Fatalf("Sensitivity: %v", err)
	}

	rep, err := svc.GetReport(t.Context(), sub.Report.ReportID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Status != ReportCompleted {
		t.Fatalf("status = %s (%s), want completed", rep.Status, rep.Error)
	}
	var payload SensitivityPayload
	if err := json.Unmarshal(rep.Body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.ParentJobID != "job-1" || len(payload.Points) != 4 || payload.Counts.Completed != 4 {
		t.Fatalf("payload = %+v", payload)
	}
	wantParams := []string{"fee=0.01,lookback=10", "fee=0.01,lookback=20", "fee=0.02,lookback=10", "fee=0.02,lookback=20"}
	for i, point := range payload.Points {
		if point.RunID != "child-"+"1234"[i:i+1] {
			t.Fatalf("point %d run_id = %q", i, point.RunID)
		}
		if got := paramString(point.Params); got != wantParams[i] {
			t.Fatalf("point %d params = %s, want %s", i, got, wantParams[i])
		}
		if point.Status != ResultCompleted || point.Metrics == nil {
			t.Fatalf("point %d = %+v", i, point)
		}
	}

	rec, err := svc.store.GetSensitivity(t.Context(), sub.Report.ReportID)
	if err != nil {
		t.Fatalf("GetSensitivity: %v", err)
	}
	var stored []SensitivityPoint
	if err := json.Unmarshal(rec.Points, &stored); err != nil {
		t.Fatalf("decode stored points: %v", err)
	}
	if len(stored) != 4 {
		t.Fatalf("stored points = %d, want 4", len(stored))
	}
}

func TestServiceSensitivityMissingPointFailsClosed(t *testing.T) {
	reader := &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}}
	queue := &fakeQueue{progress: SensitivityProgress{
		Status:  "completed",
		Total:   4,
		Settled: 4,
		Points: []SensitivityChild{
			{RunID: "child-1", ResultStatus: ResultCompleted},
			{RunID: "child-2", ResultStatus: ResultCompleted},
			{RunID: "child-3", ResultStatus: ResultCompleted},
		},
	}}
	svc := newTestService(t, Options{Results: reader, Queue: queue})
	sub, err := svc.Sensitivity(t.Context(), "r1", []GridParam{
		{Name: "a", Values: []float64{1, 2}},
		{Name: "b", Values: []float64{10, 20}},
	})
	if err != nil {
		t.Fatalf("Sensitivity: %v", err)
	}

	rep, err := svc.GetReport(t.Context(), sub.Report.ReportID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Status != ReportFailed || !strings.Contains(rep.Error, "refusing to synthesize") {
		t.Fatalf("report = %+v, want failed with missing-point error", rep)
	}
	rec, err := svc.store.GetSensitivity(t.Context(), sub.Report.ReportID)
	if err != nil {
		t.Fatalf("GetSensitivity: %v", err)
	}
	if len(rec.Points) != 0 {
		t.Fatalf("missing point must not write a partial grid: %s", rec.Points)
	}
}

func TestServiceSensitivityPartialInvalidWritesPointsAndFails(t *testing.T) {
	reader := &fakeReader{results: map[string]*BacktestResponse{
		"r1":      testResult("r1", 40),
		"child-1": testResult("child-1", 40),
		"child-2": testResult("child-2", 40),
	}}
	queue := &fakeQueue{progress: SensitivityProgress{
		Status:  "failed",
		Total:   2,
		Settled: 2,
		Points: []SensitivityChild{
			{RunID: "child-1", ResultStatus: ResultCompleted},
			{RunID: "child-2", ResultStatus: ResultInvalid, Error: "too few trades"},
		},
	}}
	svc := newTestService(t, Options{Results: reader, Queue: queue})
	sub, err := svc.Sensitivity(t.Context(), "r1", []GridParam{{Name: "lookback", Values: []float64{10, 20}}})
	if err != nil {
		t.Fatalf("Sensitivity: %v", err)
	}

	rep, err := svc.GetReport(t.Context(), sub.Report.ReportID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Status != ReportFailed || len(rep.Body) != 0 {
		t.Fatalf("report = %+v, want failed without body", rep)
	}
	if !strings.Contains(rep.Error, "point 2") || !strings.Contains(rep.Error, "too few trades") {
		t.Fatalf("per-point error missing: %s", rep.Error)
	}
	rec, err := svc.store.GetSensitivity(t.Context(), sub.Report.ReportID)
	if err != nil {
		t.Fatalf("GetSensitivity: %v", err)
	}
	var stored []SensitivityPoint
	if err := json.Unmarshal(rec.Points, &stored); err != nil {
		t.Fatalf("decode stored points: %v", err)
	}
	if len(stored) != 2 || stored[1].Status != ResultInvalid || stored[1].Metrics != nil {
		t.Fatalf("stored points = %+v", stored)
	}
}

func TestServiceSensitivityResultInvalidStaysInvalid(t *testing.T) {
	reader := &fakeReader{
		results: map[string]*BacktestResponse{"r1": testResult("r1", 40)},
		errs:    map[string]error{"child-1": &InvalidReasonsError{Reasons: []string{"data_gap_skipped"}}},
	}
	queue := &fakeQueue{progress: SensitivityProgress{
		Status:  "failed",
		Total:   1,
		Settled: 1,
		Points:  []SensitivityChild{{RunID: "child-1", ResultStatus: ResultCompleted}},
	}}
	svc := newTestService(t, Options{Results: reader, Queue: queue})
	sub, err := svc.Sensitivity(t.Context(), "r1", []GridParam{{Name: "lookback", Values: []float64{10}}})
	if err != nil {
		t.Fatalf("Sensitivity: %v", err)
	}
	if _, err := svc.GetReport(t.Context(), sub.Report.ReportID); err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	rec, err := svc.store.GetSensitivity(t.Context(), sub.Report.ReportID)
	if err != nil {
		t.Fatalf("GetSensitivity: %v", err)
	}
	var stored []SensitivityPoint
	if err := json.Unmarshal(rec.Points, &stored); err != nil {
		t.Fatalf("decode points: %v", err)
	}
	if len(stored) != 1 || stored[0].Status != ResultInvalid || !strings.Contains(stored[0].Error, "data_gap_skipped") {
		t.Fatalf("point = %+v, want invalid with upstream reason", stored)
	}
}

func TestServiceSensitivityTransientStatusKeepsPending(t *testing.T) {
	reader := &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}}
	queue := &fakeQueue{statusErr: ErrDependencyUnavailable}
	svc := newTestService(t, Options{Results: reader, Queue: queue})
	sub, err := svc.Sensitivity(t.Context(), "r1", []GridParam{{Name: "lookback", Values: []float64{10}}})
	if err != nil {
		t.Fatalf("Sensitivity: %v", err)
	}
	rep, err := svc.GetReport(t.Context(), sub.Report.ReportID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Status != ReportPending {
		t.Fatalf("status = %s, want pending (transient upstream errors are retryable)", rep.Status)
	}
}

func TestServiceSensitivityParentNotFoundFailsClosed(t *testing.T) {
	reader := &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}}
	queue := &fakeQueue{statusErr: ErrSensitivityParentNotFound}
	svc := newTestService(t, Options{Results: reader, Queue: queue})
	sub, err := svc.Sensitivity(t.Context(), "r1", []GridParam{{Name: "lookback", Values: []float64{10}}})
	if err != nil {
		t.Fatalf("Sensitivity: %v", err)
	}
	rep, err := svc.GetReport(t.Context(), sub.Report.ReportID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if rep.Status != ReportFailed || !strings.Contains(rep.Error, "not found") {
		t.Fatalf("report = %+v, want failed parent_not_found", rep)
	}
}

func TestServiceSensitivityCompletedNeedsResultReader(t *testing.T) {
	reader := &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}}
	queue := &fakeQueue{progress: SensitivityProgress{
		Status:  "completed",
		Total:   1,
		Settled: 1,
		Points:  []SensitivityChild{{RunID: "child-1", ResultStatus: ResultCompleted}},
	}}
	svc := newTestService(t, Options{Results: reader, Queue: queue})
	sub, err := svc.Sensitivity(t.Context(), "r1", []GridParam{{Name: "lookback", Values: []float64{10}}})
	if err != nil {
		t.Fatalf("Sensitivity: %v", err)
	}
	svc.results = nil
	if _, err := svc.GetReport(t.Context(), sub.Report.ReportID); !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("want ErrDependencyUnavailable, got %v", err)
	}
}
