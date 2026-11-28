// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func debounceTestEvent(id, user, text string, at time.Time) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: user, MessageID: id, Time: at.Unix(),
		RawMessage: text,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

// 合并进来的消息原地标 burst="k"、逐条判断；机器人已经回过的标 bot_replied。
func TestProactiveReplyTranscriptNumbersBurst(t *testing.T) {
	age := func(v int64) *int64 { return &v }
	payload := proactiveReplyPayload{
		Addressing:    messageAddressing{ReplyTarget: "none"},
		CurrentText:   "重启试试",
		CurrentSender: "Carol",
		BotAliases:    []string{"嘉然"},
		RecentMessages: []proactiveReplyHistoryItem{
			{Sender: "嘉然", IsBot: true, Text: "好，你说", AgeSeconds: age(6), Addressing: messageAddressing{ReplyTarget: "other", ReplyMessageID: "m1"}},
			{MessageID: "m2", UserID: "u2", Sender: "Bob", Text: "那你帮我看看", AgeSeconds: age(8), Addressing: messageAddressing{ReplyTarget: "none"}},
			{MessageID: "m1", UserID: "u1", Sender: "Alice", Text: "我给你举个例子", AgeSeconds: age(12), Addressing: messageAddressing{ReplyTarget: "none"}},
			{Sender: "嘉然", IsBot: true, Text: "你说说看", AgeSeconds: age(20), Addressing: messageAddressing{ReplyTarget: "none", Mentions: []MessageMention{{UserID: "u1"}}}},
		},
		Candidates: []proactiveReplyCandidatePayload{{MessageID: "m1", UserID: "u1"}, {MessageID: "m2", UserID: "u2"}, {MessageID: "m3", IsCurrent: true}},
	}
	got := proactiveReplyTranscript(payload)
	for _, want := range []string{
		`<msg age="12秒前" from="Alice" burst="1" bot_replied="true">我给你举个例子</msg>`,
		`<msg age="8秒前" from="Bob" burst="2">那你帮我看看</msg>` + "\n",
		`<msg age="刚刚" from="Carol" current="true">重启试试</msg>`,
		`burst_k: {"reason":`,
		`是不是在跟本人（嘉然）说话`,
		`若只标了 at/reply_to 而没标 at_self/reply_self`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("对话稿缺 %q：\n%s", want, got)
		}
	}
	if strings.Index(got, `burst_k: {"reason":`) > strings.Index(got, `"directed": true 或 false}`) {
		t.Fatalf("burst_k 格式示例应把 reason 放在 directed 之前，避免非思考模型先拍板再圆场：\n%s", got)
	}
	payload.Candidates = nil
	if strings.Contains(proactiveReplyTranscript(payload), "连发") {
		t.Fatal("没有合并时对话稿不该变")
	}
}

