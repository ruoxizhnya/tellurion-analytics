package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PG 是 analysis.* 的 PostgreSQL 实现（本仓是该 schema 的唯一写者）。
//
// 语义与 Memory 对齐：completed 是终态，同键同内容 = no-op，同键不同内容 =
// ErrReportConflict。除 Go 侧判断外，数据库还有 reports_completed_immutable
// 触发器兜底（见 ddl.go）——绕过本实现的直接 UPDATE 也会被拒。
type PG struct {
	pool *pgxpool.Pool
}

var _ Store = (*PG)(nil)

// OpenPG 建立连接池并做一次 Ping（fail-fast：连不上就不启动）。
func OpenPG(ctx context.Context, dsn string) (*PG, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("analysis: parse postgres dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("analysis: create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("analysis: ping postgres: %w", err)
	}
	return &PG{pool: pool}, nil
}

// Close 释放连接池。
func (p *PG) Close() { p.pool.Close() }

// Migrate 按序执行 Migrations()（单事务、幂等、无版本表）。
//
// 与 core / risk 同一取向：每条语句必须能在空库上一次跑完，也可重入；
// 已合并的语句不改，修错靠新增一条（docs/db-schema.md §4）。
func (p *PG) Migrate(ctx context.Context) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("analysis: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// 多实例同时启动时串行化迁移（会话级事务锁随事务提交释放）。
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "tellurion.analysis.migrate"); err != nil {
		return fmt.Errorf("analysis: acquire migration lock: %w", err)
	}
	for i, stmt := range Migrations() {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("analysis: migration %d: %w", i+1, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("analysis: commit migration: %w", err)
	}
	return nil
}

const reportColumns = `report_id, run_id, analysis_type, status, metrics_version,
	rtrim(params_hash), rtrim(input_hash), seed, body, error, created_at, updated_at`

// GetReport 读一行报告（body 原样返回）。
func (p *PG) GetReport(ctx context.Context, reportID string) (Report, error) {
	var (
		rep    Report
		typ    string
		status string
		seed   pgtype.Int8
		body   []byte
	)
	err := p.pool.QueryRow(ctx,
		`SELECT `+reportColumns+` FROM analysis.reports WHERE report_id = $1`, reportID).
		Scan(&rep.ReportID, &rep.RunID, &typ, &status, &rep.MetricsVersion,
			&rep.ParamsHash, &rep.InputHash, &seed, &body, &rep.Error,
			&rep.CreatedAt, &rep.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Report{}, ErrReportNotFound
	}
	if err != nil {
		return Report{}, fmt.Errorf("analysis: get report %s: %w", reportID, err)
	}
	rep.AnalysisType = AnalysisType(typ)
	rep.Status = ReportStatus(status)
	if len(body) > 0 {
		rep.Body = json.RawMessage(body)
	}
	if seed.Valid {
		v := seed.Int64
		rep.Seed = &v
	}
	return rep.Clone(), nil
}

