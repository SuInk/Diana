// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// 长度只看群友：机器人自己的长回复正是要纠正的对象，算进去就成了照着自己学。
func TestGroupMessageNormIgnoresBotAndEmptyMessages(t *testing.T) {
	var history []MessageEvent
	for i := 0; i < 20; i++ {
		history = append(history, MessageEvent{Kind: EventKindGroup, UserID: fmt.Sprint(100 + i%4), Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": strings.Repeat("字", 10+i)}}}})
		history = append(history, MessageEvent{Kind: EventKindGroup, UserID: "bot", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": strings.Repeat("长", 300)}}}})
	}
	history = append(history, MessageEvent{Kind: EventKindGroup, UserID: "101", Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": "x"}}}})
	norm, ok := groupMessageNorm(history, "bot", nil)
	if !ok || norm.median != 20 || norm.p90 != 30 || norm.newlinePercent != 0 {
		t.Fatalf("norm=%+v ok=%v, want 20/30 and no newlines from the 20 human messages only", norm, ok)
	}
	if _, ok := groupMessageNorm(history[:10], "bot", nil); ok {
		t.Fatal("too few human messages must not produce a length norm")
	}
}

// 群聊里够样本才进尾部，私聊不进。
func TestGroupLengthNormPromptOnlyForGroupsWithEnoughHistory(t *testing.T) {
	cfg := BotConfig{BotAccount: "bot"}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "100", SelfID: "bot"}
	if got := runtime.groupLengthNormPrompt(event, cfg); got != "" {
		t.Fatalf("empty history produced %q", got)
	}
	for i := 0; i < 20; i++ {
		item := event
		item.MessageID = fmt.Sprint(i)
		item.UserID = fmt.Sprint(100 + i%3)
		item.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "今天吃什么好呢"}}}
		runtime.remember(item)
	}
	got := runtime.groupLengthNormPrompt(event, cfg)
	if strings.Contains(got, "尽量不超过") {
		t.Fatalf("the group norm must not cap reply length any more: %q", got)
	}
	if !strings.Contains(got, "学的是上面群友的消息") {
		t.Fatalf("the group voice line is missing: %q", got)
	}
	if !strings.Contains(got, "一条消息只写一行") {
		t.Fatalf("a group that never breaks lines should be told so: %q", got)
	}
	private := event
	private.Kind = EventKindPrivate
	if runtime.groupLengthNormPrompt(private, cfg) != "" {
		t.Fatal("private chats must not get a group length norm")
	}
}

type normFallbackStore struct{ events []MessageEvent }

func (s *normFallbackStore) AppendMessageEvent(context.Context, string, MessageEvent) error {
	return nil
}

func (s *normFallbackStore) ListRecentMessageEvents(_ context.Context, _ string, limit int) ([]MessageEvent, error) {
	if len(s.events) > limit {
		return s.events[len(s.events)-limit:], nil
	}
	return s.events, nil
}

// 重启后内存里只有几条回补消息时，从库里补样本，「学群友的腔调」不能就此消失。
func TestGroupLengthNormPromptFallsBackToStoreAfterRestart(t *testing.T) {
	cfg := BotConfig{BotAccount: "bot"}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "100", SelfID: "bot"}
	store := &normFallbackStore{}
	for i := 0; i < 20; i++ {
		item := event
		item.MessageID = fmt.Sprint(i)
		item.UserID = fmt.Sprint(100 + i%3)
		item.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "今天吃什么好呢"}}}
		store.events = append(store.events, item)
	}
	runtime.remember(store.events[19])
	if got := runtime.groupLengthNormPrompt(event, cfg); got != "" {
		t.Fatalf("without a store the thin history must stay silent: %q", got)
	}
	runtime.SetMessageHistoryStore(store)
	if got := runtime.groupLengthNormPrompt(event, cfg); !strings.Contains(got, "学的是上面群友的消息") {
		t.Fatalf("the group voice line should come back from stored history: %q", got)
	}
}
