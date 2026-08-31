// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"testing"
	"time"
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
	if runtime.inboundTriggerSuperseded(context.Background(), resend) {
		t.Fatal("the resend must keep its reply")
	}
}

// 同一用户在回复送出前又发来直呼消息时，旧回复放弃，由新消息一并回答。
func TestDirectedFollowUpSupersedesPendingReply(t *testing.T) {
	runtime := replyInterruptTestRuntime()
	first := directedGroupMessage("20001", "10001", "帮我看看这个")
	runtime.noteDirectedInbound(first)

	if runtime.inboundTriggerSuperseded(context.Background(), first) {
		t.Fatal("the latest directed message must not supersede itself")
	}

	followUp := directedGroupMessage("20002", "10001", "重点是性能那块")
	runtime.noteDirectedInbound(followUp)
	if !runtime.inboundTriggerSuperseded(context.Background(), first) {
		t.Fatal("follow-up directed message should supersede the pending reply")
	}
	if runtime.inboundTriggerSuperseded(context.Background(), followUp) {
		t.Fatal("the newest directed message must keep its reply")
	}
	gated := withReplyTriggerGate(context.Background())
	if _, err := runtime.sendOutgoingWithResult(gated, first, OutgoingMessage{GroupID: "123456", Text: "旧回复"}); !errors.Is(err, errReplyTriggerSuperseded) {
		t.Fatalf("superseded reply should be dropped before send, err = %v", err)
	}

	// 其他用户的直呼互不影响。
	otherUser := directedGroupMessage("20003", "10002", "我也问一句")
	runtime.noteDirectedInbound(otherUser)
	if runtime.inboundTriggerSuperseded(context.Background(), otherUser) {
		t.Fatal("messages from a different user must not interfere")
	}
}

// 主动插话那一轮不会被「本轮开始之前就在登记表里」的直呼误判取代——登记表只记
// 直呼，光比 ID 会把更早的那条也算成更新的。
func TestUndirectedGroupMessageIsNotSupersededByEarlierDirected(t *testing.T) {
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
	// 拿不到本轮起点时一律不取代：分不清那条直呼是本轮之前还是之后来的。
	if runtime.inboundTriggerSuperseded(context.Background(), undirected) {
		t.Fatal("an undirected trigger must not be superseded without a turn start")
	}
	// 本轮开始于那条直呼之后，说明它是旧的，不该让位。
	started := withReplyTurnStart(context.Background(), time.Now().Add(time.Second))
	if runtime.inboundTriggerSuperseded(started, undirected) {
		t.Fatal("a directed message older than this turn must not supersede it")
	}
}

// 前一条随口一说被主动插话接了，人还没说完又直接叫机器人：插话那一轮让位，
// 由直呼那一轮一并回答，免得连着两条几乎一样的话。
func TestDirectedMessageSupersedesInFlightProactiveReply(t *testing.T) {
	runtime := replyInterruptTestRuntime()
	chatter := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123456",
		UserID:     "10001",
		MessageID:  "20001",
		RawMessage: "ollama pro 不是 20 刀一个月嘛",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "ollama pro 不是 20 刀一个月嘛"}}},
	}
	// 不是直呼，登记表里不会有它。
	runtime.noteDirectedInbound(chatter)

	// 插话这一轮已经开始生成，随后这个人直接叫了机器人。
	turnStart := time.Now().Add(-time.Second)
	gated := withReplyTurnStart(withReplyTriggerGate(context.Background()), turnStart)
	runtime.noteDirectedInbound(directedGroupMessage("20002", "10001", "嘉然查下"))

	if !runtime.inboundTriggerSuperseded(gated, chatter) {
		t.Fatal("an in-flight proactive reply should yield to the directed message that followed it")
	}
	if _, err := runtime.sendOutgoingWithResult(gated, chatter, OutgoingMessage{GroupID: "123456", Text: "插话回复"}); !errors.Is(err, errReplyTriggerSuperseded) {
		t.Fatalf("the proactive reply should be dropped before send, err = %v", err)
	}

	// 别人的直呼不影响这一轮。
	other := MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123456",
		UserID:     "10009",
		MessageID:  "20003",
		RawMessage: "我也说一句",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "我也说一句"}}},
	}
	if runtime.inboundTriggerSuperseded(gated, other) {
		t.Fatal("a directed message from another user must not supersede this turn")
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

	if !runtime.inboundTriggerSuperseded(context.Background(), first) {
		t.Fatal("first reply must stay superseded by the follow-up")
	}
	gated := withReplyTriggerGate(context.Background())
	if err := runtime.interruptedReplyError(gated, followUp); err != nil {
		t.Fatalf("the recalled follow-up still gets its reply: %v", err)
	}
}

