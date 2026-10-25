package analysis

import "strings"

// UpstreamContractVersion 是本仓对上游读取形状的本地契约版本（R1 结果面 / R2 artifact 面）。
//
// 形状冻结在 internal/analysis/model.go，wire 字段与失败语义的唯一真相源是
// docs/api-schema.md §6。**契约化 ≠ 已迁 contracts**：tellurion-contracts 的
// analysis 包尚未发布（docs/TASKS.md QT-43），所以先在本仓按版本冻结；包发布后
// model.go 与本文件一起迁走，端口方法集与校验语义不变。
//
// 改字段名 / 删字段 = breaking（先改 docs/api-schema.md §6）；加字段 = MINOR。
const UpstreamContractVersion = "v1"

// validateResultContract 校验 R1 结果面的形状：status 必须是 K9 三值之一。
//
// core B6-3 之后，读面对每个 run 结论都带 `status`（持久列
// `backtest_jobs.result_status` 是权威）。缺 status 因此不再是「异步结果的旧编码」，
// 而是上游没升级到 R1：照样按 run_result_incomplete 拒算，**不解析错误串**、
// 不当 completed。
func validateResultContract(resp *BacktestResponse) error {
	if resp == nil {
		return &IncompleteError{Missing: "run result"}
	}
	switch resp.Status {
	case ResultCompleted, ResultInvalid, ResultFailed:
		return nil
	}
	status := strings.TrimSpace(string(resp.Status))
	if status == "" {
		return &IncompleteError{Missing: "status", Detail: "upstream result has no status (pre-B6-3 upstream)"}
	}
	return &IncompleteError{Missing: "status", Detail: "unknown status " + status}
}

// validatePortfolioSnapshot 校验 R2 的 Brinson 输入契约（docs/api-schema.md §6.2）。
//
// dataset_hash 必须是必填：它是 report_id 的 input_hash 组成部分，缺了就无法
// 区分 artifact 换版，同键会复用旧结论。行业权重 / 收益同理——缺了会静默按 0
// 计算出一份「看起来正常」的归因，必须 fail-closed。
func validatePortfolioSnapshot(snapshot PortfolioSnapshot) error {
	if len(snapshot.Holdings) == 0 {
		return &IncompleteError{Missing: "holdings"}
	}
	if strings.TrimSpace(snapshot.DatasetHash) == "" {
		return &IncompleteError{Missing: "holdings.dataset_hash"}
	}
	if len(snapshot.BenchmarkSectorWeights) == 0 {
		return &IncompleteError{Missing: "benchmark_sector_weights"}
	}
	if len(snapshot.BenchmarkSectorReturns) == 0 {
		return &IncompleteError{Missing: "benchmark_sector_returns"}
	}
	return nil
}

// validateFactorExposures 校验 R2 的因子归因输入契约（docs/api-schema.md §6.2）。
func validateFactorExposures(exposures FactorExposures) error {
	if len(exposures.Factors) == 0 {
		return &IncompleteError{Missing: "factor_exposures"}
	}
	if strings.TrimSpace(exposures.DatasetHash) == "" {
		return &IncompleteError{Missing: "factor_exposures.dataset_hash"}
	}
	return nil
}
