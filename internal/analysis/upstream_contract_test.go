package analysis

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 上游读取契约（R1 / R2）的 wire 形状由本用例钉住：字段改名 / 删字段 / 改大小写
// 都会红。契约文本是 docs/api-schema.md §6（唯一真相源）；改形状先改文档。
func TestUpstreamContractWireShape(t *testing.T) {
	if UpstreamContractVersion != "v1" {
		t.Fatalf("UpstreamContractVersion = %q；改版本必须同步 docs/api-schema.md §6", UpstreamContractVersion)
	}

	t.Run("R1 result", func(t *testing.T) {
		const golden = `{"id":"r1","status":"completed","start_date":"2026-01-01","end_date":"2026-02-01","total_return":0.1,"annual_return":0.2,"sharpe_ratio":1.2,"sortino_ratio":1.5,"max_drawdown":-0.08,"max_drawdown_date":"2026-01-02","win_rate":0.5,"total_trades":10,"win_trades":5,"lose_trades":5,"avg_holding_days":3.5,"calmar_ratio":2.5,"portfolio_values":[{"date":"2026-01-02T00:00:00Z","total_value":100}]}`
		var out BacktestResponse
		assertWireShape(t, golden, &out)
		if out.Status != ResultCompleted || len(out.PortfolioValues) != 1 {
			t.Fatalf("decoded result = %+v", out)
		}
	})

	t.Run("R2 portfolio snapshot", func(t *testing.T) {
		const golden = `{"holdings":[{"symbol":"A","sector":"tech","weight":0.6,"period_return":0.12}],"benchmark_sector_weights":{"tech":0.5},"benchmark_sector_returns":{"tech":0.08},"benchmark_return":0.09,"dataset_hash":"h1"}`
		var out PortfolioSnapshot
		assertWireShape(t, golden, &out)
		if out.DatasetHash != "h1" || len(out.Holdings) != 1 {
			t.Fatalf("decoded snapshot = %+v", out)
		}
	})

	t.Run("R2 factor exposures", func(t *testing.T) {
		const golden = `{"factors":[{"name":"value","exposure":1,"factor_return":0.06}],"dataset_hash":"f1"}`
		var out FactorExposures
		assertWireShape(t, golden, &out)
		if out.DatasetHash != "f1" || len(out.Factors) != 1 {
			t.Fatalf("decoded exposures = %+v", out)
		}
	})
}

