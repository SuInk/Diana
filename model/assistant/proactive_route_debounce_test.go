// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func debounceTestEvent(id, user, text string, at time.Time) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: user, MessageID: id, Time: at.Unix(),
		RawMessage: text,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

// 合并进来的消息原地标【连发k】、逐条判断；机器人已经回过的标「已回」。
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
		"【连发1】（嘉然已回）[12秒前] Alice：我给你举个例子",
		"【连发2】[8秒前] Bob：那你帮我看看\n",
		"【当前消息】[刚刚] Carol：重启试试",
		"burst_k",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("对话稿缺 %q：\n%s", want, got)
		}
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

// 两个话题一轮回、分开发：判为在叫机器人的连发跟着当前消息这一轮，判否的不跟；回复请求
// 要求按话题分条发送、各自引用原消息。
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
	if len(primary.burstTargets) != 1 || primary.burstTargets[0].Event.MessageID != "m1" {
		t.Fatalf("跟着这一轮的应只有判为在叫的 m1：%#v", primary.burstTargets)
	}
	request := backlogReplyRequestText("嘉然你觉得呢", primary, primary.burstTargets)
	for _, want := range []string{"按话题分开发送", notificationSplitMarker, replyMarkerPrefix, "攻击面管理"} {
		if !strings.Contains(request, want) {
			t.Fatalf("回复请求缺 %q：%s", want, request)
		}
	}
	if strings.Contains(request, "晚上吃啥") {
		t.Fatal("判否的连发不该跟进这一轮")
	}

	// 当前消息不放行时，判为在叫的连发自己当回复对象。
	primary, _, _, allowed = runtime.splitBurstReplies(current, "嘉然你觉得呢", burst, ratings, false)
	if !allowed || primary.MessageID != "m1" || !primary.routingDirected || len(primary.burstTargets) != 0 {
		t.Fatalf("primary=%s allowed=%v directed=%v", primary.MessageID, allowed, primary.routingDirected)
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

// @ 机器人的消息不等，进来时把等着的候选并进自己那一轮，等待批不再另评。
func TestProactiveRouteParkTakenByDirectMessage(t *testing.T) {
	runtime, provider, store := parkTestRuntime(t, BotConfig{BotAccount: "42", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8}, legacyQuietRoute)
	runtime.proactiveRouteDebounce = 200 * time.Millisecond
	now := time.Now()
	first := enqueueParkTestEvent(t, store, debounceTestEvent("message-1", "user-1", "有人知道怎么配代理吗", now))
	if _, _, _, outcome := runtime.prepareMessageEvent(context.Background(), first); outcome != "merged_into_backlog_turn" {
		t.Fatalf("outcome=%q", outcome)
	}
	direct := debounceTestEvent("message-2", "user-2", "看看这个", now)
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
		t.Fatalf("等着的候选应该并进这一轮：%+v", event.backlogTurn)
	}
	time.Sleep(400 * time.Millisecond)
	for i := range provider.requestsSnapshot() {
		if strings.Contains(routerInput(provider, i), "有人知道怎么配代理吗") && strings.Contains(routerInput(provider, i), "本批群消息") {
			t.Fatal("已经并进 @ 那一轮，不该再单独评分")
		}
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
