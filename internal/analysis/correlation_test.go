package analysis

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

// curveResult 构造 n 个净值点且收益序列可指定。
func curveResult(id string, returns []float64) *BacktestResponse {
	values := make([]PortfolioValue, 0, len(returns)+1)
	value := 100.0
	values = append(values, PortfolioValue{Date: baseDate, TotalValue: value})
	for i, r := range returns {
		value *= 1 + r
		values = append(values, PortfolioValue{Date: baseDate.AddDate(0, 0, i+1), TotalValue: value})
	}
	return &BacktestResponse{ID: id, Status: ResultCompleted, PortfolioValues: values}
}

func alternatingReturns(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = 0.01
		} else {
			out[i] = -0.005
		}
	}
	return out
}

func TestBuildCorrelationMatrix(t *testing.T) {
	returns := alternatingReturns(35)
	results := map[string]*BacktestResponse{
		"a": curveResult("a", returns),
		"b": curveResult("b", returns),
		"c": curveResult("c", negate(returns)),
	}
	payload, err := buildCorrelationPayload([]string{"a", "b", "c"}, results)
	if err != nil {
		t.Fatalf("buildCorrelationPayload: %v", err)
	}
	if len(payload.Strategies) != 3 {
		t.Fatalf("strategies = %v", payload.Strategies)
	}
	// 名字排序
	for i := 1; i < len(payload.Strategies); i++ {
		if payload.Strategies[i-1] > payload.Strategies[i] {
			t.Fatalf("strategies not sorted: %v", payload.Strategies)
		}
	}
	for i := range payload.Correlations {
		if payload.Correlations[i][i] != 1.0 {
			t.Fatalf("diagonal[%d] = %v, want 1.0", i, payload.Correlations[i][i])
		}
		for j := range payload.Correlations[i] {
			if payload.Correlations[i][j] != payload.Correlations[j][i] {
				t.Fatalf("matrix not symmetric at (%d,%d)", i, j)
			}
		}
	}
	if math.Abs(payload.Correlations[0][1]-1.0) > 1e-12 {
		t.Fatalf("a vs b = %v, want 1.0", payload.Correlations[0][1])
	}
	if math.Abs(payload.Correlations[0][2]+1.0) > 1e-12 {
		t.Fatalf("a vs c = %v, want -1.0", payload.Correlations[0][2])
	}
}

func negate(in []float64) []float64 {
	out := make([]float64, len(in))
	for i, v := range in {
		out[i] = -v
	}
	return out
}

func TestBuildCorrelationTruncatesToShortest(t *testing.T) {
	results := map[string]*BacktestResponse{
		"a": curveResult("a", alternatingReturns(35)),
		"b": curveResult("b", alternatingReturns(40)),
	}
	payload, err := buildCorrelationPayload([]string{"a", "b"}, results)
	if err != nil {
		t.Fatalf("buildCorrelationPayload: %v", err)
	}
	if math.Abs(payload.Correlations[0][1]-1.0) > 1e-12 {
		t.Fatalf("truncated correlation = %v", payload.Correlations[0][1])
	}
}

func TestBuildCorrelationRequires30Points(t *testing.T) {
	// 29 个净值点 ⇒ 28 个收益点，低于下限
	results := map[string]*BacktestResponse{
		"a": curveResult("a", alternatingReturns(28)),
		"b": curveResult("b", alternatingReturns(28)),
	}
	_, err := buildCorrelationPayload([]string{"a", "b"}, results)
	if !errors.Is(err, ErrRunIncomplete) {
		t.Fatalf("want ErrRunIncomplete, got %v", err)
	}

	// 恰好 30 个净值点可算
	results = map[string]*BacktestResponse{
		"a": curveResult("a", alternatingReturns(29)),
		"b": curveResult("b", alternatingReturns(29)),
	}
	if _, err := buildCorrelationPayload([]string{"a", "b"}, results); err != nil {
		t.Fatalf("30 points should pass: %v", err)
	}
}

func TestBuildCorrelationRejectsDegenerateSeries(t *testing.T) {
	// 每日收益恒为 0（净值不变）⇒ 方差为 0 ⇒ Pearson 未定义
	constant := make([]float64, 35)
	results := map[string]*BacktestResponse{
		"a": curveResult("a", constant),
		"b": curveResult("b", alternatingReturns(35)),
	}
	_, err := buildCorrelationPayload([]string{"a", "b"}, results)
	if !errors.Is(err, ErrRunIncomplete) {
		t.Fatalf("want ErrRunIncomplete, got %v", err)
	}
}

func TestServiceCorrelationFailClosed(t *testing.T) {
	results := map[string]*BacktestResponse{
		"a":   curveResult("a", alternatingReturns(35)),
		"b":   curveResult("b", alternatingReturns(35)),
		"bad": {Status: ResultInvalid, InvalidReasons: []string{"garbage_metric"}},
	}
	reader := &fakeReader{results: results}
	svc := newTestService(t, Options{Results: reader})

	if _, err := svc.Correlation(t.Context(), []string{"a"}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("single id: want ErrInvalidParams, got %v", err)
	}
	// 去重后只剩 1 个
	if _, err := svc.Correlation(t.Context(), []string{"a", "a"}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("dedupe: want ErrInvalidParams, got %v", err)
	}
	tooMany := make([]string, 0, MaxCorrelationIDs+1)
	for i := 0; i <= MaxCorrelationIDs; i++ {
		tooMany = append(tooMany, fmt.Sprintf("r%d", i))
	}
	if _, err := svc.Correlation(t.Context(), tooMany); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("too many: want ErrInvalidParams, got %v", err)
	}

	// 任一 run 不存在 / invalid ⇒ 整体拒绝，不返回部分矩阵
	_, err := svc.Correlation(t.Context(), []string{"a", "missing"})
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("missing run: want ErrRunNotFound, got %v", err)
	}
	var invalid *InvalidReasonsError
	if _, err := svc.Correlation(t.Context(), []string{"a", "bad"}); !errors.As(err, &invalid) {
		t.Fatalf("invalid run: want InvalidReasonsError, got %v", err)
	}
	if _, err := svc.Correlation(t.Context(), []string{"a", "b"}); err != nil {
		t.Fatalf("valid runs: %v", err)
	}
}

func TestDedupeIDsPreservesOrder(t *testing.T) {
	got := dedupeIDs([]string{"b", "a", "b", "c", "a"})
	want := []string{"b", "a", "c"}
	if len(got) != len(want) {
		t.Fatalf("dedupe = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupe = %v, want %v", got, want)
		}
	}
}
