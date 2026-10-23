package analysis

import (
	"fmt"
	"sort"
)

// MinEquityPoints 是相关性分析要求的最小净值点数（与 core 同口径）。
// 转成收益后是 MinEquityPoints-1 个点，远高于 Pearson 的最小 2 点。
const MinEquityPoints = 30

// MaxCorrelationIDs 是相关性请求允许的最大 run 数。
const MaxCorrelationIDs = 8

// CorrelationRequest 是 POST /api/analysis/correlation 的入参。
type CorrelationRequest struct {
	RunIDs []string `json:"run_ids"`
}

// CorrelationPayload 是相关性报告体。
// 口径 frozen：Pearson、按名字排序、截断到最短、对角 1.0。
type CorrelationPayload struct {
	Strategies   []string    `json:"strategies"`
	Correlations [][]float64 `json:"correlations"`
}

// buildCorrelationPayload 计算两两 Pearson 相关。
//
// 任一 run 不可用（缺 / invalid / failed / 点数不足 / 收益退化）即整体拒绝，
// **不返回部分矩阵**（少一行会误导）。
func buildCorrelationPayload(ids []string, results map[string]*BacktestResponse) (CorrelationPayload, error) {
	if len(ids) < 2 {
		return CorrelationPayload{}, invalidf("correlation requires at least 2 run_ids")
	}
	if len(ids) > MaxCorrelationIDs {
		return CorrelationPayload{}, invalidf("correlation accepts at most %d run_ids", MaxCorrelationIDs)
	}

	strategies := append([]string(nil), ids...)
	sort.Strings(strategies)

	returnsByStrategy := make(map[string][]float64, len(strategies))
	minLen := -1
	for _, id := range strategies {
		resp, ok := results[id]
		if !ok || resp == nil {
			return CorrelationPayload{}, fmt.Errorf("%w: %s", ErrRunNotFound, id)
		}
		if err := requireResult(resp); err != nil {
			return CorrelationPayload{}, err
		}
		values := make([]float64, 0, len(resp.PortfolioValues))
		for _, pv := range resp.PortfolioValues {
			values = append(values, pv.TotalValue)
		}
		if len(values) < MinEquityPoints {
			return CorrelationPayload{}, &IncompleteError{Missing: fmt.Sprintf("equity_curve of %s (need >= %d points)", id, MinEquityPoints)}
		}
		rets := equityToReturns(values)
		returnsByStrategy[id] = rets
		if minLen < 0 || len(rets) < minLen {
			minLen = len(rets)
		}
	}

	n := len(strategies)
	matrix := make([][]float64, n)
	for i := range matrix {
		matrix[i] = make([]float64, n)
		matrix[i][i] = 1.0
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			c := pearson(returnsByStrategy[strategies[i]][:minLen], returnsByStrategy[strategies[j]][:minLen])
			if c != c { // NaN：任一序列方差为 0
				return CorrelationPayload{}, &IncompleteError{
					Missing: fmt.Sprintf("non-degenerate returns of %s / %s", strategies[i], strategies[j]),
				}
			}
			matrix[i][j] = c
			matrix[j][i] = c
		}
	}
	return CorrelationPayload{Strategies: strategies, Correlations: matrix}, nil
}

// equityToReturns 把净值曲线转成简单收益；前值为 0 ⇒ 该点收益按 0（与 core 同口径）。
func equityToReturns(equity []float64) []float64 {
	if len(equity) < 2 {
		return nil
	}
	out := make([]float64, 0, len(equity)-1)
	for i := 1; i < len(equity); i++ {
		prev := equity[i-1]
		if prev == 0 {
			out = append(out, 0)
			continue
		}
		out = append(out, (equity[i]-prev)/prev)
	}
	return out
}

// requireResult 把上游三值映射成领域哨兵（原样透传，不重判）。
func requireResult(resp *BacktestResponse) error {
	switch resp.Status {
	case ResultCompleted:
		if len(resp.PortfolioValues) == 0 {
			return &IncompleteError{Missing: "portfolio_values"}
		}
		return nil
	case ResultInvalid:
		return &InvalidReasonsError{Reasons: append([]string(nil), resp.InvalidReasons...)}
	case ResultFailed:
		return &FailedError{Message: resp.Error}
	default:
		return &IncompleteError{Missing: "completed run result (status=" + string(resp.Status) + ")"}
	}
}
