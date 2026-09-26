// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 这些用例都从回复入口（replyAndRecord）或预处理入口走，覆盖「连发取代」可能让
// 一条消息谁都不回的几种情况。

// 提醒刚找过这个人时：随口的主动接话在回复入口被放掉；评分判定「就是在跟机器人
// 说话」的主动接话照常回。
func TestReminderSuppressesOnlyLowIntentChatIn(t *testing.T) {
	provider := &burstReplyProvider{}
	runtime, channel := burstTestRuntime(provider)
	chatIn := textEvent("20091", "10001", "好的我知道了", 1_800_000_000)
	chatIn.proactiveReply, chatIn.chatInReply = true, true
	runtime.remember(chatIn)
	runtime.noteTriggeredDelivery(chatIn)

	if outcome, err := runtime.replyAndRecord(context.Background(), chatIn, "好的我知道了", "replied_proactive"); err != nil || outcome != "ignored_trigger_covered" {
		t.Fatalf("low-intent chat-in outcome=%q err=%v", outcome, err)
	}
	directed := textEvent("20092", "10001", "那你明天再提醒我一次行吗", 1_800_000_010)
	directed.proactiveReply, directed.routingDirected = true, true
	runtime.remember(directed)
	if outcome, err := runtime.replyAndRecord(context.Background(), directed, "那你明天再提醒我一次行吗", "replied_proactive"); err != nil || outcome != "replied_proactive" {
		t.Fatalf("router-directed chat-in outcome=%q err=%v", outcome, err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 {
		t.Fatalf("only the router-directed turn should reply: %#v", sent)
	}
}

// 热闹的群里，同一个人的两条之间隔着别人的话：提示词不会点名前一条，那它就不能被
// 取代，否则前一条谁都不回。
func TestSenderBurstKeepsEarlierMessageSeparatedByOtherSpeaker(t *testing.T) {
	provider := &burstReplyProvider{}
	runtime, channel := burstTestRuntime(provider)
	first := directedGroupMessage("20101", "10001", "帮我查一下这个报错")
	other := textEvent("20102", "10002", "我也遇到过", 1_800_000_001)
	other.GroupID = first.GroupID
	second := directedGroupMessage("20103", "10001", "顺便问下明天开会吗")
	first.Time, second.Time = 1_800_000_000, 1_800_000_003
	runtime.noteSenderTurnArrival(first)
	runtime.remember(first)
	runtime.remember(other)
	runtime.noteSenderTurnArrival(second)
	runtime.remember(second)

	if outcome, err := runtime.replyAndRecord(context.Background(), second, "顺便问下明天开会吗", "replied"); err != nil || outcome != "replied" {
		t.Fatalf("second outcome=%q err=%v", outcome, err)
	}
	if outcome, err := runtime.replyAndRecord(context.Background(), first, "帮我查一下这个报错", "replied"); err != nil || outcome != "replied" {
		t.Fatalf("first must still be answered, outcome=%q err=%v", outcome, err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 2 {
		t.Fatalf("both messages should get a reply: %#v", sent)
	}
}

// gatedBurstProvider 在「承接前一条」的那次生成上卡住，由测试决定它成功还是失败。
type gatedBurstProvider struct {
	burstReplyProvider
	entered  chan struct{}
	release  chan error
	once     sync.Once
	gateMu   sync.Mutex
	released bool
	err      error
}

func (p *gatedBurstProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if strings.Contains(requestText(req), "你还没有回复") {
		p.once.Do(func() { close(p.entered) })
		p.gateMu.Lock()
		released, err := p.released, p.err
		p.gateMu.Unlock()
		if !released {
			select {
			case err = <-p.release:
				p.gateMu.Lock()
				p.released, p.err = true, err
				p.gateMu.Unlock()
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		// 放行之后的重试拿到同样的结果，不再卡住。
		if err != nil {
			return nil, err
		}
	}
	return p.burstReplyProvider.Generate(ctx, req)
}

// 前一条明确叫了机器人，后一条只是随口的主动接话：不能拿接话取代那条直呼。
func TestSenderBurstChatInNeverSupersedesDirectedMessage(t *testing.T) {
	provider := &burstReplyProvider{}
	runtime, channel := burstTestRuntime(provider)
	first := directedGroupMessage("20121", "10001", "这个配置怎么改")
	second := textEvent("20122", "10001", "哈哈算了", 1_800_000_003)
	second.GroupID = first.GroupID
	second.proactiveReply, second.chatInReply = true, true
	first.Time = 1_800_000_000
	arriveTogether(runtime, first, second)

	if outcome, err := runtime.replyAndRecord(context.Background(), second, "哈哈算了", "replied_proactive"); err != nil || outcome != "replied_proactive" {
		t.Fatalf("chat-in outcome=%q err=%v", outcome, err)
	}
	if outcome, err := runtime.replyAndRecord(context.Background(), first, "这个配置怎么改", "replied"); err != nil || outcome != "replied" {
		t.Fatalf("directed message must still be answered, outcome=%q err=%v", outcome, err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 2 {
		t.Fatalf("sends=%#v", sent)
	}
}

// 链接解析、插件指令的消息不参与连发取代：前一条是链接，后一条问「这个讲了啥」，
// 链接那一轮的卡片不能被吞掉；反过来链接那一轮也不取代前面的提问。
func TestSenderBurstLeavesResolverMessagesAlone(t *testing.T) {
	provider := &burstReplyProvider{}
	disabled := false
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", GroupTriggers: []string{"Diana"}, BotReplyLoopDetectionEnabled: &disabled},
		channel, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	link := textEvent("20131", "10001", "看这个 https://www.bilibili.com/video/BV1xx411c7mD", 1_800_000_000)
	if !runtime.shouldHandleResolver(link, link.RawMessage) {
		t.Skip("默认插件配置下链接解析未启用")
	}
	question := directedGroupMessage("20132", "10001", "这个讲了啥")
	question.GroupID, question.Time = link.GroupID, 1_800_000_003
	arriveTogether(runtime, link, question)

	if outcome, err := runtime.replyAndRecord(context.Background(), question, "这个讲了啥", "replied"); err != nil || outcome != "replied" {
		t.Fatalf("question outcome=%q err=%v", outcome, err)
	}
	if _, superseded := runtime.senderTurnSupersededBy(link); superseded {
		t.Fatal("a link-resolver message must never be superseded")
	}

	// 反过来：链接那一轮走到回复入口时不取代前面那句提问。
	asking := directedGroupMessage("20133", "10001", "你们平时看什么视频")
	asking.GroupID, asking.Time = link.GroupID, 1_800_000_010
	laterLink := textEvent("20134", "10001", "比如这个 https://www.bilibili.com/video/BV1xx411c7mD", 1_800_000_012)
	arriveTogether(runtime, asking, laterLink)
	runtime.enterSenderTurnReply(laterLink, false)
	runtime.claimCarryOver(context.Background(), laterLink, runtime.contextHistory(laterLink))
	if _, superseded := runtime.senderTurnSupersededBy(asking); superseded {
		t.Fatal("a link-resolver turn must not supersede an earlier question")
	}
}

// 「纯图 + 短话按规则直接算补充」只认冲着那张图的一句话：带链接、@ 了别人的都交给判断器。
func TestImageFollowUpRuleIgnoresLinksAndMentionsOfOthers(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"relation":"independent","confidence":0.99,"reason":"另起一题"}`}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	root := photoEvent("20141", "10001", 1_800_000_000)
	runtime.noteSenderTurnArrival(root)
	runtime.remember(root)
	_, finish := runtime.beginDirectReply(context.Background(), root)
	defer finish()

	withLink := textEvent("20142", "10001", "这个呢 https://example.com/a", 1_800_000_003)
	mentionOther := MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "20143", Time: 1_800_000_004,
		RawMessage: "[CQ:at,qq=10002] 这个呢",
		Segments: []MessageSegment{
			{Type: "at", Data: map[string]string{"qq": "10002"}},
			{Type: "text", Data: map[string]string{"text": " 这个呢"}},
		},
	}
	for _, follow := range []MessageEvent{withLink, mentionOther} {
		runtime.noteSenderTurnArrival(follow)
		if _, _, _, outcome := runtime.prepareMessageEvent(context.Background(), follow); outcome == "merged_into_reply" {
			t.Fatalf("%s was merged into the image reply by rule", follow.MessageID)
		}
	}
	if !topicJudgeCalled(provider) {
		t.Fatal("follow-ups with a link or @ of someone else should go to the topic judge")
	}

	plain := textEvent("20144", "10001", "这个呢", 1_800_000_005)
	runtime.noteSenderTurnArrival(plain)
	if _, _, _, outcome := runtime.prepareMessageEvent(context.Background(), plain); outcome != "merged_into_reply" {
		t.Fatalf("plain short follow-up should merge by rule, outcome=%q", outcome)
	}
}

func topicJudgeCalled(provider *capturingLLMProvider) bool {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls > 0
}

// 话题判断认定文字那条「不是同一件事」时，依赖图那条路不接走正在回的图。
func TestDependencyImageNotClaimedWhenJudgedIndependent(t *testing.T) {
	provider := &scriptedReplyProvider{description: "一只橘猫"}
	runtime, base := dependencyTestRuntime(t, provider, false, nil)
	photo := historyEventByID(t, runtime, base, "photo-1")
	runtime.noteSenderTurnArrival(photo)
	runtime.enterSenderTurnReply(photo, true)
	photoCtx, finish := runtime.beginDirectReply(withReplyTriggerGate(context.Background()), photo)
	defer finish()

	question := base
	question.MessageID = "q-2"
	question.RawMessage = "另外帮我查一下明天上海的天气怎么样，我下午要出门去见客户开会，顺便看看要不要带伞"
	question.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": question.RawMessage}}}
	runtime.noteSenderTurnArrival(question)
	// 预处理：拿正在回的图做话题判断，判断器给不出可用结论，不合并。
	prepared, text, handled, outcome := runtime.prepareMessageEvent(context.Background(), question)
	if !handled {
		t.Fatalf("directed question should be handled, outcome=%q reason=%q", outcome, prepared.routingReason)
	}
	if outcome, err := runtime.replyAndRecord(context.Background(), prepared, text, "replied"); err != nil || outcome != "replied" {
		t.Fatalf("question outcome=%q err=%v", outcome, err)
	}
	if _, superseded := runtime.senderTurnSupersededBy(photo); superseded {
		t.Fatal("an image judged unrelated must not be claimed through the dependency path")
	}
	if err := runtime.interruptedReplyError(photoCtx, photo); err != nil {
		t.Fatalf("image turn should still be allowed to send: %v", err)
	}
}

// 回放、断线回补的登记顺序不可信：按消息时间认前后。
func TestSenderBurstOrdersByMessageTime(t *testing.T) {
	provider := &burstReplyProvider{}
	runtime, channel := burstTestRuntime(provider)
	first := directedGroupMessage("20151", "10001", "明天几点到")
	second := directedGroupMessage("20152", "10001", "在哪集合")
	first.Time, second.Time = 1_800_000_000, 1_800_000_004
	// 后一条先登记（比如前一条是回补进来的），历史仍按时间排。
	runtime.noteSenderTurnArrival(second)
	runtime.noteSenderTurnArrival(first)
	runtime.remember(first)
	runtime.remember(second)

	if outcome, err := runtime.replyAndRecord(context.Background(), second, "在哪集合", "replied"); err != nil || outcome != "replied" {
		t.Fatalf("second outcome=%q err=%v", outcome, err)
	}
	if outcome, _ := runtime.replyAndRecord(context.Background(), first, "明天几点到", "replied"); outcome != "superseded_follow_up" {
		t.Fatalf("earlier-by-time message should be covered, outcome=%q", outcome)
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 {
		t.Fatalf("sends=%#v", sent)
	}
}
