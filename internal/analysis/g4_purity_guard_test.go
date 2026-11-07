package analysis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// g4ClockBoundaryFiles 是允许出现 time.Now 的**边界文件**。
//
// 唯一豁免是 service.go：它把注入时钟的零值取成 time.Now，时间戳只进
// created_at / updated_at，不进任何计算结果（计算结果的时间轴来自输入）。
// 其他计算文件（metrics / benchmark / attribution / montecarlo / correlation /
// stats / sensitivity / report / export ...）一律禁止读时钟。
var g4ClockBoundaryFiles = map[string]bool{"service.go": true}

// g4RandFile 是唯一允许 import math/rand 的文件：蒙特卡洛必须显式 seed
// （docs/ARCHITECTURE.md §9.1 F6 / docs/db-schema.md §3 确定性红线）。
const g4RandFile = "montecarlo.go"

// TestG4PureFunctionInvariants 是 SVC-AN-G4 ① 的机械守卫：把 SYSTEM-DESIGN §4
// 「分析纯函数（不读时钟、不读随机，蒙特卡洛必须带 seed）」变成 CI 判据。
//
// 判据（扫 internal/analysis 的全部非测试 .go）：
//   - 出现 `time.Now` ⇒ 红，唯一豁免 = service.go（时钟注入边界）；
//   - 出现 `math/rand` / `math/rand/v2` import ⇒ 红，唯一豁免 = montecarlo.go；
//   - montecarlo.go 内只允许 `rand.New(rand.NewSource(seed))` 派生 rng，
//     使用 `rand.Intn` 之类的包级函数（全局源）⇒ 红；没有显式 seed 派生 ⇒ 红。
//
// 破坏验证：在 metrics.go 临时写一处 time.Now ⇒ 本用例红，还原 ⇒ 绿。
func TestG4PureFunctionInvariants(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	var offenders []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, raw, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if (path == "math/rand" || path == "math/rand/v2") && name != g4RandFile {
				offenders = append(offenders,
					fmt.Sprintf("%s:%d import %q，但只有 %s 允许读随机源（且必须显式 seed）",
						name, fset.Position(imp.Pos()).Line, path, g4RandFile))
			}
		}

		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name == "time" && sel.Sel.Name == "Now" && !g4ClockBoundaryFiles[name] {
				offenders = append(offenders,
					fmt.Sprintf("%s:%d 读取系统时钟 time.Now（计算必须是输入的纯函数）",
						name, fset.Position(sel.Pos()).Line))
			}
			return true
		})

		if name == g4RandFile {
			offenders = append(offenders, g4RandSourceOffenders(fset, file, name)...)
		}
	}

	if len(offenders) > 0 {
		t.Fatalf("本仓计算包必须保持纯函数（不读时钟 / 不读全局随机源；蒙特卡洛 seed 必须显式传入）：\n%s",
			strings.Join(offenders, "\n"))
	}
}

// g4RandSourceOffenders 只允许 montecarlo.go 用显式 seed 派生 rng：
// `rand.New(rand.NewSource(seed))`；包级 rand 函数（全局源）一律红。
func g4RandSourceOffenders(fset *token.FileSet, file *ast.File, name string) []string {
	var out []string
	seeded := false

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "rand" {
			return true
		}

		switch sel.Sel.Name {
		case "New":
			if len(call.Args) == 1 && g4IsSeededSource(call.Args[0]) {
				seeded = true
			}
		case "NewSource":
			if len(call.Args) != 1 || !g4IsSeedIdent(call.Args[0]) {
				out = append(out, fmt.Sprintf("%s:%d rand.NewSource 必须直接接收显式 seed",
					name, fset.Position(sel.Pos()).Line))
			}
		default:
			out = append(out, fmt.Sprintf("%s:%d 使用了 math/rand 包级函数 rand.%s（全局随机源，绕过显式 seed）",
				name, fset.Position(sel.Pos()).Line, sel.Sel.Name))
		}
		return true
	})

	if !seeded {
		out = append(out, fmt.Sprintf("%s 缺少 rand.New(rand.NewSource(seed))：随机抽样必须从显式 seed 派生", name))
	}
	return out
}

// g4IsSeededSource 判断表达式是否为 rand.NewSource(seed)。
func g4IsSeededSource(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "rand" || sel.Sel.Name != "NewSource" || len(call.Args) != 1 {
		return false
	}
	return g4IsSeedIdent(call.Args[0])
}

func g4IsSeedIdent(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "seed"
}
