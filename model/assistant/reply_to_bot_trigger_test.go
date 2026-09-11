// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func replyTriggerRuntime(t *testing.T) (*Runtime, BotConfig) {
	t.Helper()
	cfg := BotConfig{BotAccount: "10001", GroupTriggers: []string{"嘉然"}}.WithDefaults()
	return NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil), cfg
}

// 引用或回复机器人的消息一律进回复流程，和 @ 它一样，不再交给接话评分。
// 以前纯引用走语义判定：群友引用机器人怼一句「没人问你」，评分把三项都记 0.00，
// 相关度与闲聊双 0 在 ratingsAllow 里是硬否决，结果是被引用却不理人。
func TestReplyingToBotAlwaysTriggersReply(t *testing.T) {
	runtime, cfg := replyTriggerRuntime(t)
	for _, tc := range []struct {
		name  string
		event MessageEvent
		text  string
	}{
		{
			name:  "纯引用且内容是怼它",
			event: MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", SelfID: "10001", Quoted: &QuotedMessage{MessageID: "m1", UserID: "10001"}},
			text:  "没人问你",
		},
		{
			name:  "引用加提问",
			event: MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", SelfID: "10001", Quoted: &QuotedMessage{MessageID: "m1", UserID: "10001"}},
			text:  "这个怎么算的",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !runtime.shouldHandleChatTrigger(tc.event, tc.text) {
				t.Fatalf("引用机器人的消息没有进回复流程：%q", tc.text)
			}
		})
	}
	_ = cfg
}

// 引用别人的消息不算在叫机器人，仍然交给接话评分，别顺手把整组引用都放行了。
func TestReplyingToSomeoneElseStillNeedsRouting(t *testing.T) {
	runtime, _ := replyTriggerRuntime(t)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", SelfID: "10001", Quoted: &QuotedMessage{MessageID: "m1", UserID: "99999"}}
	if runtime.shouldHandleChatTrigger(event, "你说得对") {
		t.Fatal("引用其他群友的消息不该直接触发回复")
	}
}

// 既没 @ 也没引用、也没叫名字的普通群聊，仍然由接话评分决定。
func TestPlainGroupChatStillNeedsRouting(t *testing.T) {
	runtime, _ := replyTriggerRuntime(t)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", SelfID: "10001"}
	if runtime.shouldHandleChatTrigger(event, "今天好冷") {
		t.Fatal("普通群聊不该直接触发回复")
	}
}

// 叫名字这条路径不受影响。
func TestAliasStillTriggersReply(t *testing.T) {
	runtime, _ := replyTriggerRuntime(t)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", SelfID: "10001"}
	if !runtime.shouldHandleChatTrigger(event, "嘉然 在吗") {
		t.Fatal("叫名字应当直接触发回复")
	}
}
