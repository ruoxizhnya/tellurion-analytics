package analysis

import (
	"bytes"
	"context"
	"sort"
	"sync"
)

// Store 是本仓的持久化端口（analysis.* 的唯一 owner）。
//
// 两个实现：PG（生产；docs/TASKS.md QT-43 施工 ② 接线）与 Memory（测试 / 无库
// 本地装配）。端口语义按 DDL 的约束设计：completed 报告不可变；residual /
// seed 必须落库。
type Store interface {
	GetReport(ctx context.Context, reportID string) (Report, error)
	PutReport(ctx context.Context, report Report) error
	RegisterBenchmark(ctx context.Context, ref BenchmarkRef) error
	LatestBenchmark(ctx context.Context, code string) (BenchmarkRef, error)
	PutAttribution(ctx context.Context, rec AttributionRecord) error
	PutMonteCarlo(ctx context.Context, rec MonteCarloRecord) error
	PutSensitivity(ctx context.Context, rec SensitivityRecord) error
	// GetSensitivity 未知 report_id → ErrReportNotFound。
	GetSensitivity(ctx context.Context, reportID string) (SensitivityRecord, error)
}

// Memory 是进程内 Store（测试与本地装配用，不作为生产持久化）。
type Memory struct {
	mu           sync.RWMutex
	reports      map[string]Report
	benchmarks   []BenchmarkRef
	attributions map[string]AttributionRecord
	mcRuns       map[string]MonteCarloRecord
	sensitivity  map[string]SensitivityRecord
}

// NewMemory 构造空的内存 Store。
func NewMemory() *Memory {
	return &Memory{
		reports:      make(map[string]Report),
		attributions: make(map[string]AttributionRecord),
		mcRuns:       make(map[string]MonteCarloRecord),
		sensitivity:  make(map[string]SensitivityRecord),
	}
}

// GetReport 返回报告深拷贝。
func (m *Memory) GetReport(_ context.Context, reportID string) (Report, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rep, ok := m.reports[reportID]
	if !ok {
		return Report{}, ErrReportNotFound
	}
	return rep.Clone(), nil
}

// PutReport 写入 / 推进报告状态。
//
// completed 是终态：同键同内容 = no-op，内容不同 = ErrReportConflict（fail-loud）。
// pending / running 可以被推进成 completed / failed。
func (m *Memory) PutReport(_ context.Context, report Report) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.reports[report.ReportID]; ok {
		if existing.Status == ReportCompleted {
			if existing.Status != report.Status || !bytes.Equal(existing.Body, report.Body) {
				return ErrReportConflict
			}
			return nil
		}
	}
	m.reports[report.ReportID] = report.Clone()
	return nil
}

// RegisterBenchmark 登记基准。同键重复登记是 no-op（不覆盖 registered_at）。
func (m *Memory) RegisterBenchmark(_ context.Context, ref BenchmarkRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.benchmarks {
		b := m.benchmarks[i]
		if b.Code == ref.Code && b.AdjustmentBasis == ref.AdjustmentBasis && b.DatasetHash == ref.DatasetHash {
			return nil
		}
	}
	m.benchmarks = append(m.benchmarks, ref)
	return nil
}

// LatestBenchmark 按 code 取「当前」登记：registered_at 最新；同刻按 dataset_hash 定序。
func (m *Memory) LatestBenchmark(_ context.Context, code string) (BenchmarkRef, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	candidates := make([]BenchmarkRef, 0, 1)
	for _, b := range m.benchmarks {
		if b.Code == code {
			candidates = append(candidates, b)
		}
	}
	if len(candidates) == 0 {
		return BenchmarkRef{}, ErrBenchmarkNotRegistered
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].RegisteredAt.Equal(candidates[j].RegisteredAt) {
			return candidates[i].RegisteredAt.After(candidates[j].RegisteredAt)
		}
		return candidates[i].DatasetHash > candidates[j].DatasetHash
	})
	return candidates[0], nil
}

// PutAttribution 落归因记录（residual 必须落库）。
func (m *Memory) PutAttribution(_ context.Context, rec AttributionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.attributions[rec.ReportID]; ok {
		if !bytes.Equal(existing.Contributions, rec.Contributions) || existing.Residual != rec.Residual {
			return ErrReportConflict
		}
		return nil
	}
	rec.Contributions = append([]byte(nil), rec.Contributions...)
	m.attributions[rec.ReportID] = rec
	return nil
}

// PutMonteCarlo 落蒙特卡洛批次（seed 必填）。
func (m *Memory) PutMonteCarlo(_ context.Context, rec MonteCarloRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.mcRuns[rec.ReportID]; ok {
		if existing.Seed != rec.Seed || existing.N != rec.N || existing.Method != rec.Method {
			return ErrReportConflict
		}
		return nil
	}
	rec.Summary = append([]byte(nil), rec.Summary...)
	m.mcRuns[rec.ReportID] = rec
	return nil
}

// PutSensitivity 落敏感性网格行：points 只能从「空」写满一次，之后不可变。
func (m *Memory) PutSensitivity(_ context.Context, rec SensitivityRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.sensitivity[rec.ReportID]
	if !ok {
		rec.Grid = append([]byte(nil), rec.Grid...)
		rec.Points = append([]byte(nil), rec.Points...)
		m.sensitivity[rec.ReportID] = rec
		return nil
	}
	if existing.RunID != rec.RunID || !bytes.Equal(existing.Grid, rec.Grid) {
		return ErrReportConflict
	}
	if len(rec.Points) == 0 {
		// 只允许同一父作业的重放；换父作业说明计划身份变了。
		if existing.ParentJobID != rec.ParentJobID {
			return ErrReportConflict
		}
		return nil
	}
	if len(existing.Points) > 0 {
		if !bytes.Equal(existing.Points, rec.Points) || existing.ParentJobID != rec.ParentJobID {
			return ErrReportConflict
		}
		return nil
	}
	existing.Points = append([]byte(nil), rec.Points...)
	existing.ParentJobID = rec.ParentJobID
	m.sensitivity[rec.ReportID] = existing
	return nil
}

// GetSensitivity 读网格行。
func (m *Memory) GetSensitivity(_ context.Context, reportID string) (SensitivityRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.sensitivity[reportID]
	if !ok {
		return SensitivityRecord{}, ErrReportNotFound
	}
	rec.Grid = append([]byte(nil), rec.Grid...)
	rec.Points = append([]byte(nil), rec.Points...)
	return rec, nil
}
