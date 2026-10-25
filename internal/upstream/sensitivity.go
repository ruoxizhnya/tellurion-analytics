// SensitivityQueue 是 X5b 父作业（POST/GET /api/runs/parents）的 HTTP 适配器。
//
// 本仓只提交网格、读汇总；**不执行**回测。提交载荷沿用 W6 冻结的 batch 形状：
// `base_run_spec` 只带 `run_id` 作为 interim 锚点（完整 RunSpec 的装配属执行侧，
// 见 docs/ARCHITECTURE.md §9.7 R3），网格按参数名 → 取值数组给出。
//
// 逐点绑定：父汇总里的 `summary.Tasks` 顺序 = 计划顺序（参数名字典序、最后一维
// 变化最快）；没有 summary 时回落到 `children`（同序）。两者都没有内容时返回
// 空点集，由 analysis 层 fail-closed，不在这里编造。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"tellurion-analytics/internal/analysis"
)

// SensitivityQueue 实现 analysis.SensitivityQueue。
type SensitivityQueue struct {
	base   string
	client *http.Client
}

var _ analysis.SensitivityQueue = (*SensitivityQueue)(nil)

// NewSensitivityQueue 构造父作业适配器；base 是 orchestration 的根 URL。
func NewSensitivityQueue(base string, client *http.Client) *SensitivityQueue {
	return &SensitivityQueue{base: strings.TrimRight(base, "/"), client: clientOr(client)}
}

type sensitivitySubmitPayload struct {
	IdempotencyKey string                     `json:"idempotency_key"`
	Kind           string                     `json:"kind"`
	Batch          sensitivitySubmitBatchView `json:"batch"`
}

type sensitivitySubmitBatchView struct {
	BaseRunSpec map[string]string    `json:"base_run_spec"`
	Grid        map[string][]float64 `json:"grid"`
}

// Submit 提交一个 batch 父作业。幂等键由 report_id 派生：同一份报告的重试
// 不会造出第二个父作业（orchestration 侧还会按 plan_hash 兜底）。
func (q *SensitivityQueue) Submit(ctx context.Context, job analysis.SensitivityJob) (string, error) {
	grid := make(map[string][]float64, len(job.Grid))
	for _, p := range job.Grid {
		grid[p.Name] = append([]float64(nil), p.Values...)
	}
	body, err := json.Marshal(sensitivitySubmitPayload{
		IdempotencyKey: "analysis-sensitivity:" + job.ReportID,
		Kind:           "batch",
		Batch: sensitivitySubmitBatchView{
			BaseRunSpec: map[string]string{"run_id": job.RunID},
			Grid:        grid,
		},
	})
	if err != nil {
		return "", fmt.Errorf("%w: encode sensitivity submit: %v", analysis.ErrDependencyUnavailable, err)
	}
	endpoint := joinURL(q.base, "/api/runs/parents")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%w: build sensitivity submit: %v", analysis.ErrDependencyUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := q.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: submit sensitivity parent: %v", analysis.ErrDependencyUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 202 = 新建，200 = 同计划幂等重放；两者都拿同一个 parent_job_id。
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: submit sensitivity parent: upstream %s: %s",
			analysis.ErrDependencyUnavailable, resp.Status, readErrorBody(resp.Body))
	}
	var out struct {
		ParentJobID string `json:"parent_job_id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return "", fmt.Errorf("%w: decode sensitivity submit: %v", analysis.ErrDependencyUnavailable, err)
	}
	if strings.TrimSpace(out.ParentJobID) == "" {
		return "", fmt.Errorf("%w: sensitivity submit returned an empty parent_job_id", analysis.ErrDependencyUnavailable)
	}
	return out.ParentJobID, nil
}

type sensitivityParentWire struct {
	Status string `json:"status"`
	Total  int    `json:"total"`
	Counts struct {
		Completed int `json:"completed"`
		Invalid   int `json:"invalid"`
		Failed    int `json:"failed"`
		Pending   int `json:"pending"`
		Settled   int `json:"settled"`
	} `json:"counts"`
	Children []sensitivityChildWire `json:"children"`
	Summary  *struct {
		Tasks []sensitivityTaskWire `json:"Tasks"`
	} `json:"summary"`
}

type sensitivityChildWire struct {
	RunID        string  `json:"run_id"`
	Status       string  `json:"status"`
	ResultStatus *string `json:"result_status"`
	Error        string  `json:"error"`
}

type sensitivityTaskWire struct {
	RunID        string `json:"run_id"`
	ResultStatus string `json:"result_status"`
	Error        string `json:"error"`
}

// Status 读父作业的进度 / 逐点结论。
func (q *SensitivityQueue) Status(ctx context.Context, parentJobID string) (analysis.SensitivityProgress, error) {
	endpoint := joinURL(q.base, "/api/runs/parents/"+url.PathEscape(parentJobID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return analysis.SensitivityProgress{}, fmt.Errorf("%w: build parent status: %v", analysis.ErrDependencyUnavailable, err)
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return analysis.SensitivityProgress{}, fmt.Errorf("%w: get parent status: %v", analysis.ErrDependencyUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return analysis.SensitivityProgress{}, fmt.Errorf("%w: %s", analysis.ErrSensitivityParentNotFound, parentJobID)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return analysis.SensitivityProgress{}, fmt.Errorf("%w: get parent status: upstream %s: %s",
			analysis.ErrDependencyUnavailable, resp.Status, readErrorBody(resp.Body))
	}
	var wire sensitivityParentWire
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&wire); err != nil {
		return analysis.SensitivityProgress{}, fmt.Errorf("%w: decode parent status: %v", analysis.ErrDependencyUnavailable, err)
	}

	progress := analysis.SensitivityProgress{
		Status:  wire.Status,
		Total:   wire.Total,
		Settled: wire.Counts.Settled,
	}
	// summary.Tasks 是「已汇总」的权威顺序；children 只在还没汇总时可用。
	// 两者都有时以 tasks 为准（它是父记录落定的结论，children 是队列现状）。
	if wire.Summary != nil && len(wire.Summary.Tasks) > 0 {
		progress.Points = make([]analysis.SensitivityChild, 0, len(wire.Summary.Tasks))
		for _, task := range wire.Summary.Tasks {
			progress.Points = append(progress.Points, analysis.SensitivityChild{
				RunID:        task.RunID,
				ResultStatus: analysis.ResultStatus(task.ResultStatus),
				Error:        task.Error,
			})
		}
		return progress, nil
	}
	progress.Points = make([]analysis.SensitivityChild, 0, len(wire.Children))
	for _, child := range wire.Children {
		var result analysis.ResultStatus
		if child.ResultStatus != nil {
			result = analysis.ResultStatus(*child.ResultStatus)
		}
		progress.Points = append(progress.Points, analysis.SensitivityChild{
			RunID:        child.RunID,
			Status:       child.Status,
			ResultStatus: result,
			Error:        child.Error,
		})
	}
	return progress, nil
}
