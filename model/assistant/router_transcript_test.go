// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// TestProactiveReplyTranscriptReadsOldestFirst 线上漏判最多的形状：机器人刚 @ 某人答完，
// 那人紧接着追问一句。对话要按时间从早到晚排，当前消息在最后，谁回复谁、@ 了谁写在
// 名字后面，机器人的发言用它的称呼标出来。每行开头标着离现在多久，模型才看得出对话节奏。
func TestProactiveReplyTranscriptReadsOldestFirst(t *testing.T) {
	age := func(v int64) *int64 { return &v }
	payload := proactiveReplyPayload{
		Addressing:                    messageAddressing{ReplyTarget: "none"},
		CurrentText:                   "这么贵",
		CurrentSender:                 "Alice",
		BotAliases:                    []string{"嘉然", "Diana"},
		LastBotAddressedCurrentSender: true,
		// 从新到旧，和 proactiveReplyPayload 攒的顺序一致。
		RecentMessages: []proactiveReplyHistoryItem{
			{Sender: "Diana", IsBot: true, Text: "大概两三百块", AgeSeconds: age(5),
				Addressing: messageAddressing{ReplyTarget: "none", Mentions: []MessageMention{{UserID: "10002", Username: "Alice", Target: "other"}}}},
			{Sender: "Alice", Text: "测距仪多少钱", AgeSeconds: age(40),
				Addressing: messageAddressing{ReplyTarget: "none", Mentions: []MessageMention{{Target: "self"}}}},
			{Sender: "Bob", Text: "晚上吃啥", Images: 1, AgeSeconds: age(90),
				Addressing: messageAddressing{ReplyTarget: "other", ReplySender: "Alice"}},
		},
		NotebookContext: "测距仪=激光测距仪",
	}
	got := proactiveReplyTranscript(payload)
	want := []string{
		"机器人的称呼：嘉然、Diana",
		"对话按时间从早到晚：",
		"[1分钟前] Bob（回复Alice）：晚上吃啥 [图片×1]",
		"[40秒前] Alice（@嘉然）：测距仪多少钱",
		"[5秒前] 嘉然（机器人）（@Alice）：大概两三百块",
		"【当前消息】[刚刚] Alice：这么贵",
		"（嘉然最近一条发言是冲着当前发送者说的）",
		"",
		"群内术语：",
		"测距仪=激光测距仪",
	}
	if got != strings.Join(want, "\n") {
		t.Fatalf("transcript mismatch:\n%s", got)
	}
}

func TestProactiveReplyTranscriptMarksQuotedBot(t *testing.T) {
	payload := proactiveReplyPayload{
		Addressing:    messageAddressing{ReplyTarget: "self"},
		CurrentText:   "真的吗",
		CurrentSender: "Carol",
		QuotedText:    "白洲梓是《蔚蓝档案》里的角色",
		QuotedSender:  "Diana",
		QuotedIsBot:   true,
	}
	got := proactiveReplyTranscript(payload)
	if !strings.HasSuffix(got, "【当前消息】[刚刚] Carol（回复机器人）：真的吗（引用 机器人：白洲梓是《蔚蓝档案》里的角色）") {
		t.Fatalf("quoted bot message not rendered as the bot:\n%s", got)
	}
	if strings.Contains(got, "机器人的称呼") {
		t.Fatalf("no aliases configured, the alias line must be omitted:\n%s", got)
	}
}

// TestProactiveReplyPayloadInterruptedBotFollowUp 来自生产回放：机器人 @远野 说完，
// Ciallo 插了一句，远野接着说「你复现了」——问的是 Ciallo。「最近一条是冲着当前发送者
// 说的」那条提示会让模型跳过中间那句认成在问机器人，有别人插进来时不能再写。同一份
// 历史顺带验证 @ 别人的提及会补上昵称，不再一律写成「@别人」。
func TestProactiveReplyPayloadInterruptedBotFollowUp(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42", GroupTriggers: []string{"嘉然"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	text := func(s string) MessageSegment { return MessageSegment{Type: "text", Data: map[string]string{"text": s}} }
	at := func(id string) MessageSegment { return MessageSegment{Type: "at", Data: map[string]string{"qq": id}} }
	runtime.remember(MessageEvent{Kind: EventKindGroup, Time: 100, GroupID: "123456", UserID: "10001", MessageID: "m1", SenderName: "远野", Segments: []MessageSegment{text("你没开")}})
	runtime.remember(MessageEvent{Kind: EventKindGroup, Time: 110, GroupID: "123456", UserID: "42", MessageID: "m2", SenderName: "Diana", Segments: []MessageSegment{at("10001"), text("确实没开开关")}})
	runtime.remember(MessageEvent{Kind: EventKindGroup, Time: 120, GroupID: "123456", UserID: "10002", MessageID: "m3", SenderName: "Ciallo", Segments: []MessageSegment{text("我看看是什么bug")}})
	current := MessageEvent{Kind: EventKindGroup, Time: 130, GroupID: "123456", UserID: "10001", MessageID: "m4", SenderName: "远野", Segments: []MessageSegment{at("10002"), text("你复现了")}}

	payload := runtime.proactiveReplyPayload(current, PlainText(current.Segments))
	if !payload.LastBotAddressedCurrentSender || !payload.OthersSpokeAfterLastBot {
		t.Fatalf("addressed=%v othersSpoke=%v, want both true", payload.LastBotAddressedCurrentSender, payload.OthersSpokeAfterLastBot)
	}
	got := proactiveReplyTranscript(payload)
	if strings.Contains(got, "冲着当前发送者") {
		t.Fatalf("hint should be dropped after an interruption:\n%s", got)
	}
	for _, want := range []string{"嘉然（机器人）（@远野）：", "【当前消息】[刚刚] 远野（@Ciallo）："} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript missing %q:\n%s", want, got)
		}
	}

	// 没人插进来时提示照旧。
	direct := MessageEvent{Kind: EventKindGroup, Time: 115, GroupID: "123456", UserID: "10001", MessageID: "m5", SenderName: "远野", Segments: []MessageSegment{text("那怎么开")}}
	runtime2 := NewRuntime(BotConfig{BotAccount: "42", GroupTriggers: []string{"嘉然"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime2.remember(MessageEvent{Kind: EventKindGroup, Time: 110, GroupID: "123456", UserID: "42", MessageID: "m2", SenderName: "Diana", Segments: []MessageSegment{at("10001"), text("确实没开开关")}})
	if got := proactiveReplyTranscript(runtime2.proactiveReplyPayload(direct, "那怎么开")); !strings.Contains(got, "冲着当前发送者") {
		t.Fatalf("hint should stay for a direct follow-up:\n%s", got)
	}
}
