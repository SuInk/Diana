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

// 订阅推送和聊天回复对 @ 的诉求相反：聊天里每句都 @ 很烦人，所以有 auto/off；
// 但提醒和订阅是过了很久之后主动找某个人，正文是模板或后台任务生成的，没有模型
// 帮它写 @，被那个开关连坐的结果就是订阅者在群里永远收不到点名。
func TestSubscriberNoticeMentionsIgnoringChatMentionMode(t *testing.T) {
	withFastSendTiming(t)
	for _, mode := range []ReplyDecorationMode{ReplyDecorationAuto, ReplyDecorationOff, ReplyDecorationOn} {
		channel := &scriptedChannel{}
		runtime := NewRuntime(BotConfig{
			ReplyReferenceMode: mode,
			MentionUserMode:    mode,
		}, channel, NewPluginManager(), nil, nil, nil, nil)
		event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001"}

		if err := runtime.sendSubscriberNotice(context.Background(), event, "提醒你：该喝水了"); err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if len(channel.sent) != 1 {
			t.Fatalf("mode %q sent = %#v", mode, channel.sent)
		}
		if channel.sent[0].MentionUserID != "10001" {
			t.Fatalf("mode %q did not mention the subscriber: %#v", mode, channel.sent[0])
		}
		// 触发它的那条消息可能是几天前的，引用没有意义。
		if channel.sent[0].ReplyMessageID != "" {
			t.Fatalf("mode %q attached a reply reference: %#v", mode, channel.sent[0])
		}
	}
}

// 私聊本来就只有他一个人，@ 没有意义，也不该冒出一个 at 段。
func TestSubscriberNoticeDoesNotMentionInPrivateChat(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{MentionUserMode: ReplyDecorationOn}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001"}

	if err := runtime.sendSubscriberNotice(context.Background(), event, "提醒你：该喝水了"); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].MentionUserID != "" {
		t.Fatalf("private notice = %#v", channel.sent)
	}
}

// 仓库订阅走的是另一条投递函数（通知卡片不按人设分条），@ 也要跟上。
func TestRepositoryNotificationMentionsSubscriber(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{MentionUserMode: ReplyDecorationOff}, channel, NewPluginManager(), nil, nil, nil, nil)

	group := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001"}
	if err := runtime.sendNotification(context.Background(), group, "仓库有新动态"); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].MentionUserID != "10001" {
		t.Fatalf("repository notice = %#v", channel.sent)
	}

	// 纯群目标（没记订阅人）没有可 @ 的对象，就老实不 @。
	channel.sent = nil
	anonymous := MessageEvent{Kind: EventKindGroup, GroupID: "123456"}
	if err := runtime.sendNotification(context.Background(), anonymous, "仓库有新动态"); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].MentionUserID != "" {
		t.Fatalf("anonymous group notice = %#v", channel.sent)
	}
}

// 普通聊天回复不受这次改动影响：off 就是一条都不 @。
func TestChatReplyStillFollowsMentionMode(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{MentionUserMode: ReplyDecorationOff}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "42"}

	if err := runtime.send(context.Background(), event, "你好"); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || channel.sent[0].MentionUserID != "" {
		t.Fatalf("chat reply = %#v", channel.sent)
	}
}
