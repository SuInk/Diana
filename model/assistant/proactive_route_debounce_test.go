// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
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

// 三个人几秒内连发，前两条都交给最后一条，只评一次分，三条都在评分输入里。
func TestProactiveRouteDebounceMergesBurstIntoOneRoute(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{`{"should_reply":false,"confidence":0.2,"category":"chat","directed_at_bot":false,"answerable":true}`}}
	runtime := NewRuntime(BotConfig{
		BotAccount: "42", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.proactiveRouteDebounce = 20 * time.Millisecond
	store := newMemoryInboundEventStore()
	runtime.SetInboundEventStore(store)
	now := time.Now()
	events := []MessageEvent{
		debounceTestEvent("message-1", "user-1", "这个报错应该怎么处理？", now.Add(-2*time.Second)),
		debounceTestEvent("message-2", "user-2", "我也遇到了同样的报错", now.Add(-time.Second)),
		debounceTestEvent("message-3", "user-3", "重启试试", now),
	}
	for i := range events {
		id, _, err := store.EnqueueInboundEvent(context.Background(), sessionKey(events[i]), events[i])
		if err != nil {
			t.Fatal(err)
		}
		events[i].backlogProbe = &InboundQueueItem{ID: id, Session: sessionKey(events[i]), Event: events[i], Attempts: 1, EnqueuedAt: time.Now()}
	}
	for i := 0; i < 2; i++ {
		if _, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), events[i]); handled || outcome != "merged_into_backlog_turn" {
			t.Fatalf("%s handled=%v outcome=%q", events[i].MessageID, handled, outcome)
		}
		store.mu.Lock()
		store.records[events[i].backlogProbe.ID].state = "done"
		store.mu.Unlock()
	}
	if len(provider.requestsSnapshot()) != 0 {
		t.Fatal("后面还有消息排队时不该评分")
	}
	runtime.prepareMessageEvent(context.Background(), events[2])
	requests := provider.requestsSnapshot()
	if len(requests) != 1 {
		t.Fatalf("router calls=%d, want 1", len(requests))
	}
	input := requests[0].Messages[len(requests[0].Messages)-1].Content
	for _, want := range []string{"这个报错应该怎么处理", "我也遇到了同样的报错", "重启试试"} {
		if !strings.Contains(input, want) {
			t.Fatalf("评分输入缺 %q：%q", want, input)
		}
	}
}

// 窗口内没人接着说就照常评分，只多等这一个窗口。
func TestProactiveRouteDebounceRoutesWhenQuiet(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{`{"should_reply":false,"confidence":0.2,"category":"chat","directed_at_bot":false,"answerable":true}`}}
	runtime := NewRuntime(BotConfig{
		BotAccount: "42", ProactiveReplyChance: 1, ProactiveReplyThreshold: 0.8,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.proactiveRouteDebounce = 20 * time.Millisecond
	store := newMemoryInboundEventStore()
	runtime.SetInboundEventStore(store)
	event := debounceTestEvent("message-1", "user-1", "这个报错应该怎么处理？", time.Now())
	id, _, _ := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event)
	event.backlogProbe = &InboundQueueItem{ID: id, Session: sessionKey(event), Event: event, Attempts: 1, EnqueuedAt: time.Now()}
	if _, _, _, outcome := runtime.prepareMessageEvent(context.Background(), event); outcome == "merged_into_backlog_turn" {
		t.Fatal("没有后续消息时不该交出去")
	}
	if got := len(provider.requestsSnapshot()); got != 1 {
		t.Fatalf("router calls=%d, want 1", got)
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
