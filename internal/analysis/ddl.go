package analysis

// migrationStatements 是本仓 DDL 的唯一真相（取向 ①：Go 数组 + 启动时按序执行、
// 幂等、无版本表，与 core / risk 一致）。
//
// 每条都必须能在空库上一次跑完，且可重入。执行入口是 PG.Migrate（由
// cmd/analyticsservice 在启动时调用）；Memory Store 只用于测试与无库本地装配。
// 结构变更必须同步 docs/db-schema.md，结构级变更还要立 ADR。
//
// 注意：reports 的唯一键用 PG15+ 的 NULLS NOT DISTINCT —— 默认唯一约束不拦
// NULL 重复，seed 为 NULL 的非 MC 报告会重复落行。
var migrationStatements = []string{
	`CREATE SCHEMA IF NOT EXISTS analysis`,

	`CREATE TABLE IF NOT EXISTS analysis.reports (
		report_id       TEXT PRIMARY KEY,
		run_id          TEXT NOT NULL,
		analysis_type   TEXT NOT NULL CHECK (analysis_type IN
			('metrics','benchmark','attribution','sensitivity','montecarlo','correlation')),
		status          TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed')),
		metrics_version TEXT NOT NULL,
		params_hash     CHAR(64) NOT NULL,
		input_hash      CHAR(64) NOT NULL,
		seed            BIGINT,
		body            JSONB,
		error           TEXT NOT NULL DEFAULT '',
		created_at      TIMESTAMPTZ NOT NULL,
		updated_at      TIMESTAMPTZ NOT NULL,
		UNIQUE NULLS NOT DISTINCT (run_id, analysis_type, params_hash, input_hash, seed, metrics_version),
		CHECK ((status = 'completed') = (body IS NOT NULL)),
		CHECK (status <> 'failed' OR error <> '')
	)`,

	`CREATE TABLE IF NOT EXISTS analysis.benchmarks (
		code             TEXT NOT NULL,
		adjustment_basis TEXT NOT NULL,
		dataset_hash     CHAR(64) NOT NULL,
		source           TEXT NOT NULL DEFAULT '',
		registered_at    TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (code, adjustment_basis, dataset_hash)
	)`,

	`CREATE TABLE IF NOT EXISTS analysis.sensitivity_runs (
		report_id  TEXT PRIMARY KEY REFERENCES analysis.reports(report_id),
		run_id     TEXT NOT NULL,
		grid       JSONB NOT NULL,
		points     JSONB,
		created_at TIMESTAMPTZ NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS analysis.mc_runs (
		report_id  TEXT PRIMARY KEY REFERENCES analysis.reports(report_id),
		seed       BIGINT NOT NULL,
		n          INTEGER NOT NULL CHECK (n > 0),
		method     TEXT NOT NULL CHECK (method IN ('bootstrap','shuffle')),
		input_hash CHAR(64) NOT NULL,
		summary    JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS analysis.attribution (
		report_id     TEXT PRIMARY KEY REFERENCES analysis.reports(report_id),
		method        TEXT NOT NULL CHECK (method IN ('brinson','factor')),
		contributions JSONB NOT NULL,
		residual      DOUBLE PRECISION NOT NULL,
		input_hash    CHAR(64) NOT NULL,
		created_at    TIMESTAMPTZ NOT NULL
	)`,

	`CREATE INDEX IF NOT EXISTS reports_run_type_idx
		ON analysis.reports (run_id, analysis_type)`,

	// completed 报告一旦落库即不可变：应用层（PG.PutReport）先判断并返回
	// ErrReportConflict；这条触发器是绕过应用层时的兜底（直接 UPDATE 也会被拒）。
	// 只前滚：改这段只能新增语句，不改上面已有语句。
	`CREATE OR REPLACE FUNCTION analysis.reports_completed_immutable()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $fn$
		BEGIN
			IF OLD.status = 'completed' THEN
				RAISE EXCEPTION 'analysis.reports: completed report % is immutable', OLD.report_id
					USING ERRCODE = '55000';
			END IF;
			RETURN NEW;
		END
		$fn$`,

	`DROP TRIGGER IF EXISTS reports_completed_immutable ON analysis.reports`,

	`CREATE TRIGGER reports_completed_immutable
		BEFORE UPDATE ON analysis.reports
		FOR EACH ROW EXECUTE FUNCTION analysis.reports_completed_immutable()`,
}

// Migrations 返回迁移语句的副本（调用方不能改到内部数组）。
func Migrations() []string {
	out := make([]string, len(migrationStatements))
	copy(out, migrationStatements)
	return out
}
