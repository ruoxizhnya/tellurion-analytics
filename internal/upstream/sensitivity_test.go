package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tellurion-analytics/internal/analysis"
)

func TestSensitivityQueueSubmitShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/runs/parents" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"parent_job_id":"parent-1","status":"pending"}`))
	}))
	defer srv.Close()

	q := NewSensitivityQueue(srv.URL, srv.Client())
	parentID, err := q.Submit(context.Background(), analysis.SensitivityJob{
		ReportID: "report-1",
		RunID:    "run-1",
		Grid:     []analysis.GridParam{{Name: "lookback", Values: []float64{10, 20}}},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if parentID != "parent-1" {
		t.Fatalf("parent = %q", parentID)
	}
	if got["idempotency_key"] != "analysis-sensitivity:report-1" || got["kind"] != "batch" {
		t.Fatalf("payload = %v", got)
	}
	batch, _ := got["batch"].(map[string]any)
	spec, _ := batch["base_run_spec"].(map[string]any)
	grid, _ := batch["grid"].(map[string]any)
	if spec["run_id"] != "run-1" {
		t.Fatalf("base_run_spec = %v", spec)
	}
	values, _ := grid["lookback"].([]any)
	if len(values) != 2 || values[0].(float64) != 10 || values[1].(float64) != 20 {
		t.Fatalf("grid = %v", grid)
	}
}

func TestSensitivityQueueSubmitErrorsFailClosed(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusConflict, http.StatusTooManyRequests, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", status)
		}))
		q := NewSensitivityQueue(srv.URL, srv.Client())
		_, err := q.Submit(context.Background(), analysis.SensitivityJob{
			ReportID: "r", RunID: "run", Grid: []analysis.GridParam{{Name: "a", Values: []float64{1}}},
		})
		if !errors.Is(err, analysis.ErrDependencyUnavailable) {
			t.Fatalf("status %d: want ErrDependencyUnavailable, got %v", status, err)
		}
		srv.Close()
	}
}

func TestSensitivityQueueStatusFromSummaryTasks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runs/parents/parent-1" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"parent_job_id":"parent-1","kind":"batch","status":"completed","total":2,
			"counts":{"completed":1,"invalid":1,"failed":0,"pending":0,"settled":2},
			"children":[],
			"summary":{"Counts":{"Total":2},"Status":"completed","Tasks":[
				{"run_id":"child-1","result_status":"completed","duration_ms":1,"rank":1},
				{"run_id":"child-2","result_status":"invalid","error":"too few trades","duration_ms":1,"rank":0}]}
		}`))
	}))
	defer srv.Close()

	q := NewSensitivityQueue(srv.URL, srv.Client())
	prog, err := q.Status(context.Background(), "parent-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if prog.Status != "completed" || prog.Total != 2 || prog.Settled != 2 || len(prog.Points) != 2 {
		t.Fatalf("progress = %+v", prog)
	}
	if prog.Points[0].RunID != "child-1" || prog.Points[1].ResultStatus != analysis.ResultInvalid ||
		prog.Points[1].Error != "too few trades" {
		t.Fatalf("points = %+v", prog.Points)
	}
}

func TestSensitivityQueueStatusFallsBackToChildren(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"parent_job_id":"parent-1","status":"running","total":2,
			"counts":{"completed":1,"invalid":0,"failed":0,"pending":1,"settled":1},
			"children":[
				{"run_id":"child-1","status":"completed","result_status":"completed","created_at":null},
				{"run_id":"child-2","status":"running","result_status":null,"created_at":null}]
		}`))
	}))
	defer srv.Close()

	q := NewSensitivityQueue(srv.URL, srv.Client())
	prog, err := q.Status(context.Background(), "parent-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if prog.Status != "running" || len(prog.Points) != 2 {
		t.Fatalf("progress = %+v", prog)
	}
	if prog.Points[1].ResultStatus != "" {
		t.Fatalf("pending point result_status = %q, want empty", prog.Points[1].ResultStatus)
	}
}

func TestSensitivityQueueStatusNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"code":"parent_not_found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	q := NewSensitivityQueue(srv.URL, srv.Client())
	_, err := q.Status(context.Background(), "missing")
	if !errors.Is(err, analysis.ErrSensitivityParentNotFound) {
		t.Fatalf("want ErrSensitivityParentNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error should name the parent: %v", err)
	}
}
