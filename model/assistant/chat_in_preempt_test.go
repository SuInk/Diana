// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"testing"
	"time"
)

func chatInPreemptEvent(group, user, message string, chatIn bool) MessageEvent {
	return MessageEvent{Kind: EventKindGroup, GroupID: group, UserID: user, MessageID: message, proactiveReply: chatIn, chatInReply: chatIn}
}

func chatInReplyCtx(start time.Time, turnID string) context.Context {
	return withReplyTurnStart(withOutboundTurn(context.Background(), turnID), start)
}

// 生产上那次：两个人隔两秒说话，两轮主动接话并行生成，后发出的那条和前一条几乎一样。
func TestChatInDroppedWhenOwnReplySentDuringTurn(t *testing.T) {
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	start := time.Now()
	first := chatInPreemptEvent("10001", "u1", "m1", true)
	second := chatInPreemptEvent("10001", "u2", "m2", true)
	// 两轮都在第一条发出之前开始：第二轮开始得稍晚，但仍早于第一条发出。
	firstCtx := chatInReplyCtx(start.Add(-2*time.Second), "turn-1")
	secondCtx := chatInReplyCtx(start.Add(-time.Second), "turn-2")

	if _, err := runtime.sendOutgoingWithResult(firstCtx, first, OutgoingMessage{GroupID: "10001", Text: "十八个月的小猫要被画成老太太"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.sendOutgoingWithResult(secondCtx, second, OutgoingMessage{GroupID: "10001", Text: "十八个月的猫要画成老奶奶"}); !errors.Is(err, errChatInPreemptedByOwnReply) {
		t.Fatalf("second chat-in err = %v, want preempted", err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	// 第一轮自己分条发的第二句不被自己的第一句拦住。
	if _, err := runtime.sendOutgoingWithResult(firstCtx, first, OutgoingMessage{GroupID: "10001", Text: "第二句"}); err != nil {
		t.Fatalf("follow-up part of the same turn was blocked: %v", err)
	}
}

func TestChatInPreemptOnlyAffectsUnaddressedSameGroupReplies(t *testing.T) {
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	start := time.Now()
	if _, err := runtime.sendOutgoingWithResult(chatInReplyCtx(start, "turn-0"), chatInPreemptEvent("10001", "u1", "m1", true), OutgoingMessage{GroupID: "10001", Text: "先发的"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		event      MessageEvent
		startAfter bool
	}{
		{"directed reply", chatInPreemptEvent("10001", "u2", "m2", false), false},
		{"other group", chatInPreemptEvent("10002", "u2", "m3", true), false},
		// 开始时间取在上面几条都发完之后（含同群的那条被叫到的回复）。
		{"turn started after own reply", chatInPreemptEvent("10001", "u3", "m4", true), true},
	}
	for index, tc := range cases {
		turnStart := start
		if tc.startAfter {
			turnStart = time.Now().Add(time.Second)
		}
		msg := OutgoingMessage{GroupID: tc.event.GroupID, Text: tc.name}
		if _, err := runtime.sendOutgoingWithResult(chatInReplyCtx(turnStart, "turn-"+tc.event.MessageID), tc.event, msg); err != nil {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
		if sent := channel.sentSnapshot(); len(sent) != index+2 {
			t.Fatalf("%s: sent %d messages", tc.name, len(sent))
		}
	}
}

func TestChatInPreemptOutcomeDescribed(t *testing.T) {
	decision, reason, handled := DescribeEventOutcome("superseded_proactive_own_reply")
	if decision != "not_replied" || reason != chatInPreemptedReason || handled {
		t.Fatalf("decision=%s reason=%s handled=%v", decision, reason, handled)
	}
}