// assertWireShape 与 golden 往返比较：字段名被改 / 被删 ⇒ 解码丢值或重编码多键。
func assertWireShape(t *testing.T, golden string, dst any) {
	t.Helper()
	if err := json.Unmarshal([]byte(golden), dst); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	encoded, err := json.Marshal(dst)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var want, got any
	if err := json.Unmarshal([]byte(golden), &want); err != nil {
		t.Fatalf("decode golden (any): %v", err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode re-encoded (%s): %v", encoded, err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("wire shape drift:\n golden: %s\n got:    %s", golden, encoded)
	}
}

func TestUpstreamContractResultRequiresK9Status(t *testing.T) {
	mustIncompleteMissing(t, validateResultContract(&BacktestResponse{}), "status")
	mustIncompleteMissing(t, validateResultContract(&BacktestResponse{Status: "pending"}), "status")
	if err := validateResultContract(&BacktestResponse{Status: ResultCompleted}); err != nil {
		t.Fatalf("completed must pass shape check, got %v", err)
	}
}

func TestUpstreamContractArtifactFailClosed(t *testing.T) {
	valid := PortfolioSnapshot{
		Holdings:               []Holding{{Symbol: "A", Sector: "tech", Weight: 1, PeriodReturn: 0.1}},
		BenchmarkSectorWeights: map[string]float64{"tech": 1},
		BenchmarkSectorReturns: map[string]float64{"tech": 0.08},
		BenchmarkReturn:        0.08,
		DatasetHash:            "h1",
	}
	if err := validatePortfolioSnapshot(valid); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	missingHash := valid
	missingHash.DatasetHash = ""
	mustIncompleteMissing(t, validatePortfolioSnapshot(missingHash), "holdings.dataset_hash")
	missingWeights := valid
	missingWeights.BenchmarkSectorWeights = nil
	mustIncompleteMissing(t, validatePortfolioSnapshot(missingWeights), "benchmark_sector_weights")
	missingReturns := valid
	missingReturns.BenchmarkSectorReturns = nil
	mustIncompleteMissing(t, validatePortfolioSnapshot(missingReturns), "benchmark_sector_returns")

	exposures := FactorExposures{
		Factors:     []FactorExposure{{Name: "value", Exposure: 1, FactorReturn: 0.05}},
		DatasetHash: "f1",
	}
	if err := validateFactorExposures(exposures); err != nil {
		t.Fatalf("valid exposures rejected: %v", err)
	}
	exposures.DatasetHash = ""
	mustIncompleteMissing(t, validateFactorExposures(exposures), "factor_exposures.dataset_hash")
}

// 缺 dataset_hash 必须在算 input_hash 前拒算：否则同键会复用旧报告（假复用）。
func TestServiceAttributionRequiresArtifactDatasetHash(t *testing.T) {
	store := NewMemory()
	svc := newTestService(t, Options{
		Store:   store,
		Results: &fakeReader{results: map[string]*BacktestResponse{"r1": testResult("r1", 40)}},
		Holdings: &fakeHoldings{snapshot: PortfolioSnapshot{
			Holdings:               []Holding{{Symbol: "A", Sector: "tech", Weight: 1, PeriodReturn: 0.1}},
			BenchmarkSectorWeights: map[string]float64{"tech": 1},
			BenchmarkSectorReturns: map[string]float64{"tech": 0.08},
		}},
		Factors: &fakeFactors{exposures: FactorExposures{
			Factors: []FactorExposure{{Name: "value", Exposure: 1, FactorReturn: 0.05}},
		}},
	})

	_, err := svc.Attribution(t.Context(), AttributionRequest{RunID: "r1", Method: "brinson"})
	mustIncompleteMissing(t, err, "holdings.dataset_hash")

	_, err = svc.Attribution(t.Context(), AttributionRequest{RunID: "r1", Method: "factor"})
	mustIncompleteMissing(t, err, "factor_exposures.dataset_hash")

	if len(store.reports) != 0 {
		t.Fatalf("fail-closed must not persist a report, got %d", len(store.reports))
	}
}

// contractsModulePath 是本仓**不得**依赖的跨仓模块（裁决 ③：DTO owner 留在本仓）。
const contractsModulePath = "github.com/ruoxizhnya/tellurion-contracts"

// TestUpstreamContractOwnerStaysLocal 钉住「DTO 的 owner = 本仓」。
//
// 裁决 ③（tellurion:docs/design/qt-decoupling/cross-repo-decision-record-2026-10-25.md）
// 判定：`analysis` DTO 留在 analytics 本仓，**不进** contracts；迁移的触发条件是
// 「core 或 orchestration 出现 import analytics DTO 的 Go 代码」。
//
// 本用例守的是触发条件的**反面**：本仓自己一旦 import contracts 的任何包，就说明 DTO 的
// owner 已经被挪走（或至少开始依赖别处的 DTO）⇒ 触发条件已命中而裁决没跟着改 ⇒ 红。
//
// 判据从**源码**推导（go/ast 扫全仓 .go 文件的 import），不是手写清单：手写清单会在有人
// 新增一条 import 时过期，而那时守卫还是绿的——那正是最糟的假绿。
func TestUpstreamContractOwnerStaysLocal(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, raw, parser.ImportsOnly)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		for _, imp := range file.Imports {
			p, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil {
				continue
			}
			if p == contractsModulePath || strings.HasPrefix(p, contractsModulePath+"/") {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, filepath.ToSlash(rel)+" → "+p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan repo sources: %v", err)
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("本仓不得 import %s —— DTO 的 owner 是本仓（裁决 ③）。\n"+
			"要迁到 contracts，先改 docs/api-schema.md §6.4 的触发条件与裁决记录，再改这里的判据。\n%s",
			contractsModulePath, strings.Join(offenders, "\n"))
	}
}

// TestUpstreamContractMigrationTriggerDocumented 钉住「第二个使用方的触发条件」必须成文。
//
// 裁决 ③ 把触发条件写死为「core 或 orchestration 出现 import analytics DTO 的 Go 代码」，
// 并明确 **HTTP 交互不算**。这两个要素是**将来**要不要迁 contracts 的唯一判据 —— 只活在
// 某个人的脑子里，就会在第二个使用方出现时被漏掉（DTO 停在两边各一份的窗口）。
//
// 本用例读**文档正文**（docs/api-schema.md §6.4），逐条查两个要素：§6.4 存在、写明
// core / orchestration 的 Go import 措辞、写明 HTTP 交互不算。缺任一条 ⇒ 红。
func TestUpstreamContractMigrationTriggerDocumented(t *testing.T) {
	doc := readRepoFile(t, filepath.Join("..", "..", "docs", "api-schema.md"))
	const section = "### 6.4"
	idx := strings.Index(doc, section)
	if idx < 0 {
		t.Fatalf("docs/api-schema.md 里找不到 %q —— 「第二个使用方的触发条件」必须成文（裁决 ③）。", section)
	}
	body := doc[idx:]
	// 只看本节，别把后面章节的词算进来。
	if end := strings.Index(body[1:], "\n## "); end >= 0 {
		body = body[:end+1]
	}

	for _, want := range []struct {
		needle string
		why    string
	}{
		{"core 或 orchestration", "触发条件必须点名两个候选使用方（core / orchestration）"},
		{"import analytics DTO", "触发条件说的是 Go 代码 import analytics DTO，不是别的形式"},
		{"HTTP 交互", "必须写明 HTTP 交互这条排除项"},
		{"不算", "必须写明「HTTP 交互不算」第二个使用方"},
	} {
		if !strings.Contains(body, want.needle) {
			t.Errorf("docs/api-schema.md %s 段缺少 %q —— %s", section, want.needle, want.why)
		}
	}
}

// readRepoFile 读仓内文件；读不到直接失败（护栏自己失效比没有护栏更糟）。
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}
