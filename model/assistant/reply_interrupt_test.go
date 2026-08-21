// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"testing"
)

func replyInterruptTestRuntime() *Runtime {
	return NewRuntime(BotConfig{GroupTriggers: []string{"Diana"}, BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
}

func directedGroupMessage(messageID, userID, text string) MessageEvent {
	return MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123456",
		UserID:     userID,
		MessageID:  messageID,
		RawMessage: "[CQ:at,qq=42] " + text,
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "42"}},
			{Type: "text", Data: map[string]string{"text": " " + text}},
		},
	}
}

func groupRecallNotice(messageID, userID string) MessageEvent {
	return MessageEvent{
		Kind:      EventKindNotice,
		SubType:   "group_recall",
		GroupID:   "123456",
		UserID:    userID,
		MessageID: messageID,
	}
}

// 用户只撤回没重发时机器人照常回答：撤回不取消回复，只在回复里剥掉指向
// 已撤回消息的引用装饰。
func TestRecalledTriggerStillGetsReply(t *testing.T) {
	runtime := replyInterruptTestRuntime()
	trigger := directedGroupMessage("20001", "10001", "帮我看看这个")
	runtime.noteDirectedInbound(trigger)

	runtime.noteRecalledInbound(groupRecallNotice("20001", "10001"))
	if !runtime.inboundTriggerRecalled(trigger) {
		t.Fatal("recall registry should remember the message")
	}
	gated := withReplyTriggerGate(context.Background())
	if err := runtime.interruptedReplyError(gated, trigger); err != nil {
		t.Fatalf("a recall without a resend must not interrupt the reply: %v", err)
	}
	if _, err := runtime.sendOutgoingWithResult(gated, trigger, OutgoingMessage{GroupID: "123456", Text: "回复照发"}); err != nil {
		t.Fatalf("reply to a recalled message should still be sent: %v", err)
	}
	// 别的消息不受这次撤回影响。
	other := directedGroupMessage("20002", "10001", "另一个问题")
	if runtime.inboundTriggerRecalled(other) {
		t.Fatal("a different message was wrongly treated as recalled")
	}
}

// 撤回后重发才是让旧回复让位的信号：重发的那条直呼消息接管这轮回答。
func TestRecallThenResendAnswersOnlyTheResend(t *testing.T) {
	runtime := replyInterruptTestRuntime()
	original := directedGroupMessage("20001", "10001", "帮我看看这个东习")
	runtime.noteDirectedInbound(original)
	runtime.noteRecalledInbound(groupRecallNotice("20001", "10001"))

	resend := directedGroupMessage("20002", "10001", "帮我看看这个东西")
	runtime.noteDirectedInbound(resend)

	gated := withReplyTriggerGate(context.Background())
	if _, err := runtime.sendOutgoingWithResult(gated, original, OutgoingMessage{GroupID: "123456", Text: "旧回复"}); !errors.Is(err, errReplyTriggerSuperseded) {
		t.Fatalf("the recalled original should yield to the resend, err = %v", err)
	}
	if runtime.inboundTriggerSuperseded(resend) {
		t.Fatal("the resend must keep its reply")
	}
}

// 同一用户在回复送出前又发来直呼消息时，旧回复放弃，由新消息一并回答。
func TestDirectedFollowUpSupersedesPendingReply(t *testing.T) {
	runtime := replyInterruptTestRuntime()
	first := directedGroupMessage("20001", "10001", "帮我看看这个")
	runtime.noteDirectedInbound(first)

	if runtime.inboundTriggerSuperseded(first) {
		t.Fatal("the latest directed message must not supersede itself")
	}

	followUp := directedGroupMessage("20002", "10001", "重点是性能那块")
	runtime.noteDirectedInbound(followUp)
	if !runtime.inboundTriggerSuperseded(first) {
		t.Fatal("follow-up directed message should supersede the pending reply")
	}
	if runtime.inboundTriggerSuperseded(followUp) {
		t.Fatal("the newest directed message must keep its reply")
	}
	gated := withReplyTriggerGate(context.Background())
	if _, err := runtime.sendOutgoingWithResult(gated, first, OutgoingMessage{GroupID: "123456", Text: "旧回复"}); !errors.Is(err, errReplyTriggerSuperseded) {
		t.Fatalf("superseded reply should be dropped before send, err = %v", err)
	}

	// 其他用户的直呼互不影响。
	otherUser := directedGroupMessage("20003", "10002", "我也问一句")
	runtime.noteDirectedInbound(otherUser)
	if runtime.inboundTriggerSuperseded(otherUser) {
		t.Fatal("messages from a different user must not interfere")
	}
}

// 群里没有叫机器人的消息既不登记，也不会因为登记表里有别的直呼被误判取代。
func TestUndirectedGroupMessageIsNeverSuperseded(t *testing.T) {
	runtime := replyInterruptTestRuntime()
	undirected := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123456",
		UserID:     "10001",
		MessageID:  "20001",
		RawMessage: "随便聊聊",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "随便聊聊"}}},
	}
	runtime.noteDirectedInbound(undirected)
	directed := directedGroupMessage("20002", "10001", "看下这个")
	runtime.noteDirectedInbound(directed)
	// 主动插话的触发不是直呼，不参与追发取代。
	if runtime.inboundTriggerSuperseded(undirected) {
		t.Fatal("an undirected trigger must not be superseded by the directed registry")
	}
}

// 追发消息自己被撤回也不改变归属：撤回的消息照样被回答，旧回复保持让位，
// 不会出现两条都回或两条都没人回。
func TestRecalledFollowUpStillOwnsTheReply(t *testing.T) {
	runtime := replyInterruptTestRuntime()
	first := directedGroupMessage("20001", "10001", "帮我看看这个")
	followUp := directedGroupMessage("20002", "10001", "重点看性能")
	runtime.noteDirectedInbound(first)
	runtime.noteDirectedInbound(followUp)
	runtime.noteRecalledInbound(groupRecallNotice("20002", "10001"))

	if !runtime.inboundTriggerSuperseded(first) {
		t.Fatal("first reply must stay superseded by the follow-up")
	}
	gated := withReplyTriggerGate(context.Background())
	if err := runtime.interruptedReplyError(gated, followUp); err != nil {
		t.Fatalf("the recalled follow-up still gets its reply: %v", err)
	}
}

// 私聊里每条消息都算直呼：快速连发两条时旧回复放弃，由新一轮串行处理时
// 结合两条消息一起回答。
func TestPrivateFollowUpSupersedesPendingReply(t *testing.T) {
	runtime := replyInterruptTestRuntime()
	first := MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "10001",
		MessageID:  "30001",
		RawMessage: "帮我查下天气",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "帮我查下天气"}}},
	}
	followUp := MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "10001",
		MessageID:  "30002",
		RawMessage: "还有明天的",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "还有明天的"}}},
	}
	runtime.noteDirectedInbound(first)
	runtime.noteDirectedInbound(followUp)
	if !runtime.inboundTriggerSuperseded(first) {
		t.Fatal("private follow-up should supersede the pending reply")
	}
	if runtime.inboundTriggerSuperseded(followUp) {
		t.Fatal("the newest private message must keep its reply")
	}
}
