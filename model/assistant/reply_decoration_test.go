// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestReplyDecorationModeFallsBackToLegacyToggle(t *testing.T) {
	off := false
	on := true
	if got := normalizeReplyDecorationMode("", &off); got != ReplyDecorationOff {
		t.Fatalf("legacy false = %q, want off", got)
	}
	if got := normalizeReplyDecorationMode("", &on); got != ReplyDecorationOn {
		t.Fatalf("legacy true = %q, want on", got)
	}
	if got := normalizeReplyDecorationMode("", nil); got != ReplyDecorationOn {
		t.Fatalf("unset = %q, want on", got)
	}
	// 显式选过的档位不能被旧开关翻回去：升级后 auto + 遗留 true 必须仍是 auto。
	if got := normalizeReplyDecorationMode(" AUTO ", &on); got != ReplyDecorationAuto {
		t.Fatalf("auto with legacy true = %q, want auto", got)
	}
	if got := normalizeReplyDecorationMode("nonsense", &off); got != ReplyDecorationOff {
		t.Fatalf("invalid mode = %q, want legacy off", got)
	}
}

// TestSendAutoModeLeavesReferenceAndMentionToModel 验证 auto 档运行时不再自动补装饰件。
func TestSendAutoModeLeavesReferenceAndMentionToModel(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{
		ReplyReferenceMode: ReplyDecorationAuto,
		MentionUserMode:    ReplyDecorationAuto,
	}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "1244393238"}

	if err := runtime.send(context.Background(), event, "你好"); err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if channel.sent[0].ReplyMessageID != "" || channel.sent[0].MentionUserID != "" {
		t.Fatalf("auto mode still decorated the reply: %#v", channel.sent[0])
	}
}

// TestSendAutoModeKeepsModelWrittenReplyMarker 验证 auto 档下模型自己写的引用标记仍然生效。
func TestSendAutoModeKeepsModelWrittenReplyMarker(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{
		ReplyReferenceMode: ReplyDecorationAuto,
		MentionUserMode:    ReplyDecorationAuto,
	}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "1244393238"}

	if err := runtime.send(context.Background(), event, replyMarkerPrefix+"1244393238] 你好"); err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if channel.sent[0].ReplyMessageID != "1244393238" {
		t.Fatalf("model written reply marker was dropped: %#v", channel.sent[0])
	}
	if strings.Contains(channel.sent[0].Text, replyMarkerPrefix) {
		t.Fatalf("reply marker leaked into the message text: %#v", channel.sent[0])
	}
}

func TestReplyDecorationPromptOnlyGuidesAutoMode(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "1244393238"}
	if prompt := replyDecorationPrompt(BotConfig{}.WithDefaults(), event); prompt != "" {
		t.Fatalf("default config should not emit decoration guidance: %q", prompt)
	}

	cfg := BotConfig{ReplyReferenceMode: ReplyDecorationAuto, MentionUserMode: ReplyDecorationAuto}.WithDefaults()
	prompt := replyDecorationPrompt(cfg, event)
	if !strings.Contains(prompt, replyMarkerPrefix+"1244393238]") {
		t.Fatalf("auto prompt is missing the current message marker: %q", prompt)
	}
	if !strings.Contains(prompt, "@10001") {
		t.Fatalf("auto prompt is missing the sender mention hint: %q", prompt)
	}
	// 私聊没有引用和 @ 的概念，不该占用上下文。
	if direct := replyDecorationPrompt(cfg, MessageEvent{Kind: EventKindPrivate, UserID: "10001"}); direct != "" {
		t.Fatalf("private chat should not emit decoration guidance: %q", direct)
	}
}
