package storage

import (
	"context"
	"path/filepath"
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
		{"qq-main:group:one", historySearchEvent(10, "one", "old", "Alice", "很久以前讨论过长期记忆")},
		{"qq-main:group:two", historySearchEvent(20, "two", "new", "Bob", "另一个群也讨论长期记忆")},
		{"qq-other:group:three", historySearchEvent(30, "three", "foreign", "Carol", "其他机器人讨论长期记忆")},
		{"qq-main:group:two", historySearchEvent(40, "two", "noise", "Bob", "无关消息")},
	}
	for _, item := range events {
		item.event.ContextNamespace = item.session[:7]
		if err := store.AppendMessageEvent(ctx, item.session, item.event); err != nil {
			t.Fatal(err)
		}
	}

	current, total, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{
		Session: "qq-main:group:one", Text: "长期记忆", FromTime: 0, ThroughTime: 100, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(current) != 1 || current[0].MessageID != "old" {
		t.Fatalf("current search total=%d events=%#v", total, current)
	}

	groups, total, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{
		SessionPrefix: "qq-main:group:", CrossSession: true, Text: "长期 记忆", FromTime: 0, ThroughTime: 100, Limit: 1,
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
		if err := store.AppendMessageEvent(ctx, "qq-main:group:one", event); err != nil {
			t.Fatal(err)
		}
	}
	events, total, err := store.SearchMessageEvents(ctx, assistant.MessageHistorySearchQuery{
		Session: "qq-main:group:one", Text: "虎皮凤爪好吃吗", Terms: []string{"虎皮凤爪", "凤爪", "好吃"},
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
		MessageID: "outgoing-1", SenderName: "嘉然", Outbound: true,
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
