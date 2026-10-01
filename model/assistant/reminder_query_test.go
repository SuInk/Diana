// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDianaReminderToolCreatesQueryReminder(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaReminderTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"})

	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "create", "delay": "2h", "message": "带伞", "query": "查天气",
	}); err == nil || len(store.items) != 0 {
		t.Fatalf("message+query should be rejected: err=%v items=%#v", err, store.items)
	}
	out, err := tool.Run(context.Background(), map[string]any{
		"operation": "create", "delay": "2h", "query": "查今天杭州天气，下雨就提醒带伞",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 || !store.items[0].RunQuery || store.items[0].Kind != ReminderKindMessage || store.items[0].Message != "查今天杭州天气，下雨就提醒带伞" {
		t.Fatalf("items = %#v", store.items)
	}
	if !strings.Contains(out, `"run_query": true`) {
		t.Fatalf("tool output should mark run_query: %s", out)
	}

	// 改成 message 就是改回原样念。
	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "update", "id": store.items[0].ID, "message": "记得带伞",
	}); err != nil {
		t.Fatal(err)
	}
	if store.items[0].RunQuery || store.items[0].Message != "记得带伞" {
		t.Fatalf("updated = %#v", store.items[0])
	}
	// 只改时间不动种类。
	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "update", "id": store.items[0].ID, "query": "查天气",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "update", "id": store.items[0].ID, "delay": "3h",
	}); err != nil {
		t.Fatal(err)
	}
	if !store.items[0].RunQuery || store.items[0].Message != "查天气" {
		t.Fatalf("time-only update changed content: %#v", store.items[0])
	}
}