// 私聊队列按会话串行：追发消息不丢掉已经生成的第一条回复。第一条发出后
// 进入历史，第二轮再参考它继续回答。
func TestPrivateFollowUpKeepsPendingReply(t *testing.T) {
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
	if runtime.inboundTriggerSuperseded(context.Background(), first) {
		t.Fatal("private follow-up must not discard the pending reply")
	}
	if runtime.inboundTriggerSuperseded(context.Background(), followUp) {
		t.Fatal("the newest private message must keep its reply")
	}
	gated := withReplyTriggerGate(context.Background())
	if err := runtime.interruptedReplyError(gated, first); err != nil {
		t.Fatalf("pending private reply should still be delivered: %v", err)
	}
}

// 链接解析的投递不该被追发打断。
//
// 闸门的含义是「这次发送是模型对这条消息的回复」，那种输出被取代是安全的：
// 新的一轮会把前后两条一起答。解析结果不满足这个前提——新来的那一轮自己没带
// 链接、不会触发解析，模型也拿不到视频信息，卡片丢了就再也不会出现。所以它
// 摘掉闸门发送，和后台插件任务用 rootCtx 发送是同一个做法。
func TestResolverDeliveryIsNotSupersededByFollowUp(t *testing.T) {
	runtime := replyInterruptTestRuntime()
	link := directedGroupMessage("20001", "10001", "https://www.bilibili.com/video/BV1M64y1a7zh/")
	runtime.noteDirectedInbound(link)

	followUp := directedGroupMessage("20002", "10001", "这个怎么样")
	runtime.noteDirectedInbound(followUp)
	if !runtime.inboundTriggerSuperseded(context.Background(), link) {
		t.Fatal("前置条件：追发应当取代待发的普通回复")
	}

	// 普通回复被取代是对的。
	gated := withReplyTriggerGate(context.Background())
	if _, err := runtime.sendOutgoingWithResult(gated, link, OutgoingMessage{GroupID: "123456", Text: "普通回复"}); !errors.Is(err, errReplyTriggerSuperseded) {
		t.Fatalf("普通回复应当被取代，err = %v", err)
	}

	// 解析投递必须照发。
	resolverCtx := withoutReplyTriggerGate(gated)
	if err := runtime.interruptedReplyError(resolverCtx, link); err != nil {
		t.Fatalf("解析投递不该被追发打断: %v", err)
	}
	if _, err := runtime.sendOutgoingWithResult(resolverCtx, link, OutgoingMessage{
		GroupID: "123456", Text: "【标题】…", ImageURLs: []string{"https://example.invalid/cover.jpg"},
	}); err != nil {
		t.Fatalf("解析结果应当照常发出: %v", err)
	}
	// 解析卡片实际多走合并转发这条路，它是另一个发送入口，同样不能被拦。
	nodes := []map[string]any{{"type": "node", "data": map[string]any{"content": "【标题】…"}}}
	if _, err := runtime.sendForwardNodesWithResult(resolverCtx, link, nodes); err != nil {
		t.Fatalf("合并转发形式的解析结果也应当照常发出: %v", err)
	}
	// 没有解析标记时，合并转发仍然照常被追发取代。
	if _, err := runtime.sendForwardNodesWithResult(gated, link, nodes); !errors.Is(err, errReplyTriggerSuperseded) {
		t.Fatalf("普通的合并转发回复应当被取代，err = %v", err)
	}
}
