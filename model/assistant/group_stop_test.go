// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

// rememberGroupBotReply 在群历史里放一条机器人刚说过的话，好让叫停判断够得着时间门槛。
func rememberGroupBotReply(runtime *Runtime, messageID string, at time.Time) {
	runtime.remember(MessageEvent{
		Kind: EventKindGroup, GroupID: "g", UserID: "42", SelfID: "42", MessageID: messageID, Outbound: true,
		Time: at.Unix(), RawMessage: "两者是与的关系",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "两者是与的关系"}}},
	})
}

func groupMentionEvent(userID, messageID, text string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "g", SelfID: "42", UserID: userID, MessageID: messageID, ToMe: true,
		Time: time.Now().Unix(), RawMessage: "[CQ:at,qq=42] " + text,
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "42"}},
			{Type: "text", Data: map[string]string{"text": " " + text}},
		},
	}
}

func groupPlainEvent(userID, messageID, text string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "g", SelfID: "42", UserID: userID, MessageID: messageID,
		Time: time.Now().Unix(), RawMessage: text,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

func newGroupStopRuntime(provider *privateClosingProvider, channel Channel) *Runtime {
	return NewRuntime(BotConfig{
		BotAccount: "42", OwnerID: "owner", AgentEnabled: false, BotReplyLoopDetectionEnabled: boolPointer(false),
		Participation: &ParticipationPreferences{Desire: 100},
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
}

// TestGroupStopRegistersFromExplicitStopAndStillAcknowledges 被 @ 着说「闭嘴」：那句
// 「好的」照发，同时给本群记下静默窗口；不像私聊那样把这个人暂停半小时。
func TestGroupStopRegistersFromExplicitStopAndStillAcknowledges(t *testing.T) {
	provider := &privateClosingProvider{audits: []string{auditStop}, replies: []string{"好的，我闭嘴！"}}
	channel := &recordingChannel{}
	runtime := newGroupStopRuntime(provider, channel)

	rememberGroupBotReply(runtime, "bot-1", time.Now())
	event := groupMentionEvent("576951401", "stop-1", "闭嘴")
	outcome, err := runtime.replyAndRecord(context.Background(), event, event.RawMessage, "replied")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "replied" {
		t.Fatalf("outcome = %q, want replied", outcome)
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || !strings.Contains(sent[0].Text, "闭嘴") {
		t.Fatalf("the acknowledgement must still go out, sent %#v", sent)
	}
	if _, active := runtime.activeGroupStop(event, time.Now()); !active {
		t.Fatal("an explicit stop request did not open the group stop window")
	}
	if _, blocked := runtime.activeReplySuppression(event, time.Now()); blocked {
		t.Fatal("a group stop must not lock the requester out for half an hour")
	}
}

// TestGroupStopSkipsRouterAndDropsInFlightProactiveReply 窗口内接话评分不跑模型直接
// 沉默；叫停到达时已经在生成的接话回复发送前丢掉；@ 它的照答。
func TestGroupStopSkipsRouterAndDropsInFlightProactiveReply(t *testing.T) {
	provider := &privateClosingProvider{replies: []string{"确实，图片进来得靠视觉理解跑", "在的"}}
	channel := &recordingChannel{}
	runtime := newGroupStopRuntime(provider, channel)

	if _, ok := runtime.noteGroupStop(groupPlainEvent("576951401", "stop-2", "闭嘴"), "对方说闭嘴", time.Now()); !ok {
		t.Fatal("noteGroupStop() = false")
	}

	plain := groupPlainEvent("3083158904", "after-1", "视觉理解啊")
	routed, _, _, allowed := runtime.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: plain, Text: plain.RawMessage}})
	if allowed || !strings.Contains(routed.routingReason, "闭嘴") {
		t.Fatalf("router must stay silent inside the window: allowed=%t reason=%q", allowed, routed.routingReason)
	}
	if audits, replies := provider.counts(); audits != 0 || replies != 0 {
		t.Fatalf("the router must not call the model inside the window: audits=%d replies=%d", audits, replies)
	}

	inFlight := groupPlainEvent("3083158904", "in-flight", "视觉理解啊")
	inFlight.proactiveReply = true
	outcome, err := runtime.replyAndRecord(context.Background(), inFlight, inFlight.RawMessage, "replied_proactive")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "ignored_stop_requested" {
		t.Fatalf("outcome = %q, want ignored_stop_requested", outcome)
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("an in-flight proactive reply leaked past the stop window: %#v", sent)
	}

	mention := groupMentionEvent("3083158904", "mention-1", "在吗")
	outcome, err = runtime.replyAndRecord(context.Background(), mention, mention.RawMessage, "replied")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "replied" || len(channel.sentSnapshot()) != 1 {
		t.Fatalf("a direct mention inside the window must still be answered: outcome=%q sent=%d", outcome, len(channel.sentSnapshot()))
	}
}

func TestGroupStopWindowExpires(t *testing.T) {
	runtime := newGroupStopRuntime(&privateClosingProvider{}, nilChannel{})
	event := groupPlainEvent("u", "m", "闭嘴")
	now := time.Now()
	if _, ok := runtime.noteGroupStop(event, "", now); !ok {
		t.Fatal("noteGroupStop() = false")
	}
	if _, active := runtime.activeGroupStop(event, now.Add(groupStopWindow-time.Second)); !active {
		t.Fatal("window ended early")
	}
	if _, active := runtime.activeGroupStop(event, now.Add(groupStopWindow+time.Second)); active {
		t.Fatal("window did not expire")
	}
	other := event
	other.GroupID = "other"
	if _, active := runtime.activeGroupStop(other, now); active {
		t.Fatal("a stop in one group leaked into another")
	}
}

// TestGroupStopAuditOnlyForDirectMessagesAfterBotSpoke 叫停判断只加给冲着机器人来的
// 群消息，而且机器人得刚在这个群里说过话；别的消息不为它多问一项。
func TestGroupStopAuditOnlyForDirectMessagesAfterBotSpoke(t *testing.T) {
	runtime := newGroupStopRuntime(&privateClosingProvider{}, nilChannel{})
	cfg := runtime.ProfileConfig("")
	now := time.Now()
	plain := groupPlainEvent("u", "p", "闭嘴")
	mention := groupMentionEvent("u", "m", "闭嘴")
	if runtime.groupStopAuditDue(mention, cfg, "闭嘴", now) {
		t.Fatal("no bot speech yet, nothing to stop")
	}
	rememberGroupBotReply(runtime, "bot-2", now)
	if !runtime.groupStopAuditDue(mention, cfg, "闭嘴", now) {
		t.Fatal("a mention right after the bot spoke must be audited for a stop request")
	}
	if runtime.groupStopAuditDue(plain, cfg, "闭嘴", now) {
		t.Fatal("a message not addressed to the bot is the router's business, not the audit's")
	}
	if runtime.groupStopAuditDue(mention, cfg, "闭嘴", now.Add(privateClosingAuditWindow+time.Minute)) {
		t.Fatal("long after the bot spoke there is nothing to stop")
	}
}
