package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// openTestPG 连接真库并跑迁移；未设置 ANALYTICS_TEST_DSN 时跳过。
//
// CI 默认没有 PG，所以这些用例是 opt-in；本地 / 部署验证用：
//
//	ANALYTICS_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_analytics_test?sslmode=disable' \
//	  go test ./internal/analysis -run PG -count=1 -v
//
// 用例不 TRUNCATE：与 httpapi 包的真库 E2E 并行跑时不能互相清表；所有身份
// 都带本次进程的时间戳后缀，重复运行只增行、不撞键。
func openTestPG(t *testing.T) *PG {
	t.Helper()
	dsn := os.Getenv("ANALYTICS_TEST_DSN")
	if dsn == "" {
		t.Skip("ANALYTICS_TEST_DSN is not set; skipping PostgreSQL store test")
	}
	ctx := context.Background()
	pg, err := OpenPG(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenPG: %v", err)
	}
	t.Cleanup(pg.Close)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return pg
}

var (
	pgNow    = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	pgSuffix = strconv.FormatInt(time.Now().UnixNano(), 10)
)

func pgID(prefix string) string { return prefix + "-" + pgSuffix }

func reportFixture(id string, status ReportStatus, body string, seed *int64) Report {
	rep := Report{
		ReportID:       id,
		RunID:          "run-" + id,
		AnalysisType:   TypeMetrics,
		Status:         status,
		MetricsVersion: MetricsVersion,
		ParamsHash:     strings.Repeat("p", 64),
		InputHash:      strings.Repeat("i", 64),
		Seed:           seed,
		CreatedAt:      pgNow,
		UpdatedAt:      pgNow,
	}
	if body != "" {
		rep.Body = json.RawMessage(body)
	}
	return rep
}

