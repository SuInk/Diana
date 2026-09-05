// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// insertRawLog 绕过 AppendLog 直接写库，造出归一化之前那种带偏移的历史行。
func insertRawLog(t *testing.T, store *SQLiteStore, id, action, createdAt string, metadata string) {
	t.Helper()
	_, err := store.db.Exec(`
INSERT INTO app_logs (id, kind, level, action, message, detail, actor, target, metadata, created_at)
VALUES (?, 'operation', 'info', ?, 'msg', '', '', ?, ?, ?)
`, id, action, id, metadata, createdAt)
	if err != nil {
		t.Fatal(err)
	}
}

func readRawCreatedAt(t *testing.T, store *SQLiteStore, id string) string {
	t.Helper()
	var createdAt string
	if err := store.db.QueryRow(`SELECT created_at FROM app_logs WHERE id = ?`, id).Scan(&createdAt); err != nil {
		t.Fatal(err)
	}
	return createdAt
}

// 迁移之前带偏移写进去的行，重新打开库之后应当变成 UTC 形态，并且时刻不变。
func TestMigrateLogTimestampsRewritesOffsetForm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log-tz-migration.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	insertRawLog(t, store, "offset-row", "diana.llm_usage", "2026-09-04T01:00:00+08:00", `{"input_tokens":7,"output_tokens":3}`)
	insertRawLog(t, store, "utc-row", "diana.llm_usage", "2026-09-03T18:00:00Z", `{"input_tokens":1,"output_tokens":1}`)
	// 迁移标记是首次打开时写下的，这里要清掉，否则手工插进去的行不会被扫描。
	if _, err := store.db.Exec(`DELETE FROM app_state WHERE key = ?`, logTimestampUTCMigrationKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	got := readRawCreatedAt(t, reopened, "offset-row")
	if got != "2026-09-03T17:00:00Z" {
		t.Fatalf("created_at = %q，期望 2026-09-03T17:00:00Z——同一时刻换成 UTC 写法", got)
	}
	// 换写法不能换时刻。
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := time.Parse(time.RFC3339Nano, "2026-09-04T01:00:00+08:00")
	if !parsed.Equal(original) {
		t.Fatalf("时刻被改动了：%s vs %s", parsed, original)
	}
	// 本来就是 UTC 的行不该被动过。
	if got := readRawCreatedAt(t, reopened, "utc-row"); got != "2026-09-03T18:00:00Z" {
		t.Fatalf("UTC 行被改写了：%q", got)
	}
}

// 迁移的目的不是把字符串改好看，而是让这些行重新出现在统计里。
func TestMigrateLogTimestampsRestoresDashboardVisibility(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "log-tz-visibility.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}

	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("时区库不可用：%v", err)
	}
	now := time.Now().In(shanghai)
	noon := time.Date(now.Year(), now.Month(), now.Day(), 14, 0, 0, 0, shanghai)
	insertRawLog(t, store, "legacy", "diana.llm_usage",
		noon.Add(-time.Hour).Format(time.RFC3339Nano), `{"input_tokens":7,"output_tokens":3}`)

	// 迁移前：这条日志在统计里是看不见的，这正是要修的故障。
	before, err := store.DashboardStatsForDay(ctx, noon, "")
	if err != nil {
		t.Fatal(err)
	}
	if before.LLMCalls != 0 {
		t.Fatalf("前提不成立：带偏移的行本不该被统计到，实际 calls=%d", before.LLMCalls)
	}
	if _, err := store.db.Exec(`DELETE FROM app_state WHERE key = ?`, logTimestampUTCMigrationKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	after, err := reopened.DashboardStatsForDay(ctx, noon, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.LLMCalls != 1 || after.LLMInputTokens != 7 || after.LLMOutputTokens != 3 {
		t.Fatalf("迁移后仍然统计不到：calls=%d input=%d output=%d，期望 1/7/3", after.LLMCalls, after.LLMInputTokens, after.LLMOutputTokens)
	}
}

// 认不出来的时间格式原样留着：猜错会把一条尚可辨认的日志变成彻底错误的时间。
func TestMigrateLogTimestampsLeavesUnparseableValuesAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log-tz-garbage.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	insertRawLog(t, store, "garbage", "diana.llm_usage", "2026-09-04 01:00:00", `{}`)
	if _, err := store.db.Exec(`DELETE FROM app_state WHERE key = ?`, logTimestampUTCMigrationKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	if got := readRawCreatedAt(t, reopened, "garbage"); got != "2026-09-04 01:00:00" {
		t.Fatalf("认不出的值被改写了：%q", got)
	}
}

// 标记落下之后不该再扫表，避免对永久保留的日志反复扫描。
func TestMigrateLogTimestampsRunsOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "log-tz-once.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var migrated bool
	ok, err := store.loadJSON(ctx, logTimestampUTCMigrationKey, &migrated)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !migrated {
		t.Fatalf("首次打开应当写下迁移标记：ok=%v migrated=%v", ok, migrated)
	}

	// 标记还在时插入一条坏行，重开之后它应当保持原样——证明扫描确实被跳过了。
	insertRawLog(t, store, "after-marker", "diana.llm_usage", "2026-09-04T01:00:00+08:00", `{}`)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if got := readRawCreatedAt(t, reopened, "after-marker"); got != "2026-09-04T01:00:00+08:00" {
		t.Fatalf("标记已存在却仍然扫了表：%q", got)
	}
}

// 迁移之后新写入的日志仍然走 normalizeLogEntry，两条路径的结果要一致。
func TestMigrateLogTimestampsCoexistsWithNormalizedWrites(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "log-tz-mixed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("时区库不可用：%v", err)
	}
	if err := store.AppendLog(ctx, applog.Entry{
		Action:    "diana.llm_usage",
		Target:    "fresh",
		CreatedAt: time.Date(2026, 9, 4, 1, 0, 0, 0, shanghai),
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListLogs(ctx, applog.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("日志条数 = %d", len(entries))
	}
	if _, offset := entries[0].CreatedAt.Zone(); offset != 0 {
		t.Fatalf("新写入的日志不是 UTC：%s", entries[0].CreatedAt.Format(time.RFC3339Nano))
	}
}
