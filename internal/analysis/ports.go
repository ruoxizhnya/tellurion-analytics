package analysis

import (
	"context"
	"time"
)

// ResultReader 是上游回测结果的只读端口（docs/ARCHITECTURE.md §9.2）。
//
// K9-correct 的持久读取面尚未在 core 落地（§9.1 F1 / §9.7 R1），所以本仓只定义
// 消费端端口：实现方必须把三值映射成下面的哨兵，**不得**把 invalid / failed
// 当 completed 返回，也不得让本仓解析错误串还原状态。
type ResultReader interface {
	// GetResult 返回三值原样透传的结果。
	// 错误：ErrRunNotFound / ErrRunIncomplete（pending/running 也归这一类）/
	// *InvalidReasonsError / *FailedError。
	GetResult(ctx context.Context, runID string) (*BacktestResponse, error)
}

// BenchmarkReader 是基准序列的只读端口。基准行情归数据服务；本仓只读。
//
// 实现方不得回落到「用价格指数冒充全收益」：basis 不匹配就报
// ErrBenchmarkDataUnavailable。
type BenchmarkReader interface {
	GetSeries(ctx context.Context, code, basis string, start, end time.Time) (BenchmarkSeries, error)
}

// HoldingsProvider 提供 Brinson 需要的期末持仓 artifact（§9.7 R2 未落地）。
// 取不到时必须返回 *IncompleteError（点名 holdings），不得用成交明细倒推。
type HoldingsProvider interface {
	GetPortfolioSnapshot(ctx context.Context, runID string) (PortfolioSnapshot, error)
}

// FactorExposureProvider 提供因子归因需要的逐因子暴露与因子收益（§9.7 R2 未落地）。
type FactorExposureProvider interface {
	GetFactorExposures(ctx context.Context, runID string) (FactorExposures, error)
}

// GridParam 是敏感性网格的一维。
type GridParam struct {
	Name   string    `json:"name"`
	Values []float64 `json:"values"`
}

// SensitivityJob 是提交给 X5（QT-44）的作业。
type SensitivityJob struct {
	ReportID string      `json:"report_id"`
	RunID    string      `json:"run_id"`
	Grid     []GridParam `json:"param_grid"`
}

// SensitivityQueue 是 X5 job 队列的消费端端口（本仓只提交，不跑回测循环）。
// 未接线时服务必须 fail-closed（503 dependency_unavailable）。
type SensitivityQueue interface {
	Submit(ctx context.Context, job SensitivityJob) (jobID string, err error)
}

// Executor 执行本仓内部的异步重计算（蒙特卡洛）。
// 测试用 InlineExecutor（同步、确定性），生产用 AsyncExecutor。
type Executor interface {
	Submit(fn func())
}

// InlineExecutor 同步执行（测试专用：让异步路径可观察、可复现）。
type InlineExecutor struct{}

// Submit 立即执行。
func (InlineExecutor) Submit(fn func()) { fn() }

// AsyncExecutor 用独立 goroutine 执行（生产装配）。
type AsyncExecutor struct{}

// Submit 异步执行。
func (AsyncExecutor) Submit(fn func()) { go fn() }
