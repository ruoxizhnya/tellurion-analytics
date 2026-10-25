package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"tellurion-analytics/internal/analysis"
	"tellurion-analytics/internal/upstream"
)

// TestSensitivityEndToEndWithFakeParent 走真 HTTP 的契约级端到端：
// 本仓 → 父作业适配器 → 假 orchestration（严格按 W6 的 wire 形状应答）。
//
// 覆盖：提交父作业 → 轮询 pending → 父作业落定 → 逐点回填 + 报告 completed。
// 真实进程版见 TestSensitivityEndToEndWithOrchestrationProcess。
func TestSensitivityEndToEndWithFakeParent(t *testing.T) {
	state := &fakeParentState{}
	orch := httptest.NewServer(state)
	defer orch.Close()

	srv, _ := newTestServer(t, analysis.Options{
		Results: syntheticResultReader{},
		Queue:   upstream.NewSensitivityQueue(orch.URL, orch.Client()),
	})

	// run_id 每次唯一：报告身份不跨用例 / 跨轮次复用。
	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	status, body := post(t, srv, "/api/analysis/sensitivity",
		fmt.Sprintf(`{"run_id":%q,"param_grid":[{"name":"lookback","values":[10,20]}]}`, runID))
	if status != http.StatusAccepted {
		t.Fatalf("submit = %d %s", status, body)
	}
	job := decode(t, body)
	reportID, _ := job["report_id"].(string)
	parentID, _ := job["parent_job_id"].(string)
	if reportID == "" || parentID == "" {
		t.Fatalf("job response = %v", job)
	}

	_, raw := get(t, srv, "/api/analysis/reports/"+reportID)
	if rep := decode(t, raw); rep["status"] != "pending" {
		t.Fatalf("before settle: %v", rep)
	}

	state.settle()
	_, raw = get(t, srv, "/api/analysis/reports/"+reportID)
	rep := decode(t, raw)
	if rep["status"] != "completed" {
		t.Fatalf("after settle: %v (%v)", rep["status"], rep["error"])
	}
	report, _ := rep["report"].(map[string]any)
	points, _ := report["points"].([]any)
	if len(points) != 2 || report["parent_job_id"] != parentID {
		t.Fatalf("report = %v", report)
	}
	first, _ := points[0].(map[string]any)
	params, _ := first["params"].(map[string]any)
	if params["lookback"] != float64(10) || first["run_id"] != parentID+"-child-1" {
		t.Fatalf("point 1 = %v", first)
	}
}

// syntheticResultReader 给基 run 与所有子 run 返回同一份合法结果。
type syntheticResultReader struct{}

func (syntheticResultReader) GetResult(_ context.Context, runID string) (*analysis.BacktestResponse, error) {
	return sampleResult(runID), nil
}

// fakeParentState 是假的 X5b 父作业服务（严格按 docs/api-schema.md 的 wire 形状）。
type fakeParentState struct {
	settled bool
}