// 合并评分每条连发都要有判断，缺一条按格式无效处理。
func TestParticipationRatingsBurstVerdicts(t *testing.T) {
	raw := `{"relevance":{"directed":false,"reason":"群友之间"},"chat_in":{"score":0.1,"reason":"不接"},"burst_1":{"directed":true,"reason":"在接嘉然的话"}}`
	ratings, err := parseParticipationRatings(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := ratings.requireBurst(1); err != nil {
		t.Fatalf("burst_1 应该齐了：%v", err)
	}
	if v := ratings.Burst[1]; v.Directed == nil || !*v.Directed {
		t.Fatalf("burst_1=%#v", v)
	}
	if err := ratings.requireBurst(2); err == nil {
		t.Fatal("缺 burst_2 应该报错")
	}
}

// 不同发送者在同一批都被判为在叫机器人时拆成各自独立的回复目标（burstPeers），避免单轮手写拆条回错人；
// 同一发送者的多条连发仍合并成同一轮（burstTargets）；判否的不跟进来。
func TestSplitBurstRepliesSendsTopicsSeparately(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	now := time.Now()
	burst := []proactiveReplyCandidate{
		{Event: debounceTestEvent("m1", "u1", "攻击面管理", now.Add(-2*time.Second)), Text: "攻击面管理"},
		{Event: debounceTestEvent("m2", "u2", "晚上吃啥", now.Add(-time.Second)), Text: "晚上吃啥"},
	}
	current := debounceTestEvent("m3", "u3", "嘉然你觉得呢", now)
	yes, no := true, false
	ratings := participationRatings{Burst: map[int]participationRelevance{1: {Directed: &yes, Reason: "接嘉然的话"}, 2: {Directed: &no, Reason: "问别人"}}}

	primary, _, _, allowed := runtime.splitBurstReplies(current, "嘉然你觉得呢", burst, ratings, true)
	if !allowed || primary.MessageID != "m3" || !primary.burstSplit {
		t.Fatalf("primary=%s allowed=%v split=%v", primary.MessageID, allowed, primary.burstSplit)
	}
	if len(primary.burstTargets) != 0 {
		t.Fatalf("不同发送者的 m1 不应并进 m3 的同轮 burstTargets：%#v", primary.burstTargets)
	}
	if len(primary.burstPeers) != 1 || primary.burstPeers[0].Event.MessageID != "m1" || !primary.burstPeers[0].Event.routingDirected {
		t.Fatalf("不同发送者判为在叫的 m1 应拆为独立回复目标 burstPeers：%#v", primary.burstPeers)
	}

	// 同一发送者连发两条都判为在叫机器人（中间夹了别人）：合并成同发送者的一轮，最新那条作主对象。
	sameSenderBurst := []proactiveReplyCandidate{
		{Event: debounceTestEvent("s1", "u1", "我给你举个例子", now.Add(-2*time.Second)), Text: "我给你举个例子"},
		{Event: debounceTestEvent("s2", "u2", "哈哈", now.Add(-time.Second)), Text: "哈哈"},
	}
	sameSenderCurrent := debounceTestEvent("s3", "u1", "攻击面管理", now)
	primary, _, _, allowed = runtime.splitBurstReplies(sameSenderCurrent, "攻击面管理", sameSenderBurst, ratings, true)
	if !allowed || primary.MessageID != "s3" || len(primary.burstPeers) != 0 {
		t.Fatalf("同发送者合并 primary=%s allowed=%v peers=%#v", primary.MessageID, allowed, primary.burstPeers)
	}
	if len(primary.burstTargets) != 1 || primary.burstTargets[0].Event.MessageID != "s1" {
		t.Fatalf("同发送者前一条应放进 burstTargets：%#v", primary.burstTargets)
	}
	request := backlogReplyRequestText("攻击面管理", primary, primary.burstTargets)
	for _, want := range []string{"同一发言者几秒内连发的前置消息", "我给你举个例子"} {
		if !strings.Contains(request, want) {
			t.Fatalf("回复请求缺 %q：%s", want, request)
		}
	}
	if strings.Contains(request, "哈哈") {
		t.Fatal("判否的连发不该跟进这一轮")
	}

	// 当前消息不放行时，判为在叫的连发自己当回复对象。
	primary, _, _, allowed = runtime.splitBurstReplies(current, "嘉然你觉得呢", burst, ratings, false)
	if !allowed || primary.MessageID != "m1" || !primary.routingDirected || len(primary.burstTargets) != 0 || len(primary.burstPeers) != 0 {
		t.Fatalf("primary=%s allowed=%v directed=%v peers=%#v", primary.MessageID, allowed, primary.routingDirected, primary.burstPeers)
	}

	_, _, _, allowed = runtime.splitBurstReplies(current, "嘉然你觉得呢", burst[1:], participationRatings{Burst: map[int]participationRelevance{1: {Directed: &no, Reason: "问别人"}}}, false)
	if allowed {
		t.Fatal("都判否时不该回")
	}
}

const legacyQuietRoute = `{"should_reply":false,"confidence":0.2,"category":"chat","directed_at_bot":false,"answerable":true}`

// parkTestRuntime 起一个「运行中」的 runtime：等待批只在运行时启用。
func parkTestRuntime(t *testing.T, cfg BotConfig, replies ...string) (*Runtime, *sequenceLLMProvider, *memoryInboundEventStore) {
	t.Helper()
	provider := &sequenceLLMProvider{replies: replies}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		runtime.clearProactiveRouteParks()
	})
	runtime.mu.Lock()
	runtime.running = true
	runtime.runCtx = ctx
	runtime.mu.Unlock()
	store := newMemoryInboundEventStore()
	runtime.SetInboundEventStore(store)
	return runtime, provider, store
}

func enqueueParkTestEvent(t *testing.T, store *memoryInboundEventStore, event MessageEvent) MessageEvent {
	t.Helper()
	id, _, err := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event)
	if err != nil {
		t.Fatal(err)
	}
	event.backlogProbe = &InboundQueueItem{ID: id, Session: sessionKey(event), Event: event, Attempts: 1, EnqueuedAt: time.Now()}
	return event
}

func waitRouterCalls(provider *sequenceLLMProvider, want int, within time.Duration) int {
	deadline := time.Now().Add(within)
	for {
		got := len(provider.requestsSnapshot())
		if got >= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func routerInput(provider *sequenceLLMProvider, index int) string {
	request := provider.requestsSnapshot()[index]
	return request.Messages[len(request.Messages)-1].Content
}

// 三个人几秒内连发：每条都登记进等待批、worker 立刻收工，安静下来后只评一次，三条都在评分输入里。
func TestProactiveRouteParkMergesBurstIntoOneRoute(t *testing.T) {
	runtime, provider, store := parkTestRuntime(t, BotConfig{BotAccount: "42", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8}, legacyQuietRoute)
	runtime.proactiveRouteDebounce = 300 * time.Millisecond
	now := time.Now()
	texts := []string{"这个报错应该怎么处理？", "我也遇到了同样的报错", "重启试试"}
	for i, text := range texts {
		event := enqueueParkTestEvent(t, store, debounceTestEvent(fmt.Sprintf("message-%d", i+1), fmt.Sprintf("user-%d", i+1), text, now.Add(time.Duration(i-2)*time.Second)))
		started := time.Now()
		if _, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), event); handled || outcome != "merged_into_backlog_turn" {
			t.Fatalf("%s handled=%v outcome=%q", event.MessageID, handled, outcome)
		}
		if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
			t.Fatalf("登记等待批不该占着 worker，实际 %s", elapsed)
		}
	}
	if got := len(provider.requestsSnapshot()); got != 0 {
		t.Fatalf("还在等待窗口里就评分了：%d", got)
	}
	if got := waitRouterCalls(provider, 1, 3*time.Second); got != 1 {
		t.Fatalf("router calls=%d, want 1", got)
	}
	input := routerInput(provider, 0)
	for _, want := range texts {
		if !strings.Contains(input, want) {
			t.Fatalf("评分输入缺 %q：%q", want, input)
		}
	}
	time.Sleep(400 * time.Millisecond)
	if got := len(provider.requestsSnapshot()); got != 1 {
		t.Fatalf("同一批只该评一次，实际 %d", got)
	}
}

