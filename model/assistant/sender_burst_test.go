// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// burstReplyProvider 记下每一次回复生成的请求，统一回一句固定的话；发送前审核放行。
type burstReplyProvider struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
}

func (p *burstReplyProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	body := requestText(req)
	if strings.Contains(body, "send_confidence") {
		return &llm.GenerateResponse{Model: "test", Text: `{"send_confidence":0.99,"account_safe":true,"reason":"ok"}`}, nil
	}
	p.mu.Lock()
	p.requests = append(p.requests, cloneGenerateRequestForTest(req))
	p.mu.Unlock()
	return &llm.GenerateResponse{Model: "test", Text: "几条一起回答"}, nil
}

// sawPrompt 报告有没有哪次请求里同时出现了这些片段。
func (p *burstReplyProvider) sawPrompt(parts ...string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, req := range p.requests {
		body := requestText(req)
		matched := true
		for _, part := range parts {
			if !strings.Contains(body, part) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func burstTestRuntime(provider LLMProvider) (*Runtime, *recordingChannel) {
	disabled := false
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		BotAccount: "42", GroupTriggers: []string{"Diana"}, BotReplyLoopDetectionEnabled: &disabled,
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	return runtime, channel
}

// arriveTogether 模拟几条消息先后到达：都已入站登记、进了会话历史，但都还卡在路由里。
func arriveTogether(runtime *Runtime, events ...MessageEvent) {
	for _, event := range events {
		runtime.noteSenderTurnArrival(event)
		runtime.remember(event)
	}
}

// 线上的形状：第一条还在路由（接话评分、机器人接话判定要几秒），第二条已经要回复了。
// 以前第二条只和「已经在生成」的那一轮比，看不见第一条，两条各回一遍。
func TestSenderBurstOfTwoGetsOneReply(t *testing.T) {
	provider := &burstReplyProvider{}
	runtime, channel := burstTestRuntime(provider)
	first := directedGroupMessage("20001", "10001", "帮我看看这个报错")
	second := directedGroupMessage("20002", "10001", "就是登录那个")
	first.Time, second.Time = 1_800_000_000, 1_800_000_003
	arriveTogether(runtime, first, second)

	outcome, err := runtime.replyAndRecord(withOutboundTurn(context.Background(), "turn-2"), second, "就是登录那个", "replied")
	if err != nil || outcome != "replied" {
		t.Fatalf("second outcome=%q err=%v", outcome, err)
	}
	if by, ok := runtime.senderTurnSupersededBy(first); !ok || by != "20002" {
		t.Fatalf("first message should be superseded by the second, got %q %v", by, ok)
	}
	// 第一条这时才走完路由：它发现自己已被取代，不再生成也不再发送。
	outcome, err = runtime.replyAndRecord(context.Background(), first, "帮我看看这个报错", "replied")
	if err != nil || outcome != "superseded_follow_up" {
		t.Fatalf("first outcome=%q err=%v", outcome, err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || sent[0].Text != "几条一起回答" {
		t.Fatalf("burst of two should produce exactly one reply: %#v", sent)
	}
	if !provider.sawPrompt("帮我看看这个报错", "你还没有回复") {
		t.Fatal("the answering turn must be told to cover the earlier message")
	}
}

// 三句连发：最后作答的那一轮把前两句都接住，只回一次。
func TestSenderBurstOfThreeGetsOneReply(t *testing.T) {
	provider := &burstReplyProvider{}
	runtime, channel := burstTestRuntime(provider)
	first := directedGroupMessage("20011", "10001", "我想问下")
	second := directedGroupMessage("20012", "10001", "这个插件")
	third := directedGroupMessage("20013", "10001", "怎么装")
	first.Time, second.Time, third.Time = 1_800_000_000, 1_800_000_002, 1_800_000_004
	arriveTogether(runtime, first, second, third)

	if outcome, err := runtime.replyAndRecord(context.Background(), third, "怎么装", "replied"); err != nil || outcome != "replied" {
		t.Fatalf("third outcome=%q err=%v", outcome, err)
	}
	for _, earlier := range []MessageEvent{first, second} {
		outcome, err := runtime.replyAndRecord(context.Background(), earlier, readableEventText(earlier, ""), "replied")
		if err != nil || outcome != "superseded_follow_up" {
			t.Fatalf("%s outcome=%q err=%v", earlier.MessageID, outcome, err)
		}
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 {
		t.Fatalf("burst of three should produce exactly one reply: %#v", sent)
	}
	if !provider.sawPrompt("我想问下", "这个插件", "你都还没有回复") {
		t.Fatal("the answering turn must be told about both earlier messages")
	}
}

// 私聊以前整个跳过取代，连发两三条同样被各回一遍。
func TestPrivateSenderBurstGetsOneReply(t *testing.T) {
	provider := &burstReplyProvider{}
	runtime, channel := burstTestRuntime(provider)
	first := privateEvent("10001", "20021", "明天几点集合")
	second := privateEvent("10001", "20022", "还有要带什么")
	second.Time = first.Time + 2
	arriveTogether(runtime, first, second)

	if outcome, err := runtime.replyAndRecord(context.Background(), second, "还有要带什么", "replied"); err != nil || outcome != "replied" {
		t.Fatalf("second outcome=%q err=%v", outcome, err)
	}
	if outcome, _ := runtime.replyAndRecord(context.Background(), first, "明天几点集合", "replied"); outcome != "superseded_follow_up" {
		t.Fatalf("private first outcome=%q", outcome)
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 {
		t.Fatalf("private burst should produce exactly one reply: %#v", sent)
	}
	if !provider.sawPrompt("明天几点集合", "你还没有回复") {
		t.Fatal("private reply must be told to cover the earlier message too")
	}
}

// 不同的人永远各回各的。
func TestSenderBurstNeverCrossesSenders(t *testing.T) {
	provider := &burstReplyProvider{}
	runtime, channel := burstTestRuntime(provider)
	first := directedGroupMessage("20031", "10001", "今天吃什么")
	second := directedGroupMessage("20032", "10002", "明天下雨吗")
	first.Time, second.Time = 1_800_000_000, 1_800_000_002
	arriveTogether(runtime, first, second)

	if outcome, err := runtime.replyAndRecord(context.Background(), second, "明天下雨吗", "replied"); err != nil || outcome != "replied" {
		t.Fatalf("second outcome=%q err=%v", outcome, err)
	}
	if _, superseded := runtime.senderTurnSupersededBy(first); superseded {
		t.Fatal("a message from another sender must not be superseded")
	}
	if outcome, err := runtime.replyAndRecord(context.Background(), first, "今天吃什么", "replied"); err != nil || outcome != "replied" {
		t.Fatalf("first outcome=%q err=%v", outcome, err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 2 {
		t.Fatalf("different senders should each get a reply: %#v", sent)
	}
}

// 已经开始往外发的那一轮不会被后到的消息回头取消。
func TestSenderBurstDoesNotCancelTurnAlreadySending(t *testing.T) {
	runtime, _ := burstTestRuntime(&burstReplyProvider{})
	first := directedGroupMessage("20041", "10001", "讲个长故事")
	second := directedGroupMessage("20042", "10001", "要带结局")
	arriveTogether(runtime, first, second)
	runtime.enterSenderTurnReply(first, true)
	ctx, finish := runtime.beginDirectReply(withReplyTriggerGate(context.Background()), first)
	defer finish()
	if err := runtime.interruptedReplyError(ctx, first); err != nil {
		t.Fatalf("first part should pass the gate: %v", err)
	}
	if absorbed := runtime.supersedeEarlierSenderTurns(second); len(absorbed) != 0 {
		t.Fatalf("a turn that already started sending was superseded: %#v", absorbed)
	}
	if err := runtime.interruptedReplyError(ctx, first); err != nil {
		t.Fatalf("remaining parts must keep going: %v", err)
	}
}

// mergeRecordingInboundStore 在内存队列上补一个追发合并的落库，发送前的持久化检查照读。
type mergeRecordingInboundStore struct {
	*memoryInboundEventStore
}

func (s mergeRecordingInboundStore) RecordInboundEventReplyMerge(_ context.Context, event MessageEvent, rootTurnID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.superseded[event.MessageID] = rootTurnID
	return nil
}

// 先发图、再问「这是什么」：文字那一轮把图当候选依赖图一起答了，图自己那一轮
// 哪怕已经在生成，也让位；文字那一轮回出去之后 superseded_by 才落库。
func TestDependencyImageTurnIsSupersededByTextTurn(t *testing.T) {
	provider := &scriptedReplyProvider{description: "一只橘猫"}
	runtime, question := dependencyTestRuntime(t, provider, false, nil)
	store := mergeRecordingInboundStore{newMemoryInboundEventStore()}
	runtime.SetInboundEventStore(store)
	photo := historyEventByID(t, runtime, question, "photo-1")
	runtime.noteSenderTurnArrival(photo)
	runtime.enterSenderTurnReply(photo, true)
	runtime.noteSenderTurnArrival(question)

	if outcome, err := runtime.replyAndRecord(withOutboundTurn(context.Background(), "turn-q"), question, question.RawMessage, "replied"); err != nil || outcome != "replied" {
		t.Fatalf("question outcome=%q err=%v", outcome, err)
	}
	if turn, superseded, _ := store.InboundEventSuperseded(context.Background(), photo); !superseded || turn != "turn-q" {
		t.Fatalf("superseded_by was not persisted for the image message: %q %v", turn, superseded)
	}
	// 图那一轮这时才走到回复入口：取代已经落定，它收住。
	if outcome, err := runtime.replyAndRecord(context.Background(), photo, "", "replied"); err != nil || outcome != "superseded_follow_up" {
		t.Fatalf("image turn outcome=%q err=%v", outcome, err)
	}
	if sent := nonEmptySends(runtime); sent != 1 {
		t.Fatalf("image then text should produce exactly one reply, got %d", sent)
	}
}

func historyEventByID(t *testing.T, runtime *Runtime, event MessageEvent, messageID string) MessageEvent {
	t.Helper()
	for _, item := range runtime.contextHistory(event) {
		if item.MessageID == messageID {
			return item
		}
	}
	t.Fatalf("%s is not in history", messageID)
	return MessageEvent{}
}

func nonEmptySends(runtime *Runtime) int {
	sent := 0
	for _, msg := range runtime.channel.(*recordingChannel).sentSnapshot() {
		if strings.TrimSpace(msg.Text) != "" {
			sent++
		}
	}
	return sent
}

// 纯图的原请求不能是空串：有缓存的识图描述就给描述，没有就写明是一张图。
func TestDirectReplyTopicJudgeSeesImageOnlyRoot(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"relation":"independent","confidence":0.9,"reason":"另起一题"}`}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	root := photoEvent("20051", "10001", 1_800_000_000)
	follow := textEvent("20052", "10001", "另外问一下，周末那个活动报名截止到什么时候呀", 1_800_000_100)

	runtime.classifyDirectReplyTopic(context.Background(), root, nil, follow, follow.RawMessage)
	if body := requestText(provider.requestSnapshot()); !strings.Contains(body, "（一张图片）") {
		t.Fatalf("judge should see an image placeholder instead of an empty request:\n%s", body)
	}

	root.Segments[0].Data[recallImageDescriptionKey] = "一只橘猫趴在键盘上"
	runtime.classifyDirectReplyTopic(context.Background(), root, nil, follow, follow.RawMessage)
	if body := requestText(provider.requestSnapshot()); !strings.Contains(body, "一只橘猫趴在键盘上") {
		t.Fatalf("judge should see the cached image description:\n%s", body)
	}
}

// 图正在回、同一个人几秒后补一句「这个呢」：按规则直接算补充，不再问判断器。
func TestImageThenShortTextMergesWithoutJudge(t *testing.T) {
	provider := &capturingLLMProvider{reply: `{"relation":"independent","confidence":0.99}`}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	root := photoEvent("20061", "10001", 1_800_000_000)
	follow := textEvent("20062", "10001", "这个呢", 1_800_000_004)
	runtime.noteSenderTurnArrival(root)
	runtime.noteSenderTurnArrival(follow)
	ctx, finish := runtime.beginDirectReply(context.Background(), root)
	defer finish()

	if rootID, merged := runtime.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged || rootID != "20061" {
		t.Fatalf("short text after an image should merge, merged=%v root=%q", merged, rootID)
	}
	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 0 {
		t.Fatalf("the rule should decide without calling the judge, got %d calls", calls)
	}

	// 隔太久或者话太长就不走规则，交回判断器。
	late := textEvent("20063", "10001", "这个呢", 1_800_000_000+int64(senderImageFollowUpWindow/time.Second)+5)
	if senderImageThenShortText(root, time.Time{}, late, time.Time{}, late.RawMessage) {
		t.Fatal("a follow-up outside the window must not be merged by rule")
	}
	other := textEvent("20064", "10002", "这个呢", 1_800_000_003)
	if senderImageThenShortText(root, time.Time{}, other, time.Time{}, other.RawMessage) {
		t.Fatal("another sender's text must not be merged by rule")
	}
}

// 机器人刚建的 Issue 下一轮被订阅报回来：卡片照发，跟评跳过。
func TestRepositoryWatchFollowUpSkipsBotsOwnNewIssue(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	now := time.Now()
	marker := repositoryIssueOperationMarkerWithPayload("create", strings.Repeat("a", 64), strings.Repeat("b", 64))
	own := repositoryWatchChange{Repository: "SuInk/Diana", Issues: []repositoryWatchIssue{{
		Number: 821, Title: "连发回复重复", Body: "现象……\n\n" + marker, Status: "opened", CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}}}
	if !runtime.repositoryWatchChangeOnlyOwnRecentWrites("SuInk/Diana", own, now) {
		t.Fatal("an issue the bot just created should skip the follow-up")
	}
	// 同一个 Issue 如果是很久以前建的，这一轮的更新来自别人，照常跟评。
	stale := own
	stale.Issues = []repositoryWatchIssue{own.Issues[0]}
	stale.Issues[0].CreatedAt = now.Add(-2 * time.Hour)
	if runtime.repositoryWatchChangeOnlyOwnRecentWrites("SuInk/Diana", stale, now) {
		t.Fatal("an old bot issue updated by someone else should still get a follow-up")
	}
	// 刚评论过的（正文没有标记）靠内存登记认出来。
	commented := repositoryWatchChange{Repository: "SuInk/Diana", Issues: []repositoryWatchIssue{{Number: 700, Status: "updated", CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-time.Second)}}}
	if runtime.repositoryWatchChangeOnlyOwnRecentWrites("SuInk/Diana", commented, now) {
		t.Fatal("unrelated update should not be treated as the bot's own")
	}
	runtime.noteOwnRepositoryWriteResult(repositoryIssueResult{OK: true, Operation: "comment", Repository: "SuInk/Diana", RequestedNumber: 700})
	if !runtime.repositoryWatchChangeOnlyOwnRecentWrites("suink/diana", commented, now) {
		t.Fatal("an issue the bot just commented on should skip the follow-up")
	}
	// 机器人评论之后别人又评论了：最后一次动静不是机器人的，照常跟评。
	later := commented
	later.Issues = []repositoryWatchIssue{commented.Issues[0]}
	later.Issues[0].UpdatedAt = now.Add(3 * time.Minute)
	if runtime.repositoryWatchChangeOnlyOwnRecentWrites("SuInk/Diana", later, now.Add(4*time.Minute)) {
		t.Fatal("someone else's comment after the bot's write must still get a follow-up")
	}
	// 机器人刚建的 Issue 建完又被别人动过，同样照常跟评。
	touched := own
	touched.Issues = []repositoryWatchIssue{own.Issues[0]}
	touched.Issues[0].UpdatedAt = now
	if runtime.repositoryWatchChangeOnlyOwnRecentWrites("SuInk/Diana", touched, now) {
		t.Fatal("a bot-created issue touched by someone else should still get a follow-up")
	}
	// 混着别人的动态就照常跟评。
	mixed := own
	mixed.Issues = append(append([]repositoryWatchIssue(nil), own.Issues...), repositoryWatchIssue{Number: 822, Status: "opened", CreatedAt: now, UpdatedAt: now})
	if runtime.repositoryWatchChangeOnlyOwnRecentWrites("SuInk/Diana", mixed, now) {
		t.Fatal("a round with someone else's issue should still get a follow-up")
	}
	withCommit := own
	withCommit.Commits = []repositoryWatchCommit{{SHA: "abc", Title: "fix"}}
	if runtime.repositoryWatchChangeOnlyOwnRecentWrites("SuInk/Diana", withCommit, now) {
		t.Fatal("a round with commits should still get a follow-up")
	}
}

// 提醒或事件触发任务刚 @ 过这个人：对他的主动接话放掉，直接叫机器人的照常回。
func TestProactiveReplySkippedAfterRecentTriggeredDelivery(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	reminded := MessageEvent{Kind: EventKindGroup, GroupID: "123456", UserID: "10001"}
	runtime.noteTriggeredDelivery(reminded)

	chatIn := textEvent("20071", "10001", "好的我知道了", 1_800_000_000)
	chatIn.proactiveReply, chatIn.chatInReply = true, true
	gated := withReplyTriggerGate(context.Background())
	if err := runtime.interruptedReplyError(gated, chatIn); !errors.Is(err, errProactiveReplyCoveredByTrigger) {
		t.Fatalf("chat-in right after a reminder to the same user should be dropped, got %v", err)
	}
	direct := directedGroupMessage("20072", "10001", "那明天几点")
	if err := runtime.interruptedReplyError(gated, direct); err != nil {
		t.Fatalf("a direct @ must still be answered: %v", err)
	}
	otherUser := textEvent("20073", "10002", "我也要提醒", 1_800_000_001)
	otherUser.proactiveReply = true
	if err := runtime.interruptedReplyError(gated, otherUser); err != nil {
		t.Fatalf("chat-in to another user is unaffected: %v", err)
	}
	otherGroup := chatIn
	otherGroup.GroupID = "654321"
	if err := runtime.interruptedReplyError(gated, otherGroup); err != nil {
		t.Fatalf("chat-in in another group is unaffected: %v", err)
	}
}
