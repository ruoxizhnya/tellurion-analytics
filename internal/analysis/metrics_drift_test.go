package analysis

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// ADR-039 T2 的检测面：同名指标两侧对拍。
//
// golden fixture 由 core 权威实现
// `tellurion/pkg/backtest/metrics.CalculateMetrics` 在固定 nav 序列上产出
// （2026-11-11 生成），本仓读同一序列用 `buildMetricsPayload` 重算，逐项比较。
//
// 如实标注：fixture 是**合成 nav**（确定性 60 点），不是捕获的真实 run artifact。
// 「真实 run artifact」腿需先有带 seed 数据的真栈（见 TASKS 施工记录的重开条件）；
// 在那之前，本条已经让 T2 从「不可判定」变成「可判定」。
//
// 阈值（ADR-039 D4）：比率类相对差 > 1e-9、金额类绝对差 > 0.01 元判漂移。
const (
	metricDriftRelTol = 1e-9
	metricDriftAbsTol = 0.01

	// goldenFixtureHash 是 fixture 的内容 hash（对 CRLF 归一化为 LF 后计算，
	// 避免 Windows / Linux 检出差异误报）。改 fixture（含重算基线）必须走
	// ADR-039 T2 流程并同步更新这里，否则本测试先拦下。
	goldenFixtureHash = "5d76f530ffbb128ec66c6b5e938625296cee1f64b1393de219d093962821f998"
)

type goldenRunFixture struct {
	Source          string             `json:"source"`
	Kind            string             `json:"kind"`
	GeneratedAt     string             `json:"generated_at"`
	RiskFreeRate    float64            `json:"risk_free_rate"`
	PortfolioValues []PortfolioValue   `json:"portfolio_values"`
	CoreMetrics     map[string]float64 `json:"core_metrics"`
}

// TestComputedMetrics_NoDriftAgainstCoreGolden 断言本仓补算的三个指标与 core 权威值一致。
func TestComputedMetrics_NoDriftAgainstCoreGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "core-run-metrics.golden.json"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	sum := sha256.Sum256(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")))
	if got := hex.EncodeToString(sum[:]); got != goldenFixtureHash {
		t.Fatalf("golden fixture 被改动（%s != %s）；按 ADR-039 T2 重算基线并更新哈希", got, goldenFixtureHash)
	}

	var fx goldenRunFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decode golden fixture: %v", err)
	}
	if fx.Kind != "synthetic_nav" {
		t.Fatalf("golden fixture 来源类别变了：%q（当前只接受 synthetic_nav；真实 run artifact 需按 ADR-039 T2 重新立项）", fx.Kind)
	}
	if len(fx.PortfolioValues) < 2 {
		t.Fatalf("golden fixture 至少要 2 个净值点，实际 %d", len(fx.PortfolioValues))
	}
	if fx.CoreMetrics["volatility"] <= 0 {
		t.Fatalf("golden fixture 缺 core volatility：%+v", fx.CoreMetrics)
	}

	payload, err := buildMetricsPayload(&BacktestResponse{PortfolioValues: fx.PortfolioValues})
	if err != nil {
		t.Fatalf("buildMetricsPayload: %v", err)
	}

	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"volatility", payload.Computed.Volatility, fx.CoreMetrics["volatility"]},
		{"var_95", payload.Computed.VaR95, fx.CoreMetrics["var_95"]},
		{"cvar_95", payload.Computed.CVaR95, fx.CoreMetrics["cvar_95"]},
	} {
		if isMetricDrift(c.got, c.want) {
			t.Fatalf("指标漂移 %s：analytics=%v core=%v（fixture %s，阈值 rel=%g / abs=%g）",
				c.name, c.got, c.want, goldenFixtureHash[:12], metricDriftRelTol, metricDriftAbsTol)
		}
	}
}

// TestIsMetricDrift_Thresholds 钉住判定阈值本身，防止有人把阈值放大来「变绿」。
func TestIsMetricDrift_Thresholds(t *testing.T) {
	cases := []struct {
		name  string
		got   float64
		want  float64
		drift bool
	}{
		{"identical", 0.123456789, 0.123456789, false},
		{"float noise", 0.123456789 + 1e-13, 0.123456789, false},
		{"relative over", 0.123456789 * (1 + 2e-9), 0.123456789, true},
		{"zero vs abs within", 0.009, 0, false},
		{"zero vs abs over", 0.011, 0, true},
	}
	for _, c := range cases {
		if got := isMetricDrift(c.got, c.want); got != c.drift {
			t.Errorf("%s: isMetricDrift(%v, %v) = %v, want %v", c.name, c.got, c.want, got, c.drift)
		}
	}
}

// isMetricDrift 是 ADR-039 T2 的判定：比率类看相对差，基准为 0 时退化为绝对差。
func isMetricDrift(got, want float64) bool {
	if want == 0 {
		return math.Abs(got-want) > metricDriftAbsTol
	}
	return math.Abs(got-want)/math.Abs(want) > metricDriftRelTol
}
