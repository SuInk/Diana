// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestSearchMessageEventsSupportsAllTimeAndNamespaceScopedGroups(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	events := []struct {
		session string
		event   assistant.MessageEvent
	}{
		{"onebot-main:group:one", historySearchEvent(10, "one", "old", "Alice", "很久以前讨论过长期记忆")},
		{"onebot-main:group:two", historySearchEvent(20, "two", "new", "Bob", "另一个群也讨论长期记忆")},
		{"onebot-other:group:three", historySearchEvent(30, "three", "foreign", "Carol", "其他机器人讨论长期记忆")},
		{"onebot-main:group:two", historySearchEvent(40, "two", "noise", "Bob", "无关消息")},
	}
	for _, item := range events {
		item.event.ContextNamespace = item.session[:7]
		if err := store.AppendMessageEvent(ctx, item.session, item.event); err != nil {
			t.Fatal(err)
		}
	}

	current, total, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{
		Session: "onebot-main:group:one", Text: "长期记忆", FromTime: 0, ThroughTime: 100, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(current) != 1 || current[0].MessageID != "old" {
		t.Fatalf("current search total=%d events=%#v", total, current)
	}

	groups, total, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{
		SessionPrefix: "onebot-main:group:", CrossSession: true, Text: "长期 记忆", FromTime: 0, ThroughTime: 100, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(groups) != 1 || groups[0].MessageID != "new" {
		t.Fatalf("cross-group search total=%d events=%#v", total, groups)
	}
}

func TestSearchMessageEventsRanksExactPhraseThenPartialTerms(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "history-ranking.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for _, event := range []assistant.MessageEvent{
		historySearchEvent(30, "one", "partial", "Alice", "这家的凤爪味道不错"),
		historySearchEvent(20, "one", "exact", "Bob", "虎皮凤爪很好吃"),
		historySearchEvent(10, "one", "noise", "Carol", "今天讨论别的菜"),
	} {
		if err := store.AppendMessageEvent(ctx, "onebot-main:group:one", event); err != nil {
			t.Fatal(err)
		}
	}
	events, total, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{
		Session: "onebot-main:group:one", Text: "虎皮凤爪好吃吗", Terms: []string{"虎皮凤爪", "凤爪", "好吃"},
		FromTime: 0, ThroughTime: 100, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(events) != 2 || events[0].MessageID != "exact" || events[1].MessageID != "partial" {
		t.Fatalf("ranked history total=%d events=%#v", total, events)
	}
}

func TestMessageHistoryPersistsOutboundRole(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "outbound-history.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	event := assistant.MessageEvent{
		Kind: assistant.EventKindPrivate, Time: 10, SelfID: "42", UserID: "10001",
		MessageID: "outgoing-1", SenderName: "Diana", Outbound: true,
		Segments: []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": "我刚才说过的话"}}},
	}
	if err := store.AppendMessageEvent(ctx, "private:10001", event); err != nil {
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
	history, err := store.ListRecentMessageEvents(ctx, "private:10001", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || !history[0].Outbound || history[0].UserID != "10001" || history[0].SelfID != "42" {
		t.Fatalf("persisted outbound history = %#v", history)
	}
}

func historySearchEvent(at int64, groupID, messageID, sender, text string) assistant.MessageEvent {
	return assistant.MessageEvent{
		Kind: assistant.EventKindGroup, Time: at, GroupID: groupID, UserID: sender,
		MessageID: messageID, SenderName: sender,
		Segments: []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

// 纯图片消息的正文是空的：只有事后补写的图片描述能让它被搜到。
func TestSearchMessageEventsMatchesImageDescriptionSearchExtra(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	session := "onebot-main:group:one"
	image := assistant.MessageEvent{
		Kind: assistant.EventKindGroup, Time: 10, GroupID: "one", UserID: "Alice",
		MessageID: "sticker", SenderName: "Alice",
		Segments: []assistant.MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.com/cat.png"}}},
	}
	if err := store.AppendMessageEvent(ctx, session, image); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessageEvent(ctx, session, historySearchEvent(20, "one", "chatter", "Bob", "今天天气不错")); err != nil {
		t.Fatal(err)
	}

	query := assistant.MessageHistorySearchQuery{Session: session, Text: "猫耳", FromTime: 0, ThroughTime: 100, Limit: 20}
	if events, total, err := store.SearchMessageEvents(ctx, query); err != nil || total != 0 || len(events) != 0 {
		t.Fatalf("before description: events=%#v total=%d err=%v", events, total, err)
	}

	if err := store.SaveMessageSearchExtra(ctx, session, "sticker", "炸毛生气的猫耳少女在哈气"); err != nil {
		t.Fatal(err)
	}
	events, total, err := store.SearchMessageEvents(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(events) != 1 || events[0].MessageID != "sticker" {
		t.Fatalf("after description: events=%#v total=%d", events, total)
	}

	// 消息本体再次落库（撤回补写、重复投递）不能把描述冲掉。
	if err := store.AppendMessageEvent(ctx, session, image); err != nil {
		t.Fatal(err)
	}
	if events, total, err = store.SearchMessageEvents(ctx, query); err != nil || total != 1 || len(events) != 1 {
		t.Fatalf("after re-append: events=%#v total=%d err=%v", events, total, err)
	}

	// 不存在的消息不建新行，也不报错。
	if err := store.SaveMessageSearchExtra(ctx, session, "missing", "无主描述"); err != nil {
		t.Fatal(err)
	}
}

func TestSaveMessageSearchExtraIfMediaCurrentRejectsReplacedOrRemovedImage(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "history-media-revision.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	session := "onebot-main:group:one"
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	imageEvent := func(messageID, caption, hash string) assistant.MessageEvent {
		segments := []assistant.MessageSegment{{Type: "text", Data: map[string]string{"text": caption}}}
		if hash != "" {
			segments = append(segments, assistant.MessageSegment{Type: "image", Data: map[string]string{
				"content_sha256": hash,
				"file_id":        "platform-file-" + hash[:1],
			}})
		}
		return assistant.MessageEvent{
			Kind: assistant.EventKindGroup, Time: 10, GroupID: "one", UserID: "Alice",
			MessageID: messageID, SenderName: "Alice", Segments: segments,
		}
	}

	for _, test := range []struct {
		name    string
		current assistant.MessageEvent
	}{
		{name: "replaced", current: imageEvent("replace", "换图后的正文", hashB)},
		{name: "removed", current: imageEvent("remove", "删图后的正文", "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := imageEvent(test.current.MessageID, "旧正文", hashA)
			if err := store.AppendMessageEvent(ctx, session, original); err != nil {
				t.Fatal(err)
			}
			test.current.Edited = true
			if err := store.AppendMessageEvent(ctx, session, test.current); err != nil {
				t.Fatal(err)
			}
			_, saved, err := store.SaveMessageSearchExtraIfMediaCurrent(ctx, session, original, "绝不能写回的旧图描述")
			if err != nil {
				t.Fatal(err)
			}
			if saved {
				t.Fatal("媒体已变化，旧描述仍被接受")
			}
			query := assistant.MessageHistorySearchQuery{Session: session, Text: "绝不能写回", FromTime: 0, ThroughTime: 100, Limit: 20}
			if events, total, err := store.SearchMessageEvents(ctx, query); err != nil || total != 0 || len(events) != 0 {
				t.Fatalf("stale description remained searchable: events=%#v total=%d err=%v", events, total, err)
			}
		})
	}
}

func TestSaveMessageSearchExtraIfMediaCurrentUsesCurrentCaption(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "history-same-media.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	session := "onebot-main:group:one"
	hash := strings.Repeat("c", 64)
	original := assistant.MessageEvent{
		Kind: assistant.EventKindGroup, Time: 10, GroupID: "one", UserID: "Alice", MessageID: "same-image", SenderName: "Alice",
		Segments: []assistant.MessageSegment{
			{Type: "text", Data: map[string]string{"text": "旧说明"}},
			{Type: "image", Data: map[string]string{"content_sha256": hash, "file_id": "old-file-id"}},
		},
	}
	if err := store.AppendMessageEvent(ctx, session, original); err != nil {
		t.Fatal(err)
	}
	current := original
	current.Edited = true
	current.Segments = []assistant.MessageSegment{
		{Type: "text", Data: map[string]string{"text": "编辑后的当前说明"}},
		{Type: "image", Data: map[string]string{"content_sha256": hash, "file_id": "renewed-file-id"}},
	}
	if err := store.AppendMessageEvent(ctx, session, current); err != nil {
		t.Fatal(err)
	}
	returned, saved, err := store.SaveMessageSearchExtraIfMediaCurrent(ctx, session, original, "同一张图片的异步描述")
	if err != nil {
		t.Fatal(err)
	}
	if !saved || assistant.PlainText(returned.Segments) != "编辑后的当前说明" {
		t.Fatalf("save=%v current=%#v", saved, returned)
	}
	query := assistant.MessageHistorySearchQuery{Session: session, Text: "异步描述", FromTime: 0, ThroughTime: 100, Limit: 20}
	events, total, err := store.SearchMessageEvents(ctx, query)
	if err != nil || total != 1 || len(events) != 1 || assistant.PlainText(events[0].Segments) != "编辑后的当前说明" {
		t.Fatalf("current media search: events=%#v total=%d err=%v", events, total, err)
	}
}
