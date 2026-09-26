// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/assistant"
)

func TestDebugTraceIsStoredAsReadableFilesBesideDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(dir, "diana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	event := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "123456", UserID: "u", MessageID: "m:1/2", Time: time.Now().Unix()}
	eventID, _, err := store.EnqueueInboundEvent(ctx, "group:123456", event)
	if err != nil {
		t.Fatal(err)
	}
	prompt := strings.Repeat("上下文 <b>&", 100)
	base := time.Now().UTC()
	for index, meta := range []map[string]any{
		{"phase": "model_request", "purpose": "proactive_reply_router", "request": prompt},
		{"phase": "agent_tool_completed"},
	} {
		meta["kind"], meta["group_id"], meta["user_id"], meta["sequence"] = "group", "123456", "u", int64(index+1)
		if err := store.AppendLog(ctx, applog.Entry{Kind: applog.KindDebug, Action: "debug_trace", Target: event.MessageID, Message: fmt.Sprint("step", index+1), Metadata: meta, CreatedAt: base.Add(time.Duration(index) * time.Millisecond)}); err != nil {
			t.Fatal(err)
		}
	}
	// 跨群检索记录仍在 app_logs，时间夹在两步中间，读出来要按时间排好。
	if err := store.AppendLog(ctx, applog.Entry{Kind: applog.KindDebug, Action: "cross_group_context", Target: event.MessageID, Message: "retrieval", Metadata: map[string]any{"kind": "group", "group_id": "123456", "user_id": "u"}, CreatedAt: base.Add(time.Microsecond)}); err != nil {
		t.Fatal(err)
	}

	var rows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_logs WHERE action = 'debug_trace'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("debug_trace rows in app_logs = %d, want 0", rows)
	}
	messageDir := filepath.Join(dir, debugTraceDirName, base.Format(debugTraceDayForm), "group-123456", "m_1_2")
	files, err := os.ReadDir(messageDir)
	if err != nil {
		t.Fatalf("trace dir missing: %v", err)
	}
	var names []string
	for _, file := range files {
		names = append(names, file.Name())
	}
	if strings.Join(names, ",") != "001-proactive_reply_router.json,002-agent_tool_completed.json" {
		t.Fatalf("trace files = %v", names)
	}
	first := filepath.Join(messageDir, "001-proactive_reply_router.json")
	info, err := os.Stat(first)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("trace file mode = %v err=%v, want 0600", info.Mode().Perm(), err)
	}
	// 人直接打开要能读：缩进排版，提示词里的中文和 <b>& 原样出现。
	raw, _ := os.ReadFile(first)
	if !strings.Contains(string(raw), "\n  \"message\": \"step1\"") || !strings.Contains(string(raw), "上下文 <b>&") {
		t.Fatalf("trace file is not human-readable:\n%.300s", raw)
	}

	// 进程在写的时候被杀留下的半个文件跳过，其余步骤照常读出来。
	if err := os.WriteFile(filepath.Join(messageDir, "003-reply.json"), []byte(`{"id": "x", "mess`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, steps, found, err := store.InboundEventDebugTrace(ctx, eventID)
	if err != nil || !found {
		t.Fatalf("trace found=%v err=%v", found, err)
	}
	var got []string
	for _, step := range steps {
		got = append(got, step.Message)
	}
	if strings.Join(got, ",") != "step1,retrieval,step2" {
		t.Fatalf("steps = %v", got)
	}
	if steps[0].Metadata["request"] != prompt {
		t.Fatal("request payload did not round-trip through the trace file")
	}
}

func TestPruneDebugTraceFilesDropsOnlyWholeExpiredDays(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(dir, "diana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	root := filepath.Join(dir, debugTraceDirName)
	for _, day := range []string{"2026-09-18", "2026-09-19", "2026-09-20", "not-a-day"} {
		if err := os.MkdirAll(filepath.Join(root, day), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// 截止在 9 月 19 日中午：18 日整天过期，19 日下午的记录还没到期。
	deleted, err := store.PruneDebugTraceFiles(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	entries, _ := os.ReadDir(root)
	var left []string
	for _, entry := range entries {
		left = append(left, entry.Name())
	}
	if strings.Join(left, ",") != "2026-09-19,2026-09-20,not-a-day" {
		t.Fatalf("left = %v", left)
	}
	if deleted, err := store.PruneDebugTraceFiles(time.Time{}); err != nil || deleted != 0 {
		t.Fatalf("zero cutoff deleted=%d err=%v", deleted, err)
	}
}

func TestInMemoryStoreKeepsDebugTraceInAppLogs(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.AppendLog(ctx, applog.Entry{Kind: applog.KindDebug, Action: "debug_trace", Target: "m", Message: "trace"}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_logs WHERE action = 'debug_trace'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows=%d err=%v", rows, err)
	}
}

func TestPruneCompletedMemoryJobsKeepsPendingAndRecent(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -10).UnixNano()
	recent := now.Add(-time.Hour).UnixNano()
	for _, row := range []struct {
		id, status  string
		completedAt any
	}{
		{"old-done", "done", old},
		{"recent-done", "done", recent},
		{"old-pending", "pending", nil},
	} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO memory_jobs (id, kind, session, payload, status, available_at, created_at, updated_at, completed_at)
VALUES (?, 'event', 's', '{}', ?, ?, ?, ?, ?)`, row.id, row.status, old, old, old, row.completedAt); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := store.PruneCompletedMemoryJobs(ctx, now.AddDate(0, 0, -7))
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id FROM memory_jobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var left []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	if strings.Join(left, ",") != "old-pending,recent-done" {
		t.Fatalf("left = %v", left)
	}
}

// 事件没有模型调用轨迹时，要说出真实原因，而不是一律让人去检查调试模式。
func TestDebugTraceEmptyReasonNamesTheActualCause(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(dir, "diana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	enqueue := func(messageID, status, outcome, reason string, enqueuedAt time.Time) string {
		t.Helper()
		event := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "g", UserID: "u", MessageID: messageID, Time: enqueuedAt.Unix()}
		id, _, err := store.EnqueueInboundEvent(ctx, "group:g", event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE inbound_events SET status = ?, outcome = ?, decision_reason = ?, created_at = ? WHERE id = ?`,
			status, outcome, reason, enqueuedAt.UnixNano(), id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	received := func(messageID string, at time.Time) {
		t.Helper()
		if err := store.AppendLog(ctx, applog.Entry{Kind: applog.KindDebug, Action: "debug_trace", Target: messageID, Message: "收到消息", CreatedAt: at,
			Metadata: map[string]any{"phase": "event_received", "sequence": int64(0), "kind": "group", "group_id": "g", "user_id": "u"}}); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	// 第一次写轨迹的时间就是新格式上线的时间。
	received("first", now.Add(-time.Hour))
	skipped := enqueue("skipped", "done", "ignored", "未被点名，也不在接话范围内", now)
	received("skipped", now)
	debugOff := enqueue("debug-off", "done", "ignored", "", now)
	beforeUpgrade := enqueue("before-upgrade", "done", "replied", "", now.Add(-2*time.Hour))
	stale := enqueue("stale", "done", "ignored_stale", "", now)
	pending := enqueue("pending", "pending", "", "", now)
	if _, err := store.PruneDebugTraceFiles(now.AddDate(0, 0, -7)); err != nil {
		t.Fatal(err)
	}
	expired := enqueue("expired", "done", "replied", "", now.AddDate(0, 0, -9))

	for _, test := range []struct {
		eventID, want string
	}{
		{skipped, "调试模式是开着的，但它在调用模型之前就结束了：未被点名，也不在接话范围内"},
		{debugOff, "调试模式是关着的"},
		{beforeUpgrade, "新版调试记录上线之前"},
		{stale, "按过期消息直接跳过"},
		{pending, "还在排队或处理中"},
		{expired, "已按调试日志保留期清理"},
	} {
		trace, found, err := store.InboundEventDebugTraceDetail(ctx, test.eventID)
		if err != nil || !found {
			t.Fatalf("%s: found=%v err=%v", test.eventID, found, err)
		}
		if len(trace.Steps) != 0 {
			t.Fatalf("%s: 「收到消息」不该作为一步显示：%#v", test.eventID, trace.Steps)
		}
		if !strings.Contains(trace.EmptyReason, test.want) {
			t.Fatalf("%s: reason = %q, want contains %q", test.eventID, trace.EmptyReason, test.want)
		}
	}
}

// 当天的轨迹保持明文，之前的压成 .json.gz，事件页照样读得出来。
func TestCompressDebugTraceFilesKeepsTodayPlainAndOlderReadable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(dir, "diana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now().UTC()
	write := func(messageID string, at time.Time) string {
		t.Helper()
		event := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "g", UserID: "u", MessageID: messageID, Time: at.Unix()}
		id, _, err := store.EnqueueInboundEvent(ctx, "group:g", event)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AppendLog(ctx, applog.Entry{Kind: applog.KindDebug, Action: "debug_trace", Target: messageID, Message: "模型请求完成", CreatedAt: at,
			Metadata: map[string]any{"phase": "model_request", "purpose": "reply", "sequence": int64(1), "kind": "group", "group_id": "g", "user_id": "u"}}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldID := write("old", now.AddDate(0, 0, -2))
	todayID := write("today", now)

	count, err := store.CompressDebugTraceFiles(ctx, now)
	if err != nil || count != 1 {
		t.Fatalf("compressed=%d err=%v", count, err)
	}
	oldDir := filepath.Join(dir, debugTraceDirName, now.AddDate(0, 0, -2).Format(debugTraceDayForm), "group-g", "old")
	if _, err := os.Stat(filepath.Join(oldDir, "001-reply.json.gz")); err != nil {
		t.Fatalf("older trace not compressed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldDir, "001-reply.json")); !os.IsNotExist(err) {
		t.Fatalf("plain copy should be removed after compression: %v", err)
	}
	todayFile := filepath.Join(dir, debugTraceDirName, now.Format(debugTraceDayForm), "group-g", "today", "001-reply.json")
	if _, err := os.Stat(todayFile); err != nil {
		t.Fatalf("today's trace should stay plain: %v", err)
	}
	for _, id := range []string{oldID, todayID} {
		trace, found, err := store.InboundEventDebugTraceDetail(ctx, id)
		if err != nil || !found || len(trace.Steps) != 1 || trace.Steps[0].Message != "模型请求完成" {
			t.Fatalf("%s: trace=%#v found=%v err=%v", id, trace, found, err)
		}
	}
	if count, err := store.CompressDebugTraceFiles(ctx, now); err != nil || count != 0 {
		t.Fatalf("second run compressed=%d err=%v", count, err)
	}
}
