// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestGroupWordCloudCountsGroupWords(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "wordcloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	now := time.Now()
	insert := func(id, groupID, userID, text string, at time.Time, outbound bool) {
		t.Helper()
		payload := `{"outbound":false}`
		if outbound {
			payload = `{"outbound":true}`
		}
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO message_events (id, session, kind, group_id, user_id, message_id, sender_name, event_time, text, payload, created_at)
VALUES (?, ?, 'group', ?, ?, ?, '', ?, ?, ?, ?)
`, id, "group:"+groupID, groupID, userID, id, at.Unix(), text, payload, at.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	insert("m1", "g1", "10001", "今晚火锅 https://example.com/a", now, false)
	insert("m2", "g1", "10002", "火锅[CQ:face,id=1]", now, false)
	insert("m3", "g1", "10003", "火锅", now, false)
	// 撤回通知的重复行、机器人自己说的、别的群、区间外的都不算。
	insert("m3:notice:recall", "g1", "10003", "火锅", now, false)
	insert("m4", "g1", "42", "火锅火锅", now, true)
	insert("m5", "g1", "42", "火锅", now, false)
	insert("m6", "g2", "10004", "火锅", now, false)
	insert("m7", "g1", "10004", "火锅", now.Add(-48*time.Hour), false)

	cloud, err := store.GroupWordCloudFor(ctx, "g1", now.Add(-time.Hour), "42")
	if err != nil {
		t.Fatal(err)
	}
	if cloud.Messages != 3 || cloud.Speakers != 3 {
		t.Fatalf("messages=%d speakers=%d, want 3/3", cloud.Messages, cloud.Speakers)
	}
	if len(cloud.Words) == 0 || cloud.Words[0].Text != "火锅" || cloud.Words[0].Count != 3 {
		t.Fatalf("top word = %+v, want 火锅×3", cloud.Words)
	}
	for _, word := range cloud.Words {
		if word.Text == "https" || word.Text == "example" || word.Text == "face" {
			t.Fatalf("链接或 CQ 码漏进了词云：%+v", cloud.Words)
		}
	}
}
