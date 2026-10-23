package analysis

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strconv"
)

// 导出格式。pdf 未定（docs/ARCHITECTURE.md §9.7 R5），冻结前不承诺。
const (
	FormatHTML = "html"
	FormatCSV  = "csv"
)

// Export 把完成的报告渲染成导出字节。
//
// 同一 report_id + format 的输出字节一致（确定性渲染；不读时钟、不读随机）。
func Export(report Report, format string) (body []byte, contentType, filename string, err error) {
	if report.Status != ReportCompleted {
		return nil, "", "", fmt.Errorf("%w: report status is %s", ErrReportNotReady, report.Status)
	}
	switch format {
	case FormatHTML:
		body, err = renderHTML(report)
		if err != nil {
			return nil, "", "", err
		}
		return body, "text/html; charset=utf-8", report.ReportID + ".html", nil
	case FormatCSV:
		body, err = renderCSV(report)
		if err != nil {
			return nil, "", "", err
		}
		return body, "text/csv; charset=utf-8", report.ReportID + ".csv", nil
	default:
		return nil, "", "", invalidf("unsupported export format %q", format)
	}
}

// renderHTML 输出自包含的静态 HTML（JSON 报告体的可读呈现）。
func renderHTML(report Report) ([]byte, error) {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, report.Body, "", "  "); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\"><head><meta charset=\"utf-8\">\n")
	b.WriteString("<title>analysis report ")
	b.WriteString(html.EscapeString(report.ReportID))
	b.WriteString("</title>\n<style>body{font-family:monospace;margin:2rem}pre{white-space:pre-wrap}</style></head><body>\n")
	b.WriteString("<h1>")
	b.WriteString(html.EscapeString(string(report.AnalysisType)))
	b.WriteString(" report</h1>\n<p>run_id: ")
	b.WriteString(html.EscapeString(report.RunID))
	b.WriteString(" · metrics_version: ")
	b.WriteString(html.EscapeString(report.MetricsVersion))
	b.WriteString("</p>\n<pre>")
	b.WriteString(html.EscapeString(pretty.String()))
	b.WriteString("</pre>\n</body></html>\n")
	return b.Bytes(), nil
}

// renderCSV 把报告体压平成 (field,value) 两列；键路径按字典序，便于 diff。
func renderCSV(report Report) ([]byte, error) {
	var decoded any
	if err := json.Unmarshal(report.Body, &decoded); err != nil {
		return nil, err
	}
	rows := make([][2]string, 0, 32)
	flattenJSON("", decoded, &rows)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i][0] != rows[j][0] {
			return rows[i][0] < rows[j][0]
		}
		return rows[i][1] < rows[j][1]
	})

	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if err := w.Write([]string{"field", "value"}); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if err := w.Write([]string{row[0], row[1]}); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// flattenJSON 把嵌套 JSON 压平成点分路径（数组用 [i]）。
func flattenJSON(prefix string, value any, rows *[][2]string) {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			flattenJSON(path, v[k], rows)
		}
	case []any:
		for i, item := range v {
			flattenJSON(prefix+"["+strconv.Itoa(i)+"]", item, rows)
		}
	case nil:
		*rows = append(*rows, [2]string{prefix, ""})
	case string:
		*rows = append(*rows, [2]string{prefix, v})
	case bool:
		*rows = append(*rows, [2]string{prefix, strconv.FormatBool(v)})
	case float64:
		*rows = append(*rows, [2]string{prefix, strconv.FormatFloat(v, 'g', -1, 64)})
	default:
		*rows = append(*rows, [2]string{prefix, fmt.Sprint(v)})
	}
}
