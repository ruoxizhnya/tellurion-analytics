package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// MaxSensitivityPoints 是单张网格的点数上限（与 orchestration 的
// `plan.MaxBatchTasks` 同值 100）：父作业按任务数拒绝，本仓在提交前就拦住，
// 避免把一个注定 400 的网格发给上游。
const MaxSensitivityPoints = 100

// expandGrid 把网格展开成笛卡尔积，顺序与 orchestration 的 `plan.ExpandBatch`
// 一致：参数名按字典序，最后一维变化最快。
//
// 这个顺序是「点 ↔ 子 run」绑定的唯一依据（父汇总的 tasks 顺序 = 计划顺序）；
// 两边顺序不一致时由 `finalizeSensitivity` 的总数 / 结果状态校验兜底，**不按
// run_id 猜**。
func expandGrid(grid []GridParam) ([]map[string]float64, error) {
	if len(grid) == 0 {
		return nil, invalidf("param_grid is required")
	}
	byName := make(map[string][]float64, len(grid))
	names := make([]string, 0, len(grid))
	for _, p := range grid {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return nil, invalidf("param_grid name is required")
		}
		if len(p.Values) == 0 {
			return nil, invalidf("param_grid %q has no values", name)
		}
		if _, dup := byName[name]; dup {
			return nil, invalidf("duplicate param_grid name %q", name)
		}
		byName[name] = p.Values
		names = append(names, name)
	}
	sort.Strings(names)

	total := 1
	for _, name := range names {
		if total > MaxSensitivityPoints/len(byName[name]) {
			return nil, invalidf("param_grid expands beyond %d points", MaxSensitivityPoints)
		}
		total *= len(byName[name])
	}

	idx := make([]int, len(names))
	out := make([]map[string]float64, 0, total)
	for {
		point := make(map[string]float64, len(names))
		for i, name := range names {
			point[name] = byName[name][idx[i]]
		}
		out = append(out, point)

		pos := len(names) - 1
		for pos >= 0 {
			idx[pos]++
			if idx[pos] < len(byName[names[pos]]) {
				break
			}
			idx[pos] = 0
			pos--
		}
		if pos < 0 {
			break
		}
	}
	return out, nil
}

// advanceSensitivity 读父作业进度并推进报告。
//
// 轮询由 GET 报告驱动（没有后台 goroutine）：进程重启不丢状态，重复读取幂等；
// 瞬时上游故障只保持 pending，不写成 failed（那会把一次网络抖动变成终态）。
func (s *Service) advanceSensitivity(ctx context.Context, rep Report) (Report, error) {
	rec, err := s.store.GetSensitivity(ctx, rep.ReportID)
	if errors.Is(err, ErrReportNotFound) {
		return rep, nil
	}
	if err != nil {
		return Report{}, err
	}
	if rec.ParentJobID == "" || s.queue == nil {
		return rep, nil
	}
	prog, err := s.queue.Status(ctx, rec.ParentJobID)
	if errors.Is(err, ErrSensitivityParentNotFound) {
		return s.failSensitivity(ctx, rep, fmt.Sprintf("parent job %s not found", rec.ParentJobID))
	}
	if err != nil {
		return rep, nil
	}
	if prog.Status != "completed" && prog.Status != "failed" {
		return rep, nil
	}
	return s.finalizeSensitivity(ctx, rep, rec, prog)
}