// 合并窗口比防抖长：防抖早就过了也继续等，窗口里来的消息并进同一批。
func TestProactiveRouteParkMergeWindowWaitsPastDebounce(t *testing.T) {
	cfg := BotConfig{BotAccount: "42", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8,
		Participation: &ParticipationPreferences{Desire: 25, CooldownSeconds: 30, ChatLevel: "low", RelevanceLevel: "on", MergeWindowSeconds: 2}}
	runtime, provider, store := parkTestRuntime(t, cfg)
	runtime.proactiveRouteDebounce = 50 * time.Millisecond
	now := time.Now()
	first := enqueueParkTestEvent(t, store, debounceTestEvent("message-1", "user-1", "今天吃什么", now))
	runtime.prepareMessageEvent(context.Background(), first)
	time.Sleep(300 * time.Millisecond)
	second := enqueueParkTestEvent(t, store, debounceTestEvent("message-2", "user-2", "火锅吧", now))
	runtime.prepareMessageEvent(context.Background(), second)
	time.Sleep(300 * time.Millisecond)
	if got := len(provider.requestsSnapshot()); got != 0 {
		t.Fatalf("合并窗口没到就评分了：%d", got)
	}
	if got := waitRouterCalls(provider, 1, 4*time.Second); got < 1 {
		t.Fatal("窗口结束后应该评分")
	}
	input := routerInput(provider, 0)
	if !strings.Contains(input, "今天吃什么") || !strings.Contains(input, "火锅吧") {
		t.Fatalf("两条应该在同一次评分里：%q", input)
	}
}

// 攒满条数上限就在当前 worker 里当场评，等着的一起带上。
func TestProactiveRouteParkMaxMessagesRoutesImmediately(t *testing.T) {
	cfg := BotConfig{BotAccount: "42", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8,
		Participation: &ParticipationPreferences{Desire: 25, CooldownSeconds: 30, ChatLevel: "low", RelevanceLevel: "on", MergeWindowSeconds: 60, MergeMaxMessages: 3}}
	runtime, provider, store := parkTestRuntime(t, cfg)
	runtime.proactiveRouteDebounce = time.Second
	now := time.Now()
	texts := []string{"第一条", "第二条", "第三条"}
	var outcome string
	for i, text := range texts {
		event := enqueueParkTestEvent(t, store, debounceTestEvent(fmt.Sprintf("message-%d", i+1), fmt.Sprintf("user-%d", i+1), text, now))
		_, _, _, outcome = runtime.prepareMessageEvent(context.Background(), event)
	}
	if outcome == "merged_into_backlog_turn" {
		t.Fatal("第三条攒满了，应该当场评分")
	}
	if got := len(provider.requestsSnapshot()); got < 1 {
		t.Fatal("攒满后应该已经评过分")
	}
	input := routerInput(provider, 0)
	for _, want := range texts {
		if !strings.Contains(input, want) {
			t.Fatalf("评分输入缺 %q：%q", want, input)
		}
	}
	runtime.routeParkMu.Lock()
	left := len(runtime.routeParks)
	runtime.routeParkMu.Unlock()
	if left != 0 {
		t.Fatalf("等待批应该清空，还剩 %d", left)
	}
}

// @ 机器人的消息不等，进来时只接走同一发送者的等待候选；其他群友的等待候选留在原批里照常评分。
func TestProactiveRouteParkTakenByDirectMessage(t *testing.T) {
	runtime, provider, store := parkTestRuntime(t, BotConfig{BotAccount: "42", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8}, legacyQuietRoute)
	runtime.proactiveRouteDebounce = 200 * time.Millisecond
	now := time.Now()
	sameUser := enqueueParkTestEvent(t, store, debounceTestEvent("message-1", "user-1", "有人知道怎么配代理吗", now))
	otherUser := enqueueParkTestEvent(t, store, debounceTestEvent("message-other", "user-2", "另一位群友的提问", now))
	if _, _, _, outcome := runtime.prepareMessageEvent(context.Background(), sameUser); outcome != "merged_into_backlog_turn" {
		t.Fatalf("sameUser outcome=%q", outcome)
	}
	if _, _, _, outcome := runtime.prepareMessageEvent(context.Background(), otherUser); outcome != "merged_into_backlog_turn" {
		t.Fatalf("otherUser outcome=%q", outcome)
	}
	direct := debounceTestEvent("message-2", "user-1", "看看这个", now)
	direct.Segments = append([]MessageSegment{{Type: "at", Data: map[string]string{"qq": "42"}}}, direct.Segments...)
	direct = enqueueParkTestEvent(t, store, direct)
	started := time.Now()
	event, _, handled, _ := runtime.prepareMessageEvent(context.Background(), direct)
	if !handled {
		t.Fatal("@ 机器人应该直接进回复")
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("@ 消息不该等，实际 %s", elapsed)
	}
	if len(event.backlogTurn) != 1 || event.backlogTurn[0].Event.MessageID != "message-1" {
		t.Fatalf("同一发送者等着的候选应该并进这一轮，不能把其他人的也抢走：%+v", event.backlogTurn)
	}
	if record := latestEventRecord(runtime, "message-1"); !strings.Contains(record.Reason, "message-2") || strings.Contains(record.Reason, "排队等接话判断") {
		t.Fatalf("接走的候选应该指向 @ 那条，不能还挂着占位：%+v", record)
	}
	// 其他发送者的 message-other 留在等待批里，防抖到期后应单独走一次接话评分，不漏判。
	if got := waitRouterCalls(provider, 1, 2*time.Second); got != 1 {
		t.Fatalf("其他群友的等待消息应照常评分一次，实际 router calls=%d", got)
	}
	input := routerInput(provider, 0)
	if !strings.Contains(input, "另一位群友的提问") {
		t.Fatalf("评分输入应包含留下的其他群友消息：%q", input)
	}
	if strings.Contains(input, "有人知道怎么配代理吗") && strings.Contains(input, `burst="1"`) {
		t.Fatalf("已经被同一发送者 @ 那轮接走的消息不该再次进入评分候选：%q", input)
	}
}

