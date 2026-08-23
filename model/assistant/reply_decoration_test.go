// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 归一化要吃掉大小写和空白，没写或写错都按 on 处理。
func TestNormalizeReplyDecorationMode(t *testing.T) {
	cases := map[ReplyDecorationMode]ReplyDecorationMode{
		"":         ReplyDecorationOn,
		"on":       ReplyDecorationOn,
		"off":      ReplyDecorationOff,
		" AUTO ":   ReplyDecorationAuto,
		"nonsense": ReplyDecorationOn,
	}
	for input, want := range cases {
		if got := normalizeReplyDecorationMode(input); got != want {
			t.Fatalf("normalizeReplyDecorationMode(%q) = %q, want %q", input, got, want)
		}
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
	if prompt := replyDecorationPrompt(BotConfig{}.WithDefaults(), event, nil); prompt != "" {
		t.Fatalf("default config should not emit decoration guidance: %q", prompt)
	}

	cfg := BotConfig{ReplyReferenceMode: ReplyDecorationAuto, MentionUserMode: ReplyDecorationAuto}.WithDefaults()
	prompt := replyDecorationPrompt(cfg, event, nil)
	if !strings.Contains(prompt, replyMarkerPrefix+"1244393238]") {
		t.Fatalf("auto prompt is missing the current message marker: %q", prompt)
	}
	if !strings.Contains(prompt, "@10001") {
		t.Fatalf("auto prompt is missing the sender mention hint: %q", prompt)
	}
	// 私聊没有引用和 @ 的概念，不该占用上下文。
	if direct := replyDecorationPrompt(cfg, MessageEvent{Kind: EventKindPrivate, UserID: "10001"}, nil); direct != "" {
		t.Fatalf("private chat should not emit decoration guidance: %q", direct)
	}
}

// 追发合并后合并回复没有锚点指向前一条,发的人会觉得前一条被跳过。
// 连发未回时提示词必须点出那条消息并建议引用它。
func TestReplyDecorationPromptAnchorsPendingEarlierMessage(t *testing.T) {
	cfg := BotConfig{ReplyReferenceMode: ReplyDecorationAuto}
	current := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u1", MessageID: "222", Time: 10000,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "以及pr合并"}}}}
	earlier := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u1", MessageID: "111", Time: 9995,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "commit提交也不用每次都推"}}}}

	prompt := replyDecorationPrompt(cfg, current, []MessageEvent{earlier, current})
	if !strings.Contains(prompt, "你还没有回复") || !strings.Contains(prompt, "commit提交也不用每次都推") {
		t.Fatalf("连发未回时应点出上一条:%s", prompt)
	}
	// 承接靠措辞完成,不建议引用——引用框太重,连发场景里真人也只是接着说。
	if strings.Contains(prompt, replyMarkerPrefix+"111]") {
		t.Fatalf("不该建议引用更早那条消息:%s", prompt)
	}

	// 中间隔着机器人回复,说明上一条已经回过,不算连发未回。
	botReply := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "bot", MessageID: "150", Time: 9997, Outbound: true,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "收到"}}}}
	prompt = replyDecorationPrompt(cfg, current, []MessageEvent{earlier, botReply, current})
	if strings.Contains(prompt, "你还没有回复") {
		t.Fatalf("已回复过的不该再提示承接:%s", prompt)
	}

	// 隔太久的两条消息是两个话题,不绑在一起。
	stale := earlier
	stale.Time = current.Time - int64(pendingEarlierMessageWindow/time.Second) - 1
	prompt = replyDecorationPrompt(cfg, current, []MessageEvent{stale, current})
	if strings.Contains(prompt, "你还没有回复") {
		t.Fatalf("超出连发窗口不该提示:%s", prompt)
	}

	// 别人的消息不算自己的连发。
	other := earlier
	other.UserID = "u2"
	prompt = replyDecorationPrompt(cfg, current, []MessageEvent{other, current})
	if strings.Contains(prompt, "你还没有回复") {
		t.Fatalf("他人消息不该触发承接提示:%s", prompt)
	}
}
