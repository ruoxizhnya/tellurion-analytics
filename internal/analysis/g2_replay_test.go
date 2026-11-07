package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestG2MigrationReplayIsStable 是 SVC-AN-G2 ③① 的迁移重放自证腿。
//
// 判据：在**同一个** PostgreSQL 库上把 Migrations() 的全部 DDL 再跑一遍，
// 前后两次「五张 analysis.* 表的行数 / pg_indexes 集合 / 列集指纹」逐位不变
// （各自排序后取 sha256 比较）。行数快照在 REPEATABLE READ 事务里取，避免
// 其它包（httpapi 的真库 E2E）并发插行把行数抖成假红。
//
// 未设置 ANALYTICS_TEST_DSN 时跳过——CI 无库，skip 不是 fail：
//
//	ANALYTICS_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_analytics_test?sslmode=disable' \
//	  go test ./internal/analysis -run TestG2MigrationReplayIsStable -count=1 -v
func TestG2MigrationReplayIsStable(t *testing.T) {
	pg := openTestPG(t) // 第一条腿：Migrate 已在 openTestPG 里跑过一次
	ctx := context.Background()

	tx, err := pg.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatalf("begin replay transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	before := g2SchemaFingerprint(t, ctx, tx)

	// 第二条腿：与 PG.Migrate 同一份语句、同一个迁移锁，逐条重放。
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "tellurion.analysis.migrate"); err != nil {
		t.Fatalf("acquire replay lock: %v", err)
	}
	for i, stmt := range Migrations() {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("replay migration %d: %v", i+1, err)
		}
	}

	after := g2SchemaFingerprint(t, ctx, tx)
	beforeHash, afterHash := g2FingerprintHash(before), g2FingerprintHash(after)
	if before != after {
		t.Fatalf("migration replay changed analysis.* state:\nbefore sha256=%s\n%s\nafter  sha256=%s\n%s",
			beforeHash, before, afterHash, after)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit replay: %v", err)
	}
	t.Logf("replayed %d migration statements; fingerprint stable sha256=%s\n%s",
		len(Migrations()), beforeHash, before)
}

// g2Querier 让指纹函数同时接受 *pgxpool.Pool 与 pgx.Tx。
type g2Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var g2Tables = []string{"reports", "benchmarks", "sensitivity_runs", "mc_runs", "attribution"}

// g2SchemaFingerprint 采集三维指纹：行数、pg_indexes、列集（含类型/可空/默认值）。
// 每行是「维度 对象 值」的文本，最后整体排序——顺序稳定才谈得上逐位比较。
func g2SchemaFingerprint(t *testing.T, ctx context.Context, q g2Querier) string {
	t.Helper()

	var lines []string
	for _, table := range g2Tables {
		var n int64
		if err := q.QueryRow(ctx, `SELECT count(*) FROM analysis.`+table).Scan(&n); err != nil {
			t.Fatalf("count analysis.%s: %v", table, err)
		}
		lines = append(lines, fmt.Sprintf("rows analysis.%s %d", table, n))
	}

	rows, err := q.Query(ctx,
		`SELECT tablename, indexname, indexdef FROM pg_indexes WHERE schemaname = 'analysis'`)
	if err != nil {
		t.Fatalf("query pg_indexes: %v", err)
	}
	for rows.Next() {
		var table, name, def string
		if err := rows.Scan(&table, &name, &def); err != nil {
			rows.Close()
			t.Fatalf("scan pg_indexes: %v", err)
		}
		lines = append(lines, "index analysis."+table+" "+name+" "+def)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pg_indexes: %v", err)
	}

	rows, err = q.Query(ctx,
		`SELECT table_name, column_name, data_type, is_nullable, coalesce(column_default, '')
		   FROM information_schema.columns
		  WHERE table_schema = 'analysis'`)
	if err != nil {
		t.Fatalf("query information_schema.columns: %v", err)
	}
	for rows.Next() {
		var table, column, typ, nullable, def string
		if err := rows.Scan(&table, &column, &typ, &nullable, &def); err != nil {
			rows.Close()
			t.Fatalf("scan information_schema.columns: %v", err)
		}
		lines = append(lines, "column analysis."+table+" "+column+" "+typ+" "+nullable+" "+def)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate information_schema.columns: %v", err)
	}

	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func g2FingerprintHash(fingerprint string) string {
	sum := sha256.Sum256([]byte(fingerprint))
	return hex.EncodeToString(sum[:])
}
