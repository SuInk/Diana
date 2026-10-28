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

// 合并进来的消息已经进了历史，对话稿要把它们挪进当前消息一起列出、逐条判断，否则只问
// 当前那句，前面那句真在叫机器人就丢了。
func TestProactiveReplyTranscriptMarksDebouncedBurst(t *testing.T) {
	age := func(v int64) *int64 { return &v }
	payload := proactiveReplyPayload{
		Addressing:    messageAddressing{ReplyTarget: "none"},
		CurrentText:   "重启试试",
		CurrentSender: "Carol",
		BotAliases:    []string{"嘉然"},
		RecentMessages: []proactiveReplyHistoryItem{
			{MessageID: "m2", Sender: "Bob", Text: "那你帮我看看", AgeSeconds: age(8), Addressing: messageAddressing{ReplyTarget: "none"}},
			{MessageID: "m1", Sender: "Alice", Text: "早", AgeSeconds: age(60), Addressing: messageAddressing{ReplyTarget: "none"}},
		},
		Candidates: []proactiveReplyCandidatePayload{{MessageID: "m2"}, {MessageID: "m3", IsCurrent: true}},
	}
	got := proactiveReplyTranscript(payload)
	want := strings.Join([]string{
		"[1分钟前] Alice：早",
		"【当前消息】以下几条是几秒内接连发出的，合并成一次评分，每条都按当前消息判断：",
		"[8秒前] Bob：那你帮我看看",
		"[刚刚] Carol：重启试试",
		"（其中任何一条在跟嘉然说话，directed 就填 true，reason 写明是哪一条）",
	}, "\n")
	if !strings.Contains(got, want) {
		t.Fatalf("连发没有并进当前消息：\n%s", got)
	}
	payload.Candidates = nil
	if strings.Contains(proactiveReplyTranscript(payload), "接连发出") {
		t.Fatal("没有合并时对话稿不该变")
	}
}

// 机器人刚参与过的对话不合并：前一句可能是在接它的话，合并后容易漏判。
func TestProactiveRouteNearBot(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	now := time.Now()
	event := debounceTestEvent("current", "user-1", "看完了", now)
	withHistory := func(history ...MessageEvent) MessageEvent {
		e := event
		e.replyHistory, e.replyHistoryLoaded = history, true
		return e
	}
	bot := debounceTestEvent("bot", "42", "缓存的关键是前缀稳定", now.Add(-5*time.Minute))
	oldBot := debounceTestEvent("old-bot", "42", "早", now.Add(-30*time.Minute))
	mention := debounceTestEvent("mention", "user-1", "你的缓存怎么做的", now.Add(-40*time.Minute))
	mention.Segments = append([]MessageSegment{{Type: "at", Data: map[string]string{"qq": "42"}}}, mention.Segments...)
	other := debounceTestEvent("other", "user-2", "晚上吃啥", now.Add(-time.Minute))
	cases := []struct {
		name  string
		event MessageEvent
		want  bool
	}{
		{"机器人 5 分钟前说过话", withHistory(other, bot), true},
		{"发送者 40 分钟前 @ 过机器人", withHistory(oldBot, mention, other), true},
		{"机器人半小时前说过、没跟发送者来回", withHistory(oldBot, other), false},
		{"机器人没参与", withHistory(other), false},
	}
	for _, tc := range cases {
		if got := runtime.proactiveRouteNearBot(tc.event); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