func latestEventRecord(runtime *Runtime, messageID string) EventRecord {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	for _, record := range runtime.recent {
		if record.MessageID == messageID {
			return record
		}
	}
	return EventRecord{}
}

// 整批评完决定接话：跟着那一轮一起回的指向回复对象，没被选上的记成不回，都不再挂着占位。
func TestRecordProactiveBatchRoutedSettlesPlaceholders(t *testing.T) {
	runtime, _, _ := parkTestRuntime(t, BotConfig{BotAccount: "42"})
	now := time.Now()
	batch := []proactiveReplyCandidate{
		{Event: debounceTestEvent("message-1", "user-1", "早上好", now), Text: "早上好"},
		{Event: debounceTestEvent("message-2", "user-2", "这个怎么配", now), Text: "这个怎么配"},
		{Event: debounceTestEvent("message-3", "user-3", "我也想知道", now), Text: "我也想知道"},
	}
	routed := batch[1].Event
	routed.backlogTurn = batch[2:]
	runtime.recordProactiveBatchRouted(batch, routed)

	if record := latestEventRecord(runtime, "message-2"); record.MessageID != "" {
		t.Fatalf("回复对象自己的结果由回复那一轮记：%+v", record)
	}
	joined := latestEventRecord(runtime, "message-3")
	if joined.Outcome != "merged_into_backlog_turn" || !strings.Contains(joined.Reason, "message-2") {
		t.Fatalf("一起回的那条应该指向回复对象：%+v", joined)
	}
	skipped := latestEventRecord(runtime, "message-1")
	if skipped.Outcome != "ignored" || !strings.Contains(skipped.Reason, "没被选上") {
		t.Fatalf("没被选上的应该记成不回：%+v", skipped)
	}
}

func TestProactiveRouteWaitUsesOldestCandidate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	debounce := 3 * time.Second
	cases := []struct {
		name   string
		window time.Duration
		oldest time.Time
		want   time.Duration
	}{
		{"不设窗口按防抖", 0, now, debounce},
		{"窗口短于防抖按防抖", 2 * time.Second, now, debounce},
		{"窗口从最早那条算起", 15 * time.Second, now.Add(-5 * time.Second), 10 * time.Second},
		{"窗口用完仍等一个防抖", 15 * time.Second, now.Add(-20 * time.Second), debounce},
	}
	for _, tc := range cases {
		if got := proactiveRouteWait(debounce, tc.window, tc.oldest, now); got != tc.want {
			t.Fatalf("%s: wait=%s, want %s", tc.name, got, tc.want)
		}
	}
	merged := mergeProactiveRouteCandidates(
		[]proactiveReplyCandidate{{Event: debounceTestEvent("m2", "u2", "b", now.Add(-3*time.Second))}},
		[]proactiveReplyCandidate{{Event: debounceTestEvent("m1", "u1", "a", now.Add(-8*time.Second))}, {Event: debounceTestEvent("m2", "u2", "b", now.Add(-3*time.Second))}},
	)
	if len(merged) != 2 || merged[0].Event.MessageID != "m1" {
		t.Fatalf("merged=%+v", merged)
	}
	if got := proactiveRouteCandidateTime(merged[0], now); !got.Equal(now.Add(-8 * time.Second)) {
		t.Fatalf("oldest=%s", got)
	}
}

func TestParticipationMergeSettingsClamp(t *testing.T) {
	p := copyParticipation(&ParticipationPreferences{MergeWindowSeconds: 999, MergeMaxMessages: 99})
	if p.MergeWindowSeconds != maxProactiveRouteMergeWindowSeconds || p.MergeMaxMessages != backlogMessageMaxItems {
		t.Fatalf("clamp=%+v", p)
	}
	window, maxMessages := proactiveRouteMergeLimits(BotConfig{Participation: &ParticipationPreferences{MergeWindowSeconds: 15, MergeMaxMessages: 8}})
	if window != 15*time.Second || maxMessages != 8 {
		t.Fatalf("limits=%s/%d", window, maxMessages)
	}
}

