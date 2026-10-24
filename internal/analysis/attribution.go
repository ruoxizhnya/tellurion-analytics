package analysis

import "sort"

// AttributionRequest 是 POST /api/analysis/attribution 的入参。
type AttributionRequest struct {
	RunID  string `json:"run_id"`
	Method string `json:"method"`
}

// Contribution 是一条归因贡献。
//
// factor：Name = 因子名，Contribution = 暴露 × 因子收益。
// brinson：Name = 行业，Allocation / Selection / Interaction 分项，
// Contribution = 三者之和。
type Contribution struct {
	Name            string  `json:"name"`
	Contribution    float64 `json:"contribution"`
	Allocation      float64 `json:"allocation,omitempty"`
	Selection       float64 `json:"selection,omitempty"`
	Interaction     float64 `json:"interaction,omitempty"`
	PortfolioWeight float64 `json:"portfolio_weight,omitempty"`
	BenchmarkWeight float64 `json:"benchmark_weight,omitempty"`
}

// AttributionPayload 是归因报告体。
//
// 不变量：factor 的 Σcontributions + residual == total_return；
// brinson 的 Σ(allocation+selection+interaction) == total_return - benchmark_return
// （容差 1e-8，见 docs/ARCHITECTURE.md §9.4）。residual 不得摊进贡献。
type AttributionPayload struct {
	Method          string         `json:"method"`
	Contributions   []Contribution `json:"contributions"`
	Residual        float64        `json:"residual"`
	TotalReturn     float64        `json:"total_return"`
	BenchmarkReturn *float64       `json:"benchmark_return,omitempty"`
}

// buildFactorAttribution 计算因子归因：贡献 = 暴露 × 因子收益，残差兜差额。
func buildFactorAttribution(resp *BacktestResponse, exposures FactorExposures) (AttributionPayload, error) {
	if err := validateFactorExposures(exposures); err != nil {
		return AttributionPayload{}, err
	}
	factors := append([]FactorExposure(nil), exposures.Factors...)
	sort.Slice(factors, func(i, j int) bool { return factors[i].Name < factors[j].Name })

	contributions := make([]Contribution, 0, len(factors))
	sum := 0.0
	for _, f := range factors {
		c := f.Exposure * f.FactorReturn
		sum += c
		contributions = append(contributions, Contribution{Name: f.Name, Contribution: c})
	}
	return AttributionPayload{
		Method:        "factor",
		Contributions: contributions,
		Residual:      resp.TotalReturn - sum,
		TotalReturn:   resp.TotalReturn,
	}, nil
}

// buildBrinsonAttribution 计算 Brinson-Fachler 归因。
//
// allocation_s = (wp_s - wb_s) × (rb_s - rb)
// selection_s  = wb_s × (rp_s - rb_s)
// interaction_s= (wp_s - wb_s) × (rp_s - rb_s)
// 三者之和 = 组合收益 - 基准收益（行业权重合为 1 时）。
// residual 吸收 artifact 与结果口径的差异，**不摊进**分项。
func buildBrinsonAttribution(resp *BacktestResponse, snapshot PortfolioSnapshot) (AttributionPayload, error) {
	if err := validatePortfolioSnapshot(snapshot); err != nil {
		return AttributionPayload{}, err
	}

	type sectorAgg struct {
		weight float64
		value  float64 // Σ weight × period_return
	}
	agg := make(map[string]*sectorAgg)
	for _, h := range snapshot.Holdings {
		s := h.Sector
		if s == "" {
			s = "(unclassified)"
		}
		cur, ok := agg[s]
		if !ok {
			cur = &sectorAgg{}
			agg[s] = cur
		}
		cur.weight += h.Weight
		cur.value += h.Weight * h.PeriodReturn
	}

	sectors := make([]string, 0, len(agg))
	for s := range agg {
		sectors = append(sectors, s)
	}
	sort.Strings(sectors)

	benchmarkReturn := snapshot.BenchmarkReturn
	contributions := make([]Contribution, 0, len(sectors))
	sum := 0.0
	for _, s := range sectors {
		a := agg[s]
		rp := 0.0
		if a.weight != 0 {
			rp = a.value / a.weight
		}
		wb := snapshot.BenchmarkSectorWeights[s]
		rb := snapshot.BenchmarkSectorReturns[s]
		allocation := (a.weight - wb) * (rb - benchmarkReturn)
		selection := wb * (rp - rb)
		interaction := (a.weight - wb) * (rp - rb)
		sum += allocation + selection + interaction
		contributions = append(contributions, Contribution{
			Name:            s,
			Contribution:    allocation + selection + interaction,
			Allocation:      allocation,
			Selection:       selection,
			Interaction:     interaction,
			PortfolioWeight: a.weight,
			BenchmarkWeight: wb,
		})
	}

	rb := benchmarkReturn
	return AttributionPayload{
		Method:          "brinson",
		Contributions:   contributions,
		Residual:        (resp.TotalReturn - benchmarkReturn) - sum,
		TotalReturn:     resp.TotalReturn,
		BenchmarkReturn: &rb,
	}, nil
}
