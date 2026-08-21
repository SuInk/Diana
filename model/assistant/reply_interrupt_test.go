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

// 撤回触发消息后，还没送出的回复必须在发送前放弃；分条投递已经开始的
// 后续分条不打断，避免留下半截话。
func TestRecallInterruptsPendingReply(t *testing.T) {
	runtime := replyInterruptTestRuntime()
	trigger := directedGroupMessage("20001", "10001", "帮我看看这个")

	gated := withReplyTriggerGate(context.Background())
	if err := runtime.interruptedReplyError(gated, trigger); err != nil {
		t.Fatalf("reply should pass before any recall: %v", err)
	}

	runtime.noteRecalledInbound(groupRecallNotice("20001", "10001"))
	if !runtime.inboundTriggerRecalled(trigger) {
		t.Fatal("recalled trigger was not detected")
	}
	if _, err := runtime.sendOutgoingWithResult(gated, trigger, OutgoingMessage{GroupID: "123456", Text: "旧回复"}); !errors.Is(err, errReplyTriggerRecalled) {
		t.Fatalf("first chunk should be interrupted, err = %v", err)
	}
	// 已经开始的分条投递不在中途打断。
	if err := runtime.interruptedReplyError(withContinuousOutboundDelivery(gated), trigger); err != nil {
		t.Fatalf("continuation chunks must not be interrupted: %v", err)
	}
	// 非回复类发送（通知、欢迎语）不带回复门标记，不受影响。
	if err := runtime.interruptedReplyError(context.Background(), trigger); err != nil {
		t.Fatalf("non-reply sends must not be gated: %v", err)
	}
	// 别的消息不受这次撤回影响。
	other := directedGroupMessage("20002", "10001", "另一个问题")
	if runtime.inboundTriggerRecalled(other) {
		t.Fatal("a different message was wrongly treated as recalled")
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

// 追发消息自己被撤回后，前一条消息的回复要恢复放行，不能两条都没人回。
func TestRecallOfFollowUpRestoresOriginalReply(t *testing.T) {
	runtime := replyInterruptTestRuntime()
	first := directedGroupMessage("20001", "10001", "帮我看看这个")
	followUp := directedGroupMessage("20002", "10001", "算了当我没说")
	runtime.noteDirectedInbound(first)
	runtime.noteDirectedInbound(followUp)
	if !runtime.inboundTriggerSuperseded(first) {
		t.Fatal("precondition: follow-up should supersede the first reply")
	}

	runtime.noteRecalledInbound(groupRecallNotice("20002", "10001"))
	if !runtime.inboundTriggerRecalled(followUp) {
		t.Fatal("recalled follow-up was not detected")
	}
	if runtime.inboundTriggerSuperseded(first) {
		t.Fatal("first reply must be released after the follow-up was recalled")
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
