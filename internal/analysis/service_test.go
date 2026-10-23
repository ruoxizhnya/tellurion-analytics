package analysis

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestServiceMetricsThreeValueFailClosed(t *testing.T) {
	cases := []struct {
		name  string
		resp  *BacktestResponse
		err   error
		check func(t *testing.T, err error)
	}{
		{
			name: "invalid",
			resp: &BacktestResponse{Status: ResultInvalid, InvalidReasons: []string{"garbage_metric"}},
			check: func(t *testing.T, err error) {
				var invalid *InvalidReasonsError
				if !errors.As(err, &invalid) || len(invalid.Reasons) != 1 || invalid.Reasons[0] != "garbage_metric" {
					t.Fatalf("want InvalidReasonsError, got %v", err)
				}
			},
		},
		{
			name: "failed",
			resp: &BacktestResponse{Status: ResultFailed, Error: "boom"},
			check: func(t *testing.T, err error) {
				var failed *FailedError
				if !errors.As(err, &failed) || failed.Message != "boom" {
					t.Fatalf("want FailedError, got %v", err)
				}
			},
		},
		{
			name: "not completed",
			resp: &BacktestResponse{Status: ""},
			check: func(t *testing.T, err error) {
				var incomplete *IncompleteError
				if !errors.As(err, &incomplete) {
					t.Fatalf("want *IncompleteError, got %v", err)
				}
			},
		},
		{
			name: "completed without curve",
			resp: &BacktestResponse{Status: ResultCompleted},
			check: func(t *testing.T, err error) {
				mustIncompleteMissing(t, err, "portfolio_values")
			},
		},
		{
			name: "run not found",
			err:  ErrRunNotFound,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrRunNotFound) {
					t.Fatalf("want ErrRunNotFound, got %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeReader{
				results: map[string]*BacktestResponse{"r1": tc.resp},
				errs:    map[string]error{"r1": tc.err},
			}
			svc := newTestService(t, Options{Results: reader})
			_, err := svc.Metrics(t.Context(), "r1")
			if err == nil {
				t.Fatalf("want error")
			}
			tc.check(t, err)
		})
	}
}

func TestServiceMetricsRequiresWiredReader(t *testing.T) {
	svc := newTestService(t, Options{})
	_, err := svc.Metrics(t.Context(), "r1")
	if !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("want ErrDependencyUnavailable, got %v", err)
	}
}

func TestServiceMetricsReportShapeAndReplay(t *testing.T) {
	reader := &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}}
	svc := newTestService(t, Options{Results: reader})

	first, err := svc.Metrics(t.Context(), "r1")
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if first.Status != ReportCompleted || first.AnalysisType != TypeMetrics || first.MetricsVersion != MetricsVersion {
		t.Fatalf("report = %+v", first)
	}
	var payload MetricsPayload
	if err := json.Unmarshal(first.Body, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload.FromResult.SharpeRatio != 1.234 {
		t.Fatalf("from_result sharpe = %v", payload.FromResult.SharpeRatio)
	}

	second, err := svc.Metrics(t.Context(), "r1")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if second.ReportID != first.ReportID || !bytes.Equal(second.Body, first.Body) {
		t.Fatalf("replay is not byte-identical")
	}
}

func TestServiceMetricsInputChangesReportIdentity(t *testing.T) {
	store := NewMemory()
	reader := &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}}
	svc := newTestService(t, Options{Store: store, Results: reader})

	first, err := svc.Metrics(t.Context(), "r1")
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	// 上游结果换版（同 run_id）⇒ 新 input_hash ⇒ 新报告，不复用旧结论
	changed := testResult("r1", 40)
	changed.TotalReturn = 0.99
	reader.results["r1"] = changed
	second, err := svc.Metrics(t.Context(), "r1")
	if err != nil {
		t.Fatalf("Metrics changed input: %v", err)
	}
	if second.ReportID == first.ReportID {
		t.Fatalf("input change reused report_id %s", first.ReportID)
	}
}

func TestServiceSensitivityFailClosedAndPending(t *testing.T) {
	reader := &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}}

	// 队列未接线 ⇒ 503 哨兵
	svc := newTestService(t, Options{Results: reader})
	_, err := svc.Sensitivity(t.Context(), "r1", []GridParam{{Name: "lookback", Values: []float64{10, 20}}})
	if !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("want ErrDependencyUnavailable, got %v", err)
	}

	// 队列已接线 ⇒ pending 报告 + 提交网格
	queue := &fakeQueue{}
	svc = newTestService(t, Options{Results: reader, Queue: queue})
	rep, err := svc.Sensitivity(t.Context(), "r1", []GridParam{{Name: "lookback", Values: []float64{10, 20}}})
	if err != nil {
		t.Fatalf("Sensitivity: %v", err)
	}
	if rep.Status != ReportPending || len(queue.submitted) != 1 || queue.submitted[0].ReportID != rep.ReportID {
		t.Fatalf("pending report / queue = %+v / %+v", rep, queue.submitted)
	}
	stored, err := svc.GetReport(t.Context(), rep.ReportID)
	if err != nil || stored.Status != ReportPending || stored.Body != nil {
		t.Fatalf("stored pending = %+v, err=%v", stored, err)
	}
}

func TestServiceSensitivityValidatesGrid(t *testing.T) {
	reader := &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}}
	svc := newTestService(t, Options{Results: reader, Queue: &fakeQueue{}})
	badGrids := [][]GridParam{
		nil,
		{{Name: "", Values: []float64{1}}},
		{{Name: "lookback"}},
		{{Name: "a", Values: []float64{1}}, {Name: "a", Values: []float64{2}}},
	}
	for i, grid := range badGrids {
		if _, err := svc.Sensitivity(t.Context(), "r1", grid); !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("grid %d: want ErrInvalidParams, got %v", i, err)
		}
	}
}

func TestExportIsDeterministicAndFailClosed(t *testing.T) {
	reader := &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}}
	svc := newTestService(t, Options{Results: reader})
	rep, err := svc.Metrics(t.Context(), "r1")
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	html1, ct1, name1, err := Export(rep, FormatHTML)
	if err != nil {
		t.Fatalf("Export html: %v", err)
	}
	html2, _, _, _ := Export(rep, FormatHTML)
	if !bytes.Equal(html1, html2) || ct1 != "text/html; charset=utf-8" || name1 != rep.ReportID+".html" {
		t.Fatalf("html export not deterministic / wrong metadata")
	}
	csv1, ct2, _, err := Export(rep, FormatCSV)
	if err != nil {
		t.Fatalf("Export csv: %v", err)
	}
	if ct2 != "text/csv; charset=utf-8" || !bytes.Contains(csv1, []byte("from_result.sharpe_ratio")) {
		t.Fatalf("csv export content/type = %s / %q", csv1, ct2)
	}
	if _, _, _, err := Export(rep, "pdf"); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("pdf: want ErrInvalidParams, got %v", err)
	}

	pending := Report{ReportID: "p1", Status: ReportPending}
	if _, _, _, err := Export(pending, FormatHTML); !errors.Is(err, ErrReportNotReady) {
		t.Fatalf("pending export: want ErrReportNotReady, got %v", err)
	}
}

func TestMigrationsAreNonEmpty(t *testing.T) {
	if len(Migrations()) == 0 {
		t.Fatalf("no migrations")
	}
	// 调用方拿到副本，改动不污染内部数组
	first := Migrations()
	first[0] = "DROP SCHEMA analysis CASCADE"
	if Migrations()[0] == first[0] {
		t.Fatalf("Migrations returned internal slice")
	}
}