// 定时器已经取走、还在评分的那一批：@ 进来时一并接走，评分那一轮在发送前放弃，
// 不再把 @ 那一轮已经答过的话原样再发一遍（2026-11-09 生产事故）。
func TestProactiveRouteFlightTakenByDirectMessage(t *testing.T) {
	runtime, _, store := parkTestRuntime(t, BotConfig{BotAccount: "42", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8})
	now := time.Now()
	forward := debounceTestEvent("message-1", "user-1", "你看这段转发", now)
	key := proactiveRouteParkKey(forward)
	runtime.routeParkMu.Lock()
	flight := runtime.beginProactiveRouteFlightLocked(key, []proactiveReplyCandidate{{Event: forward, Text: "你看这段转发"}})
	runtime.routeParkMu.Unlock()
	defer runtime.endProactiveRouteFlight(key, flight)
	flightCtx := context.WithValue(context.Background(), proactiveRouteFlightContextKey{}, flight)

	direct := debounceTestEvent("message-2", "user-1", "你干的", now.Add(10*time.Second))
	direct.Segments = append([]MessageSegment{{Type: "at", Data: map[string]string{"qq": "42"}}}, direct.Segments...)
	direct = enqueueParkTestEvent(t, store, direct)
	event, _, handled, _ := runtime.prepareMessageEvent(context.Background(), direct)
	if !handled {
		t.Fatal("@ 机器人应该直接进回复")
	}
	if len(event.backlogTurn) != 1 || event.backlogTurn[0].Event.MessageID != "message-1" {
		t.Fatalf("评分中的那条应该并进 @ 这一轮：%+v", event.backlogTurn)
	}
	if !runtime.proactiveRouteFlightTaken(flight) {
		t.Fatal("被接走后，评分那一轮应该在生成前放弃")
	}
	if !runtime.proactiveRouteTakenBeforeSend(flightCtx) {
		t.Fatal("被接走后，评分那一轮的回复不该再发")
	}
	if outcome, _, _ := DescribeEventOutcome(proactiveRouteTakenOutcome); outcome != "not_replied" {
		t.Fatalf("decision=%q", outcome)
	}
}

// 评分那一轮已经开始往外发了，就不再被接走：@ 那一轮不带它，它后面的几条照常发完。
func TestProactiveRouteFlightCommittedIsNotTaken(t *testing.T) {
	runtime, _, store := parkTestRuntime(t, BotConfig{BotAccount: "42", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8})
	now := time.Now()
	forward := debounceTestEvent("message-1", "user-1", "你看这段转发", now)
	key := proactiveRouteParkKey(forward)
	runtime.routeParkMu.Lock()
	flight := runtime.beginProactiveRouteFlightLocked(key, []proactiveReplyCandidate{{Event: forward, Text: "你看这段转发"}})
	runtime.routeParkMu.Unlock()
	flightCtx := context.WithValue(context.Background(), proactiveRouteFlightContextKey{}, flight)
	if runtime.proactiveRouteTakenBeforeSend(flightCtx) {
		t.Fatal("没人接走时第一条应该照发")
	}

	direct := debounceTestEvent("message-2", "user-1", "你干的", now.Add(10*time.Second))
	direct.Segments = append([]MessageSegment{{Type: "at", Data: map[string]string{"qq": "42"}}}, direct.Segments...)
	direct = enqueueParkTestEvent(t, store, direct)
	event, _, _, _ := runtime.prepareMessageEvent(context.Background(), direct)
	if len(event.backlogTurn) != 0 {
		t.Fatalf("已经在发的那一批不该被接走：%+v", event.backlogTurn)
	}
	if runtime.proactiveRouteTakenBeforeSend(flightCtx) {
		t.Fatal("已经开始发的回复，后面几条也要发完")
	}
	runtime.endProactiveRouteFlight(key, flight)
	runtime.routeParkMu.Lock()
	left := len(runtime.routeFlights)
	runtime.routeParkMu.Unlock()
	if left != 0 {
		t.Fatalf("结束后不该残留评分中的批次：%d", left)
	}
}

