package analysis

import (
	"encoding/json"
	"time"
)

// MetricsVersion 是本仓的指标口径版本（docs/ARCHITECTURE.md §9.6）。
// 改既有指标定义 ⇒ 新版本 + ADR；老报告不重算。
const MetricsVersion = "v1"

// ResultStatus 是上游 K9 三值（docs/ARCHITECTURE.md §9.1 F1）。
// 本仓只透传，不重判、不解析错误串。
type ResultStatus string

const (
	ResultCompleted ResultStatus = "completed"
	ResultInvalid   ResultStatus = "invalid"
	ResultFailed    ResultStatus = "failed"
)

// PortfolioValue 是本地镜像的净值点。
//
// 零跨仓依赖：契约仓（tellurion-contracts）的 analysis 包尚未发布，本文件按
// contracts.BacktestResponse 的 wire 形状做最小镜像。契约包落地后迁走，不在这里
// import core。
type PortfolioValue struct {
	Date       time.Time `json:"date"`
	TotalValue float64   `json:"total_value"`
}

// BacktestResponse 是回测结果的只读镜像（字段集见 core 契约，本仓只读）。
type BacktestResponse struct {
	ID              string           `json:"id"`
	Status          ResultStatus     `json:"status"`
	Error           string           `json:"error,omitempty"`
	InvalidReasons  []string         `json:"invalid_reasons,omitempty"`
	StartDate       string           `json:"start_date,omitempty"`
	EndDate         string           `json:"end_date,omitempty"`
	TotalReturn     float64          `json:"total_return,omitempty"`
	AnnualReturn    float64          `json:"annual_return,omitempty"`
	SharpeRatio     float64          `json:"sharpe_ratio,omitempty"`
	SortinoRatio    float64          `json:"sortino_ratio,omitempty"`
	MaxDrawdown     float64          `json:"max_drawdown,omitempty"`
	MaxDrawdownDate string           `json:"max_drawdown_date,omitempty"`
	WinRate         float64          `json:"win_rate,omitempty"`
	TotalTrades     int              `json:"total_trades,omitempty"`
	WinTrades       int              `json:"win_trades,omitempty"`
	LoseTrades      int              `json:"lose_trades,omitempty"`
	AvgHoldingDays  float64          `json:"avg_holding_days,omitempty"`
	CalmarRatio     float64          `json:"calmar_ratio,omitempty"`
	PortfolioValues []PortfolioValue `json:"portfolio_values,omitempty"`
}

// BenchmarkRef 是基准登记身份（analysis.benchmarks 的一行）。
// adjustment_basis 与 dataset_hash 是身份的一部分：改口径 = 新登记。
type BenchmarkRef struct {
	Code            string    `json:"code"`
	AdjustmentBasis string    `json:"adjustment_basis"`
	DatasetHash     string    `json:"dataset_hash"`
	Source          string    `json:"source,omitempty"`
	RegisteredAt    time.Time `json:"registered_at"`
}

// BenchmarkPoint 是基准序列的一个点（价格 / 全收益指数）。
type BenchmarkPoint struct {
	Date  time.Time `json:"date"`
	Value float64   `json:"value"`
}

// BenchmarkSeries 是只读的基准序列读取结果。
type BenchmarkSeries struct {
	Ref    BenchmarkRef
	Points []BenchmarkPoint
}

// Holding 是期末持仓 artifact 的一条（R2 未落地前的临时读取形状）。
type Holding struct {
	Symbol       string  `json:"symbol"`
	Sector       string  `json:"sector"`
	Weight       float64 `json:"weight"`
	PeriodReturn float64 `json:"period_return"`
}

// PortfolioSnapshot 是 Brinson 的输入：组合持仓 + 基准分行业权重与收益。
//
// ⚠️ 形状是**临时**的：docs/ARCHITECTURE.md §9.7 R2 尚未裁决 artifact owner 与
// 最终形状。本仓只定义消费端端口；R2 落地后按裁决调整（不在此处 import core）。
type PortfolioSnapshot struct {
	Holdings               []Holding          `json:"holdings"`
	BenchmarkSectorWeights map[string]float64 `json:"benchmark_sector_weights"`
	BenchmarkSectorReturns map[string]float64 `json:"benchmark_sector_returns"`
	BenchmarkReturn        float64            `json:"benchmark_return"`
	DatasetHash            string             `json:"dataset_hash"`
}

// FactorExposure 是因子归因的一条：暴露 × 因子收益 = 贡献。
type FactorExposure struct {
	Name         string  `json:"name"`
	Exposure     float64 `json:"exposure"`
	FactorReturn float64 `json:"factor_return"`
}

// FactorExposures 是因子归因的输入（R2 未落地前的临时读取形状）。
type FactorExposures struct {
	Factors     []FactorExposure `json:"factors"`
	DatasetHash string           `json:"dataset_hash"`
}

// ReportStatus 是**作业态**，不是 K9 三值（docs/ARCHITECTURE.md §9.6）。
type ReportStatus string

const (
	ReportPending   ReportStatus = "pending"
	ReportRunning   ReportStatus = "running"
	ReportCompleted ReportStatus = "completed"
	ReportFailed    ReportStatus = "failed"
)

// AnalysisType 是报告的分析种类（同时是幂等键的一部分）。
type AnalysisType string

const (
	TypeMetrics     AnalysisType = "metrics"
	TypeBenchmark   AnalysisType = "benchmark"
	TypeAttribution AnalysisType = "attribution"
	TypeSensitivity AnalysisType = "sensitivity"
	TypeMonteCarlo  AnalysisType = "montecarlo"
	TypeCorrelation AnalysisType = "correlation"
)

// Report 是 analysis.reports 的一行。
//
// Body 只在 completed 时非空（DDL 有 CHECK）；Failed 必带 Error。
// ParamsHash / InputHash / Seed 是幂等键的组成部分，也是报告的追溯依据。
type Report struct {
	ReportID       string          `json:"report_id"`
	RunID          string          `json:"run_id"`
	AnalysisType   AnalysisType    `json:"analysis_type"`
	Status         ReportStatus    `json:"status"`
	MetricsVersion string          `json:"metrics_version"`
	Body           json.RawMessage `json:"report,omitempty"`
	Error          string          `json:"error,omitempty"`
	ParamsHash     string          `json:"-"`
	InputHash      string          `json:"-"`
	Seed           *int64          `json:"-"`
	CreatedAt      time.Time       `json:"-"`
	UpdatedAt      time.Time       `json:"-"`
}

// Clone 深拷贝（Memory Store 的边界约定：出库即拷贝）。
func (r Report) Clone() Report {
	out := r
	out.Body = append(json.RawMessage(nil), r.Body...)
	if r.Seed != nil {
		seed := *r.Seed
		out.Seed = &seed
	}
	return out
}

// AttributionRecord 是 analysis.attribution 的一行：residual 必须落库。
type AttributionRecord struct {
	ReportID      string          `json:"report_id"`
	Method        string          `json:"method"`
	Contributions json.RawMessage `json:"contributions"`
	Residual      float64         `json:"residual"`
	InputHash     string          `json:"input_hash"`
	CreatedAt     time.Time       `json:"created_at"`
}

// MonteCarloRecord 是 analysis.mc_runs 的一行：seed 必填。
type MonteCarloRecord struct {
	ReportID  string          `json:"report_id"`
	Seed      int64           `json:"seed"`
	N         int             `json:"n"`
	Method    string          `json:"method"`
	InputHash string          `json:"input_hash"`
	Summary   json.RawMessage `json:"summary"`
	CreatedAt time.Time       `json:"created_at"`
}