func (f *fakeParentState) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/runs/parents":
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"parent_job_id":"parent-1","kind":"batch","status":"pending","total":2}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/runs/parents/parent-1":
		if !f.settled {
			_, _ = w.Write([]byte(`{"parent_job_id":"parent-1","status":"running","total":2,
				"counts":{"completed":0,"invalid":0,"failed":0,"pending":2,"settled":0},"children":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"parent_job_id":"parent-1","kind":"batch","status":"completed","total":2,
			"counts":{"completed":2,"invalid":0,"failed":0,"pending":0,"settled":2},
			"children":[],
			"summary":{"Counts":{"Total":2},"Status":"completed","Tasks":[
				{"run_id":"parent-1-child-1","result_status":"completed","duration_ms":1,"rank":1},
				{"run_id":"parent-1-child-2","result_status":"completed","duration_ms":1,"rank":2}]}}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeParentState) settle() { f.settled = true }

// TestSensitivityEndToEndWithOrchestrationProcess 起**真** orchestration 进程
// （`go build` + `cmd/orchestrationservice`），验证提交 / 轮询 / 回填全链路。
//
// 前置：orchestration 按设计拒绝无库启动，故仅在 `ORCHESTRATION_TEST_DSN` 有值时
// 运行（CI 无 PG 会 skip）。执行侧（worker）尚未落地，父记录的汇总由本用例
// 通过 SQL 模拟「执行侧调用 ApplyBatchSummary 后的落库形状」——这是唯一被模拟的
// 环节，HTTP 与父表读取都是真的。
func TestSensitivityEndToEndWithOrchestrationProcess(t *testing.T) {
	dsn := os.Getenv("ORCHESTRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("ORCHESTRATION_TEST_DSN is not set; skipping real orchestration process E2E")
	}
	repo := os.Getenv("ORCHESTRATION_REPO")
	if repo == "" {
		repo = filepath.Join("..", "..", "..", "tellurion-orchestration")
	}
	if _, err := os.Stat(filepath.Join(repo, "cmd", "orchestrationservice")); err != nil {
		t.Skipf("orchestration repo not found at %s: %v", repo, err)
	}

	bin := filepath.Join(t.TempDir(), "orchestrationservice")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "./cmd/orchestrationservice")
	build.Dir = repo
	var buildOut bytes.Buffer
	build.Stdout, build.Stderr = &buildOut, &buildOut
	if err := build.Run(); err != nil {
		t.Fatalf("build orchestration: %v\n%s", err, buildOut.String())
	}

	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-dsn", dsn, "-addr", addr, "-reap-interval", "0")
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start orchestration: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("orchestration logs:\n%s", logs.String())
		}
	})
	waitHealthy(t, addr, &logs)

	srv, _ := newTestServer(t, analysis.Options{
		Results: syntheticResultReader{},
		Queue:   upstream.NewSensitivityQueue("http://"+addr, nil),
	})

	// run_id 每次唯一：orchestration 的父身份是 plan_hash，复用同一 run_id 会
	// 命中上一轮已完成的父作业，测不到本轮的 pending。
	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	status, body := post(t, srv, "/api/analysis/sensitivity",
		fmt.Sprintf(`{"run_id":%q,"param_grid":[{"name":"lookback","values":[10,20]}]}`, runID))
	if status != http.StatusAccepted {
		t.Fatalf("submit = %d %s", status, body)
	}
	job := decode(t, body)
	reportID, _ := job["report_id"].(string)
	parentID, _ := job["parent_job_id"].(string)
	if reportID == "" || parentID == "" {
		t.Fatalf("job response = %v", job)
	}

	_, raw := get(t, srv, "/api/analysis/reports/"+reportID)
	if rep := decode(t, raw); rep["status"] != "pending" {
		t.Fatalf("before workers report: %v", rep)
	}

	// 模拟执行侧：把父记录推进到 completed，并按计划顺序写回逐点结论。
	seedParentSummary(t, dsn, parentID, 2)

	_, raw = get(t, srv, "/api/analysis/reports/"+reportID)
	rep := decode(t, raw)
	if rep["status"] != "completed" {
		t.Fatalf("after summary: %v (%v)", rep["status"], rep["error"])
	}
	report, _ := rep["report"].(map[string]any)
	points, _ := report["points"].([]any)
	if len(points) != 2 {
		t.Fatalf("points = %v", report["points"])
	}
	second, _ := points[1].(map[string]any)
	params, _ := second["params"].(map[string]any)
	if params["lookback"] != float64(20) {
		t.Fatalf("point 2 params = %v", params)
	}
}

// seedParentSummary 模拟执行侧写回父汇总（W6 的 ApplyBatchSummary 落库形状）。
func seedParentSummary(t *testing.T, dsn, parentID string, total int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect orchestration db: %v", err)
	}
	defer pool.Close()

	tasks := make([]map[string]any, total)
	for i := range tasks {
		tasks[i] = map[string]any{
			"run_id":        fmt.Sprintf("%s-child-%d", parentID, i+1),
			"result_status": "completed",
			"duration_ms":   1,
			"rank":          i + 1,
		}
	}
	summary, err := json.Marshal(map[string]any{
		"Counts": map[string]any{"Total": total, "Completed": total, "Invalid": 0, "Failed": 0, "Pending": 0},
		"Status": "completed",
		"Tasks":  tasks,
	})
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	tag, err := pool.Exec(ctx,
		`UPDATE run.parent_jobs
		    SET status = 'completed', completed = $2, invalid = 0, failed = 0,
		        summary_json = $3::jsonb, completed_at = now()
		  WHERE parent_job_id = $1`,
		parentID, total, string(summary))
	if err != nil {
		t.Fatalf("seed parent summary: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("parent %s not found in run.parent_jobs", parentID)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitHealthy(t *testing.T, addr string, logs *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("orchestration did not become healthy on %s:\n%s", addr, logs.String())
}