// 合并评分把回复对象切到批里更早的 burst 消息时，防抖协程必须把调试轨迹、SSE 进度推送、
// LLM 用量归属和出站幂等账本都切到真正要回复的那条消息上，并在进入生成前把事件状态更新为 pending。
func TestFlushProactiveRouteParkRebindsTraceProgressOutboundAndPendingOnBurstTarget(t *testing.T) {
	cfg := BotConfig{
		BotAccount:              "42",
		DebugModeEnabled:        boolPointer(true),
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
		Participation: &ParticipationPreferences{
			Desire:             25,
			CooldownSeconds:    30,
			ChatLevel:          "minimal",
			RelevanceLevel:     "on",
			MergeWindowSeconds: 1,
		},
	}
	routerReply := `{"relevance":{"directed":false,"reason":"批尾消息不是在问机器人"},"chat_in":{"score":0.05,"reason":"不插嘴"},"burst_1":{"reason":"在向机器人追问","directed":true}}`
	agentReply := "好的，我来回答 m1"
	runtime, provider, store := parkTestRuntime(t, cfg, routerReply, agentReply)
	runtime.proactiveRouteDebounce = 40 * time.Millisecond
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)

	var progressMu = make(chan EventProgress, 64)
	runtime.SetEventProgressListener(func(p EventProgress) {
		select {
		case progressMu <- p:
		default:
		}
	})
	var recordsMu = make(chan EventRecord, 64)
	runtime.SetEventListener(func(rec EventRecord) {
		select {
		case recordsMu <- rec:
		default:
		}
	})

	now := time.Now()
	m1 := enqueueParkTestEvent(t, store, debounceTestEvent("m1", "user-1", "嘉然怎么回事", now.Add(-2*time.Second)))
	m2 := enqueueParkTestEvent(t, store, debounceTestEvent("m2", "user-2", "旁边路过一句", now))
	if _, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), m1); handled || outcome != "merged_into_backlog_turn" {
		t.Fatalf("m1 handled=%v outcome=%q", handled, outcome)
	}
	if _, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), m2); handled || outcome != "merged_into_backlog_turn" {
		t.Fatalf("m2 handled=%v outcome=%q", handled, outcome)
	}

	// 等待评分 + 回复两次模型调用完成，并等到 m1 最终结果落库。
	if got := waitRouterCalls(provider, 2, 5*time.Second); got < 2 {
		t.Fatalf("llm calls = %d, want >= 2 (router + reply)", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	var m1Records []EventRecord
	for time.Now().Before(deadline) {
		for {
			select {
			case rec := <-recordsMu:
				if rec.MessageID == "m1" {
					m1Records = append(m1Records, rec)
				}
			default:
				goto drained
			}
		}
	drained:
		if len(m1Records) > 0 && m1Records[len(m1Records)-1].Outcome == "replied_proactive" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if len(m1Records) == 0 || m1Records[len(m1Records)-1].Outcome != "replied_proactive" {
		t.Fatalf("m1 records = %#v, want final replied_proactive", m1Records)
	}
	sawPending := false
	for _, rec := range m1Records {
		if rec.Outcome == "merged_into_backlog_turn" && rec.Decision == "pending" && strings.Contains(rec.Reason, "正在生成回复") {
			sawPending = true
			break
		}
	}
	if !sawPending {
		t.Fatalf("m1 should emit pending progress record before final reply, got: %#v", m1Records)
	}

	// 检查调试轨迹：批尾 m2 只记录自己的收件与路由器调用；真正回复的 m1 必须拿到切换节点和 reply 模型轨迹，不能漏到 m2 名下。
	var m1Phases, m2Phases []string
	m1SawReplyModel := false
	m2SawReplyModel := false
	for _, entry := range logs.entriesSnapshot() {
		if entry.Action != "debug_trace" {
			continue
		}
		phase, _ := entry.Metadata["phase"].(string)
		purpose, _ := entry.Metadata["purpose"].(string)
		switch entry.Target {
		case "m1":
			m1Phases = append(m1Phases, phase+":"+purpose)
			if phase == "model_request" && purpose != "proactive_reply_router" {
				m1SawReplyModel = true
			}
		case "m2":
			m2Phases = append(m2Phases, phase+":"+purpose)
			if phase == "model_request" && purpose != "proactive_reply_router" {
				m2SawReplyModel = true
			}
		}
	}
	if !m1SawReplyModel {
		t.Fatalf("m1 trace missing reply model call; m1=%v m2=%v", m1Phases, m2Phases)
	}
	if m2SawReplyModel {
		t.Fatalf("m2 trace leaked reply model call belonging to m1; m1=%v m2=%v", m1Phases, m2Phases)
	}
	sawSelected := false
	for _, p := range m1Phases {
		if strings.HasPrefix(p, "proactive_route_selected:") {
			sawSelected = true
			break
		}
	}
	if !sawSelected {
		t.Fatalf("m1 trace missing proactive_route_selected marker: %v", m1Phases)
	}

	// 检查 SSE 进度推送：切目标后的进度必须推给 m1。
	close(progressMu)
	m1Progress := 0
	for p := range progressMu {
		if p.MessageID == "m1" {
			m1Progress++
		}
	}
	if m1Progress < 2 {
		t.Fatalf("m1 progress events = %d, want >= 2 (selected + reply steps)", m1Progress)
	}
}

// 攒满条数上限在当前 worker 当场评分、并把回复目标切到批里更早的消息时，返回的 context
// 也必须把调试轨迹、LLM 用量归属和出站账本切到新目标上。
func TestPrepareMessageEventWithContextRebindsOnImmediateBurstRoute(t *testing.T) {
	cfg := BotConfig{
		BotAccount:              "42",
		DebugModeEnabled:        boolPointer(true),
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
		Participation: &ParticipationPreferences{
			Desire:             25,
			CooldownSeconds:    30,
			ChatLevel:          "minimal",
			RelevanceLevel:     "on",
			MergeWindowSeconds: 60,
			MergeMaxMessages:   2,
		},
	}
	routerReply := `{"relevance":{"directed":false,"reason":"批尾没问机器人"},"chat_in":{"score":0.05,"reason":"不接"},"burst_1":{"reason":"明确在问机器人","directed":true}}`
	runtime, _, store := parkTestRuntime(t, cfg, routerReply)
	runtime.proactiveRouteDebounce = time.Second
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)

	now := time.Now()
	m1 := enqueueParkTestEvent(t, store, debounceTestEvent("m1", "user-1", "帮我看下这个", now.Add(-time.Second)))
	m2 := enqueueParkTestEvent(t, store, debounceTestEvent("m2", "user-2", "路过", now))

	if _, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), m1); handled || outcome != "merged_into_backlog_turn" {
		t.Fatalf("m1 handled=%v outcome=%q", handled, outcome)
	}
	ctx := withLLMUsageContext(context.Background(), m2)
	ctx = runtime.withDebugTraceContext(ctx, m2)
	ctx = withOutboundTurn(ctx, m2.backlogProbe.ID)

	reboundCtx, routed, _, handled, outcome := runtime.prepareMessageEventWithContext(ctx, m2)
	if !handled || outcome != "replied_proactive" || routed.MessageID != "m1" {
		t.Fatalf("routed=%s handled=%v outcome=%q", routed.MessageID, handled, outcome)
	}
	if state := debugTraceFromContext(reboundCtx); state == nil || state.event.MessageID != "m1" {
		t.Fatalf("debugTrace state = %#v, want bound to m1", state)
	}
	if turn := outboundTurnID(reboundCtx); turn != m1.backlogProbe.ID {
		t.Fatalf("outboundTurnID = %q, want m1 probe ID %q (not m2 %q)", turn, m1.backlogProbe.ID, m2.backlogProbe.ID)
	}
	if usage := llmUsageFromContext(reboundCtx); usage == nil || usage.event.MessageID != "m1" {
		t.Fatalf("llmUsageFromContext = %#v, want m1", usage)
	}
}