// PutReport 写入 / 推进报告状态。
//
// 已存在且 completed：只允许同键同内容（no-op），否则 ErrReportConflict。
// 已存在但未完成：允许推进到 completed / failed（异步 job 的正常路径）。
func (p *PG) PutReport(ctx context.Context, report Report) error {
	for attempt := 0; attempt < 3; attempt++ {
		done, err := p.tryInsertReport(ctx, report)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		done, err = p.updateReportIfNotCompleted(ctx, report)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	return fmt.Errorf("analysis: put report %s: concurrent delete", report.ReportID)
}

// tryInsertReport 插入新行；主键冲突返回 (false, nil)。
func (p *PG) tryInsertReport(ctx context.Context, report Report) (bool, error) {
	tag, err := p.pool.Exec(ctx,
		`INSERT INTO analysis.reports
			(report_id, run_id, analysis_type, status, metrics_version,
			 params_hash, input_hash, seed, body, error, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12)
		 ON CONFLICT (report_id) DO NOTHING`,
		report.ReportID, report.RunID, string(report.AnalysisType), string(report.Status),
		report.MetricsVersion, report.ParamsHash, report.InputHash, report.Seed,
		jsonParam(report.Body), report.Error, report.CreatedAt, report.UpdatedAt)
	if err != nil {
		return false, fmt.Errorf("analysis: insert report %s: %w", report.ReportID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// updateReportIfNotCompleted 在行存在且未完成时推进状态；completed 行按
// 「同内容 no-op / 异内容冲突」处理。返回 false 表示行已不存在（调用方重试）。
func (p *PG) updateReportIfNotCompleted(ctx context.Context, report Report) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("analysis: begin put report %s: %w", report.ReportID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		status string
		same   bool
	)
	err = tx.QueryRow(ctx,
		`SELECT status,
		        (status = $2 AND body IS NOT DISTINCT FROM $3::jsonb)
		   FROM analysis.reports
		  WHERE report_id = $1
		  FOR UPDATE`,
		report.ReportID, string(report.Status), jsonParam(report.Body)).
		Scan(&status, &same)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("analysis: lock report %s: %w", report.ReportID, err)
	}
	if status == string(ReportCompleted) {
		if !same {
			return false, ErrReportConflict
		}
		return true, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE analysis.reports
		    SET status = $2, body = $3::jsonb, error = $4, updated_at = $5
		  WHERE report_id = $1`,
		report.ReportID, string(report.Status), jsonParam(report.Body),
		report.Error, report.UpdatedAt); err != nil {
		return false, fmt.Errorf("analysis: update report %s: %w", report.ReportID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("analysis: commit report %s: %w", report.ReportID, err)
	}
	return true, nil
}

// RegisterBenchmark 登记基准引用。同键重复登记是 no-op（不覆盖 registered_at）。
func (p *PG) RegisterBenchmark(ctx context.Context, ref BenchmarkRef) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO analysis.benchmarks (code, adjustment_basis, dataset_hash, source, registered_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (code, adjustment_basis, dataset_hash) DO NOTHING`,
		ref.Code, ref.AdjustmentBasis, ref.DatasetHash, ref.Source, ref.RegisteredAt)
	if err != nil {
		return fmt.Errorf("analysis: register benchmark %s: %w", ref.Code, err)
	}
	return nil
}

// LatestBenchmark 按 code 取「当前」登记：registered_at 最新；同刻按 dataset_hash 定序。
func (p *PG) LatestBenchmark(ctx context.Context, code string) (BenchmarkRef, error) {
	var ref BenchmarkRef
	err := p.pool.QueryRow(ctx,
		`SELECT code, rtrim(adjustment_basis), rtrim(dataset_hash), source, registered_at
		   FROM analysis.benchmarks
		  WHERE code = $1
		  ORDER BY registered_at DESC, dataset_hash DESC
		  LIMIT 1`, code).
		Scan(&ref.Code, &ref.AdjustmentBasis, &ref.DatasetHash, &ref.Source, &ref.RegisteredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BenchmarkRef{}, ErrBenchmarkNotRegistered
	}
	if err != nil {
		return BenchmarkRef{}, fmt.Errorf("analysis: latest benchmark %s: %w", code, err)
	}
	return ref, nil
}

// PutAttribution 落归因记录（residual 必须落库）。同键异内容 ⇒ ErrReportConflict。
func (p *PG) PutAttribution(ctx context.Context, rec AttributionRecord) error {
	tag, err := p.pool.Exec(ctx,
		`INSERT INTO analysis.attribution
			(report_id, method, contributions, residual, input_hash, created_at)
		 VALUES ($1, $2, $3::jsonb, $4, $5, $6)
		 ON CONFLICT (report_id) DO NOTHING`,
		rec.ReportID, rec.Method, jsonParam(rec.Contributions), rec.Residual,
		rec.InputHash, rec.CreatedAt)
	if err != nil {
		return fmt.Errorf("analysis: insert attribution %s: %w", rec.ReportID, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var same bool
	if err := p.pool.QueryRow(ctx,
		`SELECT (contributions = $2::jsonb AND residual = $3)
		   FROM analysis.attribution WHERE report_id = $1`,
		rec.ReportID, jsonParam(rec.Contributions), rec.Residual).Scan(&same); err != nil {
		return fmt.Errorf("analysis: compare attribution %s: %w", rec.ReportID, err)
	}
	if !same {
		return ErrReportConflict
	}
	return nil
}

// PutMonteCarlo 落蒙特卡洛批次（seed 必填）。同键异内容 ⇒ ErrReportConflict。
func (p *PG) PutMonteCarlo(ctx context.Context, rec MonteCarloRecord) error {
	tag, err := p.pool.Exec(ctx,
		`INSERT INTO analysis.mc_runs
			(report_id, seed, n, method, input_hash, summary, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)
		 ON CONFLICT (report_id) DO NOTHING`,
		rec.ReportID, rec.Seed, rec.N, rec.Method, rec.InputHash,
		jsonParam(rec.Summary), rec.CreatedAt)
	if err != nil {
		return fmt.Errorf("analysis: insert mc run %s: %w", rec.ReportID, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var same bool
	if err := p.pool.QueryRow(ctx,
		`SELECT (seed = $2 AND n = $3 AND method = $4)
		   FROM analysis.mc_runs WHERE report_id = $1`,
		rec.ReportID, rec.Seed, rec.N, rec.Method).Scan(&same); err != nil {
		return fmt.Errorf("analysis: compare mc run %s: %w", rec.ReportID, err)
	}
	if !same {
		return ErrReportConflict
	}
	return nil
}

// jsonParam 把 JSON 交给 `$n::jsonb` 落库：空体是 NULL（DDL 的 CHECK 依赖它）。
func jsonParam(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
