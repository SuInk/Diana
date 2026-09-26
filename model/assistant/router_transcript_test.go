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