type proactivePurposeLLMProvider struct {
	mu          sync.Mutex
	routerReply string
	replyCalls  map[string]int
}

func (p *proactivePurposeLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	purpose := llmUsagePurposeFromContext(ctx)
	msgID := ""
	if usage := llmUsageFromContext(ctx); usage != nil {
		msgID = strings.TrimSpace(usage.event.MessageID)
	}
	for _, msg := range req.Messages {
		if strings.Contains(msg.Content, "发送前审核器") {
			return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"send_confidence":0.99,"reason":"审核放行"}`}, nil
		}
	}
	if purpose == "proactive_reply_router" {
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: p.routerReply}, nil
	}
	if purpose == PurposeReply {
		p.mu.Lock()
		if p.replyCalls == nil {
			p.replyCalls = map[string]int{}
		}
		p.replyCalls[msgID]++
		p.mu.Unlock()
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: "独立回复给消息 " + msgID}, nil
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: `{"send_confidence":0.99,"reason":"默认通过"}`}, nil
}

// 同一防抖批里多个不同发送者都被判为在跟机器人说话时，按发送者各自独立发起回复轮次，
// 各自拥有独立的回复正文、调试轨迹、SSE 进度推送与最终事件记录，不靠单轮手写拆条。
func TestFlushProactiveRouteParkRepliesCrossSenderTargetsIndependently(t *testing.T) {
	cfg := BotConfig{
		BotAccount:              "42",
		DebugModeEnabled:        boolPointer(true),
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
		Participation: &ParticipationPreferences{
			Desire:             25,
			CooldownSeconds:    30,
			ChatLevel:          "minimal",
			RelevanceLevel:     "on",
			MergeWindowSeconds: 1,
		},
	}
	provider := &proactivePurposeLLMProvider{
		routerReply: `{"relevance":{"directed":true,"reason":"user-2 在问机器人"},"chat_in":{"score":0.10,"reason":"不需要闲聊"},"burst_1":{"reason":"user-1 也在问机器人","directed":true}}`,
	}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		runtime.clearProactiveRouteParks()
	})
	runtime.mu.Lock()
	runtime.running = true
	runtime.runCtx = ctx
	runtime.mu.Unlock()
	store := newMemoryInboundEventStore()
	runtime.SetInboundEventStore(store)
	runtime.proactiveRouteDebounce = 40 * time.Millisecond
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)

	recordsCh := make(chan EventRecord, 64)
	runtime.SetEventListener(func(rec EventRecord) {
		select {
		case recordsCh <- rec:
		default:
		}
	})

	now := time.Now()
	m1 := enqueueParkTestEvent(t, store, debounceTestEvent("m1", "user-1", "嘉然帮我看下问题一", now.Add(-time.Second)))
	m2 := enqueueParkTestEvent(t, store, debounceTestEvent("m2", "user-2", "嘉然也帮我看下问题二", now))
	if _, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), m1); handled || outcome != "merged_into_backlog_turn" {
		t.Fatalf("m1 handled=%v outcome=%q", handled, outcome)
	}
	if _, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), m2); handled || outcome != "merged_into_backlog_turn" {
		t.Fatalf("m2 handled=%v outcome=%q", handled, outcome)
	}

	deadline := time.Now().Add(4 * time.Second)
	finalByID := map[string]EventRecord{}
	for time.Now().Before(deadline) {
		for {
			select {
			case rec := <-recordsCh:
				if rec.Outcome == "replied_proactive" {
					finalByID[rec.MessageID] = rec
				}
			default:
				goto checked
			}
		}
	checked:
		if finalByID["m1"].Outcome == "replied_proactive" && finalByID["m2"].Outcome == "replied_proactive" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if finalByID["m1"].Outcome != "replied_proactive" || finalByID["m2"].Outcome != "replied_proactive" {
		t.Fatalf("both m1 and m2 should finish with replied_proactive, got: %#v", finalByID)
	}
	if !strings.Contains(finalByID["m1"].Reply, "m1") || !strings.Contains(finalByID["m2"].Reply, "m2") {
		t.Fatalf("each target must receive its own independent reply: m1=%q m2=%q", finalByID["m1"].Reply, finalByID["m2"].Reply)
	}

	replyTraceCount := map[string]int{}
	sawM1Selected := false
	for _, entry := range logs.entriesSnapshot() {
		if entry.Action != "debug_trace" {
			continue
		}
		phase, _ := entry.Metadata["phase"].(string)
		purpose, _ := entry.Metadata["purpose"].(string)
		if entry.Target == "m1" && phase == "proactive_route_selected" {
			sawM1Selected = true
		}
		if phase == "model_request" && purpose == PurposeReply {
			replyTraceCount[entry.Target]++
		}
	}
	if !sawM1Selected {
		t.Fatal("m1 trace missing proactive_route_selected marker")
	}
	if replyTraceCount["m1"] < 1 || replyTraceCount["m2"] < 1 {
		t.Fatalf("each independent target must record its own reply model trace, got: %#v", replyTraceCount)
	}
}

// 攒满条数上限当场评分且命中多个不同发送者时，主目标返回给当前 worker，其余发送者各自起独立回复。
func TestPrepareMessageEventWithContextLaunchesCrossSenderPeerIndependently(t *testing.T) {
	cfg := BotConfig{
		BotAccount:              "42",
		DebugModeEnabled:        boolPointer(true),
		ProactiveReplyChance:    1,
		ProactiveReplyThreshold: 0.8,
		Participation: &ParticipationPreferences{
			Desire:             25,
			CooldownSeconds:    30,
			ChatLevel:          "minimal",
			RelevanceLevel:     "on",
			MergeWindowSeconds: 60,
			MergeMaxMessages:   2,
		},
	}
	routerReply := `{"relevance":{"directed":true,"reason":"user-2 在问机器人"},"chat_in":{"score":0.10,"reason":"不闲聊"},"burst_1":{"reason":"user-1 也在问机器人","directed":true}}`
	runtime, provider, store := parkTestRuntime(t, cfg, routerReply, "单独回给 user-1")
	runtime.proactiveRouteDebounce = time.Second
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)

	recordsCh := make(chan EventRecord, 32)
	runtime.SetEventListener(func(rec EventRecord) {
		select {
		case recordsCh <- rec:
		default:
		}
	})

	now := time.Now()
	m1 := enqueueParkTestEvent(t, store, debounceTestEvent("m1", "user-1", "嘉然帮我看下", now.Add(-time.Second)))
	m2 := enqueueParkTestEvent(t, store, debounceTestEvent("m2", "user-2", "嘉然也回我一下", now))
	if _, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), m1); handled || outcome != "merged_into_backlog_turn" {
		t.Fatalf("m1 handled=%v outcome=%q", handled, outcome)
	}
	ctx := runtime.withDebugTraceContext(withLLMUsageContext(context.Background(), m2), m2)
	_, routed, _, handled, outcome := runtime.prepareMessageEventWithContext(ctx, m2)
	if !handled || outcome != "replied_proactive" || routed.MessageID != "m2" {
		t.Fatalf("primary routed=%s handled=%v outcome=%q", routed.MessageID, handled, outcome)
	}
	if len(routed.backlogTurn) != 0 || len(routed.burstPeers) != 0 {
		t.Fatalf("m2 不应夹带 m1（m1 已独立发起回复）：backlogTurn=%#v burstPeers=%#v", routed.backlogTurn, routed.burstPeers)
	}
	if got := waitRouterCalls(provider, 2, 5*time.Second); got < 2 {
		t.Fatalf("expected peer m1 to run independent reply, llm calls=%d", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	m1Replied := false
	for time.Now().Before(deadline) && !m1Replied {
		select {
		case rec := <-recordsCh:
			if rec.MessageID == "m1" && rec.Outcome == "replied_proactive" {
				m1Replied = true
			}
		case <-time.After(15 * time.Millisecond):
		}
	}
	if !m1Replied {
		t.Fatal("independent peer m1 did not complete with replied_proactive")
	}
}

// 评分中的跨人批次：user-1 发直接触发消息只接走并取消 user-1 自己的目标，不影响同批里 user-2 的独立发送。
func TestProactiveRouteFlightCrossSenderDirectDoesNotCancelOtherUser(t *testing.T) {
	runtime, _, store := parkTestRuntime(t, BotConfig{BotAccount: "42", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8})
	now := time.Now()
	m1 := debounceTestEvent("m1", "user-1", "转发一条消息", now.Add(-time.Second))
	m2 := debounceTestEvent("m2", "user-2", "另一个人的追问", now)
	key := proactiveRouteParkKey(m1)
	runtime.routeParkMu.Lock()
	flight := runtime.beginProactiveRouteFlightLocked(key, []proactiveReplyCandidate{
		{Event: m1, Text: "转发一条消息"},
		{Event: m2, Text: "另一个人的追问"},
	})
	runtime.routeParkMu.Unlock()
	defer runtime.endProactiveRouteFlight(key, flight)

	direct := debounceTestEvent("m3", "user-1", "你干的", now.Add(time.Second))
	direct.Segments = append([]MessageSegment{{Type: "at", Data: map[string]string{"qq": "42"}}}, direct.Segments...)
	direct = enqueueParkTestEvent(t, store, direct)
	event, _, handled, _ := runtime.prepareMessageEvent(context.Background(), direct)
	if !handled {
		t.Fatal("@ 机器人应该直接进回复")
	}
	if len(event.backlogTurn) != 1 || event.backlogTurn[0].Event.MessageID != "m1" {
		t.Fatalf("user-1 的直接触发应只接走 m1，不应接走 user-2 的 m2：%+v", event.backlogTurn)
	}
	if !runtime.proactiveRouteFlightTakenFor(flight, m1) {
		t.Fatal("user-1 的 m1 应标记为已被接走")
	}
	if runtime.proactiveRouteFlightTakenFor(flight, m2) {
		t.Fatal("user-2 的 m2 不应被 user-1 的直接触发取消")
	}
	ctxM1 := context.WithValue(context.Background(), proactiveRouteFlightContextKey{}, proactiveRouteFlightTarget{flight: flight, messageIDs: []string{"m1"}})
	ctxM2 := context.WithValue(context.Background(), proactiveRouteFlightContextKey{}, proactiveRouteFlightTarget{flight: flight, messageIDs: []string{"m2"}})
	if !runtime.proactiveRouteTakenBeforeSend(ctxM1) {
		t.Fatal("m1 在发送闸门处应放弃")
	}
	if runtime.proactiveRouteTakenBeforeSend(ctxM2) {
		t.Fatal("m2 在发送闸门处应照常发送")
	}
}
