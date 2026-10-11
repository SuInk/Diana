// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func vectorStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "vectors.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if !store.historyVectors {
		t.Fatal("向量表没有建立,语义检索会静默失效")
	}
	return store
}

func vectorEvent(at int64, group, messageID, text string) assistant.MessageEvent {
	return assistant.MessageEvent{
		Kind: assistant.EventKindGroup, Platform: "onebot", GroupID: group, UserID: "u1",
		SenderName: "甲", MessageID: messageID, RawMessage: text,
		Segments: []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
		Time:     at,
	}
}

// 最近邻按余弦相似度排序,且只比对同一 embedding 模型产出的向量。
func TestMessageEventVectorSearchRanksByCosineAndFiltersModel(t *testing.T) {
	ctx := context.Background()
	store := vectorStore(t)
	session := "onebot-main:group:one"
	for _, item := range []struct {
		id, text string
		vec      []float32
	}{
		{"near", "凤爪味道不错", []float32{1, 0, 0}},
		{"mid", "今天天气不错", []float32{0.7, 0.7, 0}},
		{"far", "编译不过求助", []float32{0, 0, 1}},
	} {
		if err := store.AppendMessageEvent(ctx, session, vectorEvent(50, "one", item.id, item.text)); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveMessageEventVector(ctx, session, item.id, "embed-v1", item.vec); err != nil {
			t.Fatal(err)
		}
	}
	// 另一个模型写入的向量不得参与比对。
	if err := store.AppendMessageEvent(ctx, session, vectorEvent(50, "one", "othermodel", "别的模型")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessageEventVector(ctx, session, "othermodel", "embed-v2", []float32{1, 0, 0}); err != nil {
		t.Fatal(err)
	}

	events, err := store.SearchMessageEventsByVector(ctx, assistant.MessageHistoryVectorQuery{
		Session: session, Vector: []float32{1, 0.1, 0}, Model: "embed-v1",
		FromTime: 0, ThroughTime: 100, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("应命中 near 与 mid(far 与查询正交、othermodel 模型不同),实际 %d 条", len(events))
	}
	if events[0].MessageID != "near" || events[1].MessageID != "mid" {
		t.Fatalf("排序不对:%s, %s", events[0].MessageID, events[1].MessageID)
	}
}

// 跨会话检索必须仍然被 session 前缀限死。
func TestMessageEventVectorSearchScopesSessions(t *testing.T) {
	ctx := context.Background()
	store := vectorStore(t)
	if err := store.AppendMessageEvent(ctx, "onebot-main:group:one", vectorEvent(50, "one", "mine", "自己人")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessageEventVector(ctx, "onebot-main:group:one", "mine", "embed-v1", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessageEvent(ctx, "otherbot:group:x", vectorEvent(50, "x", "leak", "隔壁命名空间")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessageEventVector(ctx, "otherbot:group:x", "leak", "embed-v1", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	events, err := store.SearchMessageEventsByVector(ctx, assistant.MessageHistoryVectorQuery{
		SessionPrefix: "onebot-main:group:", CrossSession: true,
		Vector: []float32{1, 0}, Model: "embed-v1", FromTime: 0, ThroughTime: 100, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].MessageID != "mine" {
		t.Fatalf("命名空间泄漏:%+v", events)
	}
}

func TestDeleteMessageEventVectorIsScopedToStoredSession(t *testing.T) {
	ctx := context.Background()
	store := vectorStore(t)
	for _, session := range []string{"legacy:group:one", "scoped:group:one"} {
		if err := store.AppendMessageEvent(ctx, session, vectorEvent(50, "one", "same-id", session)); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveMessageEventVector(ctx, session, "same-id", "embed-v1", []float32{1, 0}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteMessageEventVector(ctx, "legacy:group:one", "same-id"); err != nil {
		t.Fatal(err)
	}
	search := func(session string) []assistant.MessageEvent {
		events, err := store.SearchMessageEventsByVector(ctx, assistant.MessageHistoryVectorQuery{
			Session: session, Vector: []float32{1, 0}, Model: "embed-v1", FromTime: 0, ThroughTime: 100, Limit: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		return events
	}
	if events := search("legacy:group:one"); len(events) != 0 {
		t.Fatalf("legacy session vector was not deleted: %#v", events)
	}
	if events := search("scoped:group:one"); len(events) != 1 || events[0].MessageID != "same-id" {
		t.Fatalf("delete leaked into another session: %#v", events)
	}
}

func TestMessageEventVectorCannotReturnAfterKnownEdit(t *testing.T) {
	ctx := context.Background()
	store := vectorStore(t)
	session := "telegram-main:group:one"
	original := vectorEvent(50, "one", "edited-message", "旧正文可被语义召回")
	if err := store.AppendMessageEvent(ctx, session, original); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessageEventVector(ctx, session, original.MessageID, "embed-v1", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}

	edited := original
	edited.RawMessage = "编辑后的正文"
	edited.Segments = []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": edited.RawMessage}}}
	edited.Edited = true
	edited.EditTime = 60
	edited.TelegramUpdateID = 101
	edited.EditRefreshOnly = true
	if err := store.AppendMessageEvent(ctx, session, edited); err != nil {
		t.Fatal(err)
	}
	// The edit row is committed before the best-effort physical delete. Even in
	// that window (or if deletion fails), search must hide the stale vector.
	events, err := store.SearchMessageEventsByVector(ctx, assistant.MessageHistoryVectorQuery{
		Session: session, Vector: []float32{1, 0}, Model: "embed-v1", FromTime: 0, ThroughTime: 100, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("known edit exposed stale vector before physical delete: %#v", events)
	}
	if err := store.DeleteMessageEventVector(ctx, session, original.MessageID); err != nil {
		t.Fatal(err)
	}
	// 模拟 edit 已删除向量后，旧正文的 embedding 才完成。保存必须在同一
	// SQL 语句里看到当前 revision，不能把旧向量重新插回来。
	if err := store.SaveMessageEventVector(ctx, session, original.MessageID, "embed-v1", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+messageHistoryVectorTable).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale vector returned after edit: count=%d", count)
	}

	firstSeen := vectorEvent(70, "one", "first-seen-edit", "第一次看到的就是编辑版本")
	firstSeen.Edited = true
	firstSeen.EditTime = 70
	firstSeen.TelegramUpdateID = 102
	if err := store.AppendMessageEvent(ctx, session, firstSeen); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessageEventVector(ctx, session, firstSeen.MessageID, "embed-v1", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+messageHistoryVectorTable).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("first-seen edit should index like an ordinary message: count=%d", count)
	}
}
