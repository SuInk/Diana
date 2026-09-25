// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
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
	norm, ok := groupMessageNorm(history, "bot")
	if !ok || norm.median != 20 || norm.p90 != 30 || norm.newlinePercent != 0 {
		t.Fatalf("norm=%+v ok=%v, want 20/30 and no newlines from the 20 human messages only", norm, ok)
	}
	if _, ok := groupMessageNorm(history[:10], "bot"); ok {
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
	if !strings.Contains(got, "一般 5 字左右") || !strings.Contains(got, "尽量不超过 15 字") {
		t.Fatalf("group length norm = %q, want the median and the 15-rune floor", got)
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