func TestDueQueryReminderRunsAgentAndSendsResult(t *testing.T) {
	store := &stubReminderStore{items: []Reminder{{
		ID:        "rq-1",
		Kind:      ReminderKindMessage,
		OwnerID:   "10001",
		UserID:    "10001",
		Message:   "查今天杭州天气，下雨就提醒带伞",
		RunQuery:  true,
		TriggerAt: time.Now().Add(-time.Second),
		CreatedAt: time.Now().Add(-time.Hour),
	}}}
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"final","content":"杭州今天下午有雨，出门记得带伞。"}`,
	}}
	runtime := NewRuntime(BotConfig{
		OwnerID:        "10001",
		SystemPrompt:   "你是说话自然的 Diana。",
		AgentEnabled:   true,
		AgentMaxSteps:  3,
		RequestTimeout: 5 * time.Second,
	}, channel, NewPluginManager(), nil, store, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	runtime.fireDueReminders(context.Background())

	if len(channel.sent) != 1 || !strings.Contains(channel.sent[0].Text, "下午有雨") || strings.Contains(channel.sent[0].Text, "提醒你：") {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if len(provider.requests) != 1 || !requestMessagesContain(provider.requests[0].Messages, "查今天杭州天气") {
		t.Fatalf("requests = %#v", provider.requests)
	}
	done := store.items[0]
	if done.LastRunAt.IsZero() || done.PendingDelivery != "" || !done.PendingSince.IsZero() {
		t.Fatalf("delivered state = %#v", done)
	}
}

func TestQueryReminderSendFailureRetriesWithoutRerunningAgent(t *testing.T) {
	store := &stubReminderStore{items: []Reminder{{
		ID:        "rq-2",
		Kind:      ReminderKindMessage,
		OwnerID:   "10001",
		GroupID:   "12345",
		UserID:    "10001",
		Message:   "看看比赛结果",
		RunQuery:  true,
		TriggerAt: time.Now().Add(-time.Second),
		CreatedAt: time.Now().Add(-time.Hour),
	}}}
	channel := newScriptedBackoffChannel("12345")
	channel.alwaysFail["12345"] = true
	provider := &sequenceLLMProvider{replies: []string{`{"action":"final","content":"主队 2:1 赢了。"}`}}
	runtime := NewRuntime(BotConfig{
		OwnerID:        "10001",
		AgentEnabled:   true,
		AgentMaxSteps:  3,
		RequestTimeout: 5 * time.Second,
	}, channel, NewPluginManager(), nil, store, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	ctx := withOutboundDeliveryPolicy(context.Background(), fastOutboundDeliveryPolicy())

	runtime.fireDueReminders(ctx)

	failed := store.items[0]
	if failed.ConsecutiveFailures != 1 || !failed.LastRunAt.IsZero() || !strings.Contains(failed.PendingDelivery, "2:1") {
		t.Fatalf("failed state = %#v", failed)
	}

	channel.mu.Lock()
	channel.alwaysFail["12345"] = false
	channel.mu.Unlock()
	time.Sleep(fastOutboundDeliveryPolicy().DropCooldown + 10*time.Millisecond)
	store.items[0].TriggerAt = time.Now().Add(-time.Second)

	runtime.fireDueReminders(ctx)

	if len(provider.requests) != 1 {
		t.Fatalf("retry reran agent: requests=%d", len(provider.requests))
	}
	if !containsText(channel.attemptTexts("12345"), "主队 2:1 赢了。") {
		t.Fatalf("held result never reached the group: %#v", channel.attemptTexts("12345"))
	}
	done := store.items[0]
	if done.LastRunAt.IsZero() || done.PendingDelivery != "" || done.ConsecutiveFailures != 0 {
		t.Fatalf("delivered state = %#v", done)
	}
}

// 关了 Agent 的机器人没法执行，一次性提醒照原样念出来，不挂着无限重试。
func TestQueryReminderFallsBackToPlainNoticeWithoutAgent(t *testing.T) {
	store := &stubReminderStore{items: []Reminder{{
		ID:        "rq-3",
		Kind:      ReminderKindMessage,
		OwnerID:   "10001",
		UserID:    "10001",
		Message:   "查天气",
		RunQuery:  true,
		TriggerAt: time.Now().Add(-time.Second),
		CreatedAt: time.Now().Add(-time.Hour),
	}}}
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{`{"action":"final","content":"不该被调用"}`}}
	runtime := NewRuntime(BotConfig{OwnerID: "10001", RequestTimeout: 5 * time.Second}, channel, NewPluginManager(), nil, store, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	runtime.fireDueReminders(context.Background())

	if len(provider.requests) != 0 {
		t.Fatalf("agent disabled but LLM called: %d", len(provider.requests))
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != "提醒你：查天气" || store.items[0].LastRunAt.IsZero() {
		t.Fatalf("sent = %#v item = %#v", channel.sent, store.items[0])
	}
}

// 没跑出结果就失败：挂起重试，通知里说的是执行失败而不是发送失败。
func TestQueryReminderExecutionFailureRetries(t *testing.T) {
	store := &stubReminderStore{items: []Reminder{{
		ID:        "rq-4",
		Kind:      ReminderKindMessage,
		OwnerID:   "10001",
		UserID:    "10001",
		Message:   "查天气",
		RunQuery:  true,
		TriggerAt: time.Now().Add(-time.Second),
		CreatedAt: time.Now().Add(-time.Hour),
	}}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		OwnerID:        "10001",
		AgentEnabled:   true,
		AgentMaxSteps:  3,
		RequestTimeout: 5 * time.Second,
	}, channel, NewPluginManager(), nil, store, nil, func() (LLMProvider, error) {
		return nil, errors.New("模型不可用")
	})

	runtime.fireDueReminders(context.Background())

	item := store.items[0]
	if item.ConsecutiveFailures != 1 || !item.LastRunAt.IsZero() || item.PendingDelivery != "" || !item.TriggerAt.After(time.Now()) {
		t.Fatalf("failure state = %#v", item)
	}
	if len(channel.sent) != 1 || !strings.Contains(channel.sent[0].Text, "到点执行失败") {
		t.Fatalf("sent = %#v", channel.sent)
	}
}