func TestPGMigrateIdempotent(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	var exists bool
	if err := pg.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'reports_completed_immutable')`).
		Scan(&exists); err != nil {
		t.Fatalf("query trigger: %v", err)
	}
	if !exists {
		t.Fatal("reports_completed_immutable trigger is missing after migration")
	}
}

func TestPGReportLifecycle(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	completedID := pgID("r-completed")
	completed := reportFixture(completedID, ReportCompleted, `{"metrics_version":"v1"}`, nil)
	if err := pg.PutReport(ctx, completed); err != nil {
		t.Fatalf("PutReport(completed): %v", err)
	}
	got, err := pg.GetReport(ctx, completedID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if got.Status != ReportCompleted || string(got.Body) == "" || got.ParamsHash != completed.ParamsHash {
		t.Fatalf("unexpected report: %+v", got)
	}

	// 同键同内容 = no-op；同键异内容 = fail-loud。
	if err := pg.PutReport(ctx, completed); err != nil {
		t.Fatalf("PutReport replay: %v", err)
	}
	conflict := completed
	conflict.Body = json.RawMessage(`{"metrics_version":"v2"}`)
	if err := pg.PutReport(ctx, conflict); !errors.Is(err, ErrReportConflict) {
		t.Fatalf("want ErrReportConflict, got %v", err)
	}

	// pending → completed 是合法的状态推进（异步 job 路径）。
	pendingID := pgID("r-pending")
	pending := reportFixture(pendingID, ReportPending, "", nil)
	if err := pg.PutReport(ctx, pending); err != nil {
		t.Fatalf("PutReport(pending): %v", err)
	}
	pending.Status = ReportCompleted
	pending.Body = json.RawMessage(`{"n":1}`)
	pending.UpdatedAt = pgNow.Add(time.Minute)
	if err := pg.PutReport(ctx, pending); err != nil {
		t.Fatalf("PutReport(complete pending): %v", err)
	}
	got, err = pg.GetReport(ctx, pendingID)
	if err != nil || got.Status != ReportCompleted {
		t.Fatalf("pending→completed: %+v err=%v", got, err)
	}

	if _, err := pg.GetReport(ctx, "missing"); !errors.Is(err, ErrReportNotFound) {
		t.Fatalf("want ErrReportNotFound, got %v", err)
	}
}

func TestPGReportSeedRoundTrip(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	seed := int64(0) // 0 是合法显式种子（F6），必须与 NULL 区分。
	seedID := pgID("r-seed")
	rep := reportFixture(seedID, ReportCompleted, `{"seed":0}`, &seed)
	rep.AnalysisType = TypeMonteCarlo
	if err := pg.PutReport(ctx, rep); err != nil {
		t.Fatalf("PutReport: %v", err)
	}
	got, err := pg.GetReport(ctx, seedID)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if got.Seed == nil || *got.Seed != 0 {
		t.Fatalf("seed = %v, want explicit 0", got.Seed)
	}

	noSeedID := pgID("r-noseed")
	noSeed := reportFixture(noSeedID, ReportCompleted, `{"seed":null}`, nil)
	if err := pg.PutReport(ctx, noSeed); err != nil {
		t.Fatalf("PutReport(no seed): %v", err)
	}
	got, err = pg.GetReport(ctx, noSeedID)
	if err != nil {
		t.Fatalf("GetReport(no seed): %v", err)
	}
	if got.Seed != nil {
		t.Fatalf("seed = %v, want nil", got.Seed)
	}
}

func TestPGCompletedReportIsImmutableInDB(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	rep := reportFixture(pgID("r-immutable"), ReportCompleted, `{"v":1}`, nil)
	if err := pg.PutReport(ctx, rep); err != nil {
		t.Fatalf("PutReport: %v", err)
	}
	// 绕过应用层直接改库：触发器必须拒绝。
	_, err := pg.pool.Exec(ctx,
		`UPDATE analysis.reports SET body = '{"v":2}'::jsonb WHERE report_id = $1`, rep.ReportID)
	if err == nil {
		t.Fatal("direct UPDATE of a completed report was accepted; immutability trigger is missing")
	}
	if !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("unexpected trigger error: %v", err)
	}
}

func TestPGBenchmarks(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	code := pgID("000300.SH")
	older := BenchmarkRef{
		Code: code, AdjustmentBasis: "qfq", DatasetHash: "ds-old",
		Source: "test", RegisteredAt: pgNow,
	}
	newer := older
	newer.DatasetHash = "ds-new"
	newer.RegisteredAt = pgNow.Add(time.Hour)

	if err := pg.RegisterBenchmark(ctx, older); err != nil {
		t.Fatalf("RegisterBenchmark(older): %v", err)
	}
	if err := pg.RegisterBenchmark(ctx, older); err != nil {
		t.Fatalf("RegisterBenchmark replay: %v", err)
	}
	if err := pg.RegisterBenchmark(ctx, newer); err != nil {
		t.Fatalf("RegisterBenchmark(newer): %v", err)
	}

	latest, err := pg.LatestBenchmark(ctx, code)
	if err != nil {
		t.Fatalf("LatestBenchmark: %v", err)
	}
	if latest.DatasetHash != "ds-new" {
		t.Fatalf("latest = %s, want ds-new", latest.DatasetHash)
	}
	if _, err := pg.LatestBenchmark(ctx, pgID("missing")); !errors.Is(err, ErrBenchmarkNotRegistered) {
		t.Fatalf("want ErrBenchmarkNotRegistered, got %v", err)
	}
}

func TestPGAttributionAndMonteCarlo(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	attrID := pgID("r-attr")
	if err := pg.PutReport(ctx, reportFixture(attrID, ReportCompleted, `{"method":"factor"}`, nil)); err != nil {
		t.Fatalf("PutReport(attr): %v", err)
	}
	attr := AttributionRecord{
		ReportID:      attrID,
		Method:        "factor",
		Contributions: json.RawMessage(`[{"name":"value","contribution":0.08}]`),
		Residual:      0.02,
		InputHash:     strings.Repeat("i", 64),
		CreatedAt:     pgNow,
	}
	if err := pg.PutAttribution(ctx, attr); err != nil {
		t.Fatalf("PutAttribution: %v", err)
	}
	if err := pg.PutAttribution(ctx, attr); err != nil {
		t.Fatalf("PutAttribution replay: %v", err)
	}
	changed := attr
	changed.Residual = 0.03
	if err := pg.PutAttribution(ctx, changed); !errors.Is(err, ErrReportConflict) {
		t.Fatalf("want ErrReportConflict, got %v", err)
	}

	seed := int64(42)
	mcID := pgID("r-mc")
	mcReport := reportFixture(mcID, ReportCompleted, `{"n":10}`, &seed)
	mcReport.AnalysisType = TypeMonteCarlo
	if err := pg.PutReport(ctx, mcReport); err != nil {
		t.Fatalf("PutReport(mc): %v", err)
	}
	mc := MonteCarloRecord{
		ReportID:  mcID,
		Seed:      seed,
		N:         10,
		Method:    MethodBootstrap,
		InputHash: strings.Repeat("i", 64),
		Summary:   json.RawMessage(`{"mean":0.1}`),
		CreatedAt: pgNow,
	}
	if err := pg.PutMonteCarlo(ctx, mc); err != nil {
		t.Fatalf("PutMonteCarlo: %v", err)
	}
	if err := pg.PutMonteCarlo(ctx, mc); err != nil {
		t.Fatalf("PutMonteCarlo replay: %v", err)
	}
	changedMC := mc
	changedMC.Seed = 43
	if err := pg.PutMonteCarlo(ctx, changedMC); !errors.Is(err, ErrReportConflict) {
		t.Fatalf("want ErrReportConflict, got %v", err)
	}
}
