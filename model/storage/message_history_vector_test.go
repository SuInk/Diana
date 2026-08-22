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