// finalizeSensitivity 把父作业的逐点结论回填成 `analysis.sensitivity_runs.points`
// 并推进报告状态。
//
// 三条 fail-closed（缺 point 不补齐、不猜形状）：
//   - 父作业的 total 与本地展开的点数不一致；
//   - 父作业给出的点少于计划（或某个点没有 K9 result_status）；
//   - 汇总已完成但本仓取不到某个点的结果 / 指标。
//
// 前两条**不写 points**（半张网格比没有更危险）；任一点非 completed 时写满
// 逐点结论（含逐点 error）但报告落 failed，绝不产出「看起来完整」的网格。
func (s *Service) finalizeSensitivity(ctx context.Context, rep Report, rec SensitivityRecord, prog SensitivityProgress) (Report, error) {
	var grid []GridParam
	if err := json.Unmarshal(rec.Grid, &grid); err != nil {
		return s.failSensitivity(ctx, rep, "stored param_grid is not decodable: "+err.Error())
	}
	planned, err := expandGrid(grid)
	if err != nil {
		return s.failSensitivity(ctx, rep, "stored param_grid is invalid: "+err.Error())
	}
	if prog.Total != len(planned) {
		return s.failSensitivity(ctx, rep, fmt.Sprintf(
			"parent total %d does not match the planned %d points", prog.Total, len(planned)))
	}
	if len(prog.Points) != len(planned) {
		return s.failSensitivity(ctx, rep, fmt.Sprintf(
			"parent returned %d points for %d planned; refusing to synthesize the missing ones",
			len(prog.Points), len(planned)))
	}

	points := make([]SensitivityPoint, len(planned))
	counts := SensitivityCounts{Total: len(planned)}
	failures := make([]string, 0)
	for i, params := range planned {
		child := prog.Points[i]
		point := SensitivityPoint{Params: params, RunID: child.RunID, Status: child.ResultStatus}
		switch child.ResultStatus {
		case ResultCompleted:
			if s.results == nil {
				return Report{}, fmt.Errorf("%w: result reader is not wired", ErrDependencyUnavailable)
			}
			resp, err := s.results.GetResult(ctx, child.RunID)
			if err != nil {
				if errors.Is(err, ErrDependencyUnavailable) {
					return Report{}, err
				}
				point.Status, point.Error = pointStatusFromResultError(err)
				if point.Error == "" {
					point.Error = err.Error()
				}
				break
			}
			metrics, err := buildMetricsPayload(resp)
			if err != nil {
				point.Status = ResultFailed
				point.Error = "metrics: " + err.Error()
				break
			}
			point.Metrics = &metrics
		case ResultInvalid:
			point.Error = child.Error
			if point.Error == "" {
				point.Error = "child run invalid"
			}
		case ResultFailed:
			point.Error = child.Error
			if point.Error == "" {
				point.Error = "child run failed"
			}
		default:
			return s.failSensitivity(ctx, rep, fmt.Sprintf(
				"point %d (%s) has no K9 result_status; refusing to synthesize a point",
				i+1, paramString(params)))
		}

		switch point.Status {
		case ResultCompleted:
			counts.Completed++
		case ResultInvalid:
			counts.Invalid++
		case ResultFailed:
			counts.Failed++
		}
		if point.Status != ResultCompleted {
			failures = append(failures, fmt.Sprintf("point %d (%s): %s", i+1, paramString(params), point.Error))
		}
		points[i] = point
	}

	body, err := json.Marshal(points)
	if err != nil {
		return Report{}, fmt.Errorf("analysis: marshal sensitivity points: %w", err)
	}
	rec.Points = body
	if err := s.store.PutSensitivity(ctx, rec); err != nil {
		return Report{}, err
	}
	if len(failures) > 0 {
		return s.failSensitivity(ctx, rep, "sensitivity grid failed: "+strings.Join(failures, "; "))
	}

	payload, err := json.Marshal(SensitivityPayload{
		ParentJobID: rec.ParentJobID,
		Grid:        grid,
		Points:      points,
		Counts:      counts,
	})
	if err != nil {
		return Report{}, fmt.Errorf("analysis: marshal sensitivity report: %w", err)
	}
	now := s.now().UTC()
	rep.Status = ReportCompleted
	rep.Body = payload
	rep.UpdatedAt = now
	if err := s.store.PutReport(ctx, rep); err != nil {
		return Report{}, err
	}
	return rep, nil
}

// failSensitivity 把报告推进到 failed（points 保持原样：要么没有，要么是完整
// 的逐点结论 + 逐点 error）。
func (s *Service) failSensitivity(ctx context.Context, rep Report, msg string) (Report, error) {
	rep.Status = ReportFailed
	rep.Error = msg
	rep.UpdatedAt = s.now().UTC()
	rep.Body = nil
	if err := s.store.PutReport(ctx, rep); err != nil {
		return Report{}, err
	}
	return rep, nil
}

// paramString 把一组参数渲染成确定性文本（键按字典序），用于逐点错误定位。
func paramString(params map[string]float64) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, params[k]))
	}
	return strings.Join(parts, ",")
}

// pointStatusFromResultError 把结果读取错误映射回 K9 三值（不把 invalid 折叠成 failed）。
func pointStatusFromResultError(err error) (ResultStatus, string) {
	var invalid *InvalidReasonsError
	if errors.As(err, &invalid) {
		return ResultInvalid, strings.Join(invalid.Reasons, "; ")
	}
	var failed *FailedError
	if errors.As(err, &failed) {
		return ResultFailed, failed.Message
	}
	return ResultFailed, err.Error()
}
