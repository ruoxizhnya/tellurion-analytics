package analysis

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// reportIDDomain 是 report_id 哈希的域标签（防止与其他仓的哈希串用）。
const reportIDDomain = "tellurion.analysis_report_id.v1"

// ReportID 派生确定性报告身份。
//
// 构成 frozen（docs/ARCHITECTURE.md §9.6）：(run_id, analysis_type, params_hash,
// input_hash, seed, metrics_version)。同键 ⇒ 同 report_id ⇒ 复用已存报告。
func ReportID(runID string, typ AnalysisType, paramsHash, inputHash string, seed *int64, metricsVersion string) string {
	h := sha256.New()
	writeHashField(h, reportIDDomain)
	writeHashField(h, string(typ))
	writeHashField(h, runID)
	writeHashField(h, paramsHash)
	writeHashField(h, inputHash)
	writeHashField(h, seedCanonical(seed))
	writeHashField(h, metricsVersion)
	return hex.EncodeToString(h.Sum(nil))
}

// seedCanonical 把 *int64 编码成无歧义的字符串：nil ≠ 0（F6）。
func seedCanonical(seed *int64) string {
	if seed == nil {
		return "nil"
	}
	return fmt.Sprintf("s:%d", *seed)
}

// hashJSON 对任意可序列化值做 canonical（结构体字段序固定）SHA-256。
func hashJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// hashInputs 把多个输入哈希/原值拼成一个 input_hash（长度前缀，避免歧义）。
func hashInputs(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		writeHashField(h, p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeHashField(h interface{ Write([]byte) (int, error) }, s string) {
	fmt.Fprintf(h, "%d:%s\n", len(s), s)
}
