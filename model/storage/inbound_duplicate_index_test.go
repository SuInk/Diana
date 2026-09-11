// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
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
