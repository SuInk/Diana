// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

// 入站去重查询跑在写事务里，每条消息进来都会走一次。以前它只能用 (kind, group_id)
// 索引的第一列——group_id 那一列被 COALESCE 包住用不上——于是等价于把同类型的全部
// 历史扫一遍；库大了以后一次要好几秒，期间唯一的写连接被占着，别的写入全部超时。
// 这条用例钉住：去重查询必须走 message_id 索引，而不是退回到按 kind 扫表。
func TestDuplicateInboundHistoryLookupUsesMessageIDIndex(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	rows, err := store.db.Query(`
EXPLAIN QUERY PLAN
SELECT id, payload
FROM message_events
WHERE message_id = ?
  AND kind = ?
  AND COALESCE(group_id, '') = ?
  AND COALESCE(user_id, '') = ?
ORDER BY event_time DESC, created_at DESC, id DESC
`, "1", "group", "g", "u")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "idx_message_events_message_id") {
		t.Fatalf("duplicate lookup does not use the message_id index:\n%s", joined)
	}
}

// 发送前的追发检查和追发合并打标记都在写连接上按 message_id 找入站事件。以前
// inbound_events 没有这一列的索引，每次都整表扫，库大了会吃满 2 秒超时并堵住写入。
func TestInboundSupersessionLookupUsesMessageIDIndex(t *testing.T) {
	store, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	for _, query := range []string{
		// InboundEventSuperseded
		`SELECT superseded_by FROM inbound_events
WHERE message_id = ? AND kind = ?
  AND COALESCE(group_id, '') = ? AND COALESCE(user_id, '') = ?
ORDER BY created_at DESC, id DESC LIMIT 1`,
		// RecordInboundEventReplyMerge 的子查询
		`SELECT id FROM inbound_events
WHERE message_id = ? AND kind = ?
  AND COALESCE(group_id, '') = ? AND COALESCE(user_id, '') = ?
ORDER BY created_at DESC, id DESC LIMIT 1`,
	} {
		plan := explainQueryPlan(t, store, query, "1", "group", "g", "u")
		if !strings.Contains(plan, "idx_inbound_events_message_id") {
			t.Fatalf("supersession lookup does not use the message_id index:\n%s", plan)
		}
	}
}

// 正文索引没有查询用得上，老库升级后要被删掉，新库也不再建。
func TestMessageEventsTextIndexIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diana.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE INDEX idx_message_events_text ON message_events(text)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_message_events_text'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("idx_message_events_text still exists after migration")
	}
}

func explainQueryPlan(t *testing.T, store *SQLiteStore, query string, args ...any) string {
	t.Helper()
	rows, err := store.db.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(plan, "\n")
}
