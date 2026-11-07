package analysis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestG2OnlyAnalysisSchemaIsWritable 是 SVC-AN-G2 ③② 的跨 schema 写护栏：
// 把 AGENTS.md §3「上游只读 / 只写自己的 analysis.*」从口头约定变成判据。
//
// 判据：扫本仓所有 .go 文件里的**字符串字面量**（本仓风格是整条 SQL 放一个
// 反引号字面量），凡是写语句（INSERT / UPDATE / DELETE / TRUNCATE / DDL）：
//   - 出现禁止的 schema 限定（quant / market / run / risk / strategy / ai /
//     broker / public）⇒ 红；
//   - 数据写语句的目标未限定 schema ⇒ 红（解析到哪个 schema 取决于
//     search_path，不能算「只写 analysis.*」）。
//
// 只读 SELECT 不拦（AGENTS §3 允许按 run_id 拉上游结果，只是不允许写）。
func TestG2OnlyAnalysisSchemaIsWritable(t *testing.T) {
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
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, raw, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			offenders = append(offenders, g2SQLWriteOffenders(rel, fset.Position(lit.Pos()).Line, text)...)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan repo sources: %v", err)
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("本仓只允许写 analysis.*（AGENTS.md §3：上游只读）。\n%s", strings.Join(offenders, "\n"))
	}
}

// g2ForbiddenWriteSchemas 是不许写（连出现都不许）的 schema 名。
var g2ForbiddenWriteSchemas = []string{"quant", "market", "run", "risk", "strategy", "ai", "broker", "public"}

var (
	g2WriteVerbRe = regexp.MustCompile(`(?i)\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM|TRUNCATE|MERGE\s+INTO|CREATE\s+TABLE|CREATE\s+(?:UNIQUE\s+)?INDEX|CREATE\s+SCHEMA|CREATE\s+TRIGGER|CREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION|ALTER\s+TABLE|DROP\s+(?:TABLE|TRIGGER|FUNCTION))\b`)

	g2ForbiddenRefRe = regexp.MustCompile(`(?i)\b(?:` + strings.Join(g2ForbiddenWriteSchemas, "|") + `)\s*\.`)

	// 数据写语句的目标表；UPDATE 用 `… SET` 收敛，避免把
	// `CREATE TRIGGER … BEFORE UPDATE ON …` 里的 UPDATE 当成写目标。
	g2DataWriteTargetPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bINSERT\s+INTO\s+([A-Za-z_][A-Za-z0-9_$]*(?:\.[A-Za-z_][A-Za-z0-9_$]*)?)`),
		regexp.MustCompile(`(?i)\bUPDATE\s+([A-Za-z_][A-Za-z0-9_$]*(?:\.[A-Za-z_][A-Za-z0-9_$]*)?)\s+SET\b`),
		regexp.MustCompile(`(?i)\bDELETE\s+FROM\s+([A-Za-z_][A-Za-z0-9_$]*(?:\.[A-Za-z_][A-Za-z0-9_$]*)?)`),
		regexp.MustCompile(`(?i)\bTRUNCATE(?:\s+TABLE)?\s+([A-Za-z_][A-Za-z0-9_$]*(?:\.[A-Za-z_][A-Za-z0-9_$]*)?)`),
	}
)

// g2SQLWriteOffenders 对单个字符串字面量做判定，返回人类可读的违规行。
func g2SQLWriteOffenders(rel string, line int, text string) []string {
	if !g2WriteVerbRe.MatchString(text) {
		return nil // 只读 SELECT 不拦
	}
	var out []string
	if m := g2ForbiddenRefRe.FindString(text); m != "" {
		out = append(out, fmt.Sprintf("%s:%d 写语句引用了禁止的 schema 限定 %q", rel, line, m))
	}
	for _, re := range g2DataWriteTargetPatterns {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if target := m[1]; !strings.Contains(target, ".") {
				out = append(out, fmt.Sprintf("%s:%d 写语句目标 %q 未限定 schema（必须是 analysis.）", rel, line, target))
			}
		}
	}
	return out
}
