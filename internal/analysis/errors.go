package analysis

import (
	"errors"
	"fmt"
)

// 领域哨兵。wire code 的唯一词表是 docs/api-schema.md §2；新增 code 必须先改文档。
var (
	ErrRunNotFound              = errors.New("run not found")
	ErrRunInvalid               = errors.New("run result invalid")
	ErrRunFailed                = errors.New("run result failed")
	ErrRunIncomplete            = errors.New("run result incomplete")
	ErrBenchmarkNotRegistered   = errors.New("benchmark not registered")
	ErrBenchmarkDataUnavailable = errors.New("benchmark data unavailable")
	ErrReportNotFound           = errors.New("report not found")
	ErrReportNotReady           = errors.New("report not ready")
	ErrReportConflict           = errors.New("report conflict")
	ErrMethodNotSupported       = errors.New("method not supported")
	ErrSeedRequired             = errors.New("seed required")
	ErrInvalidParams            = errors.New("invalid params")
	ErrDependencyUnavailable    = errors.New("dependency unavailable")
	// ErrSensitivityParentNotFound 是父作业不存在（终态失败，不是可重试的部署问题）。
	ErrSensitivityParentNotFound = errors.New("sensitivity parent job not found")
)

// InvalidReasonsError 携带上游的 invalid_reasons（K9 原样透传）。
type InvalidReasonsError struct {
	Reasons []string
}

func (e *InvalidReasonsError) Error() string {
	return fmt.Sprintf("%s: %v", ErrRunInvalid, e.Reasons)
}

// Unwrap 让 errors.Is(err, ErrRunInvalid) 成立。
func (e *InvalidReasonsError) Unwrap() error { return ErrRunInvalid }

// FailedError 携带上游的 error 原文（K9 原样透传）。
type FailedError struct {
	Message string
}

func (e *FailedError) Error() string {
	return fmt.Sprintf("%s: %s", ErrRunFailed, e.Message)
}

func (e *FailedError) Unwrap() error { return ErrRunFailed }

// IncompleteError 点名缺哪个输入（不做猜测、不产半份报告）。
//
// Missing 是稳定标识（词表 = docs/api-schema.md §6.3），会原样进 wire 的
// `missing`；Detail 只做人读定位，不进 wire。
type IncompleteError struct {
	Missing string
	Detail  string
}

func (e *IncompleteError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%s: missing %s", ErrRunIncomplete, e.Missing)
	}
	return fmt.Sprintf("%s: missing %s (%s)", ErrRunIncomplete, e.Missing, e.Detail)
}

func (e *IncompleteError) Unwrap() error { return ErrRunIncomplete }

// invalidf 构造 400 invalid_params。
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidParams, fmt.Sprintf(format, args...))
}
