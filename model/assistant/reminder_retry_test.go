// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestScheduledQueryFailureNotifiesAndSchedulesRetry(t *testing.T) {
	now := time.Now()
	store := &stubReminderStore{items: []Reminder{{
		ID:              "query-fail",
		Kind:            ReminderKindQuery,
		OwnerID:         "10001",
		GroupID:         "123456",
		UserID:          "10001",
		Message:         "查询最新公告",
		TriggerAt:       now.Add(-time.Minute),
		IntervalSeconds: int64((6 * time.Hour) / time.Second),
		CreatedAt:       now.Add(-7 * time.Hour),
	}}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001", AgentEnabled: false}, channel, NewPluginManager(), nil, store, nil, nil)

	// 到阈值之前只重试、不出声：网络抖一下就往群里吼一嗓子，真出事时没人再看这类消息。
	runtime.fireDueReminders(context.Background())
	if len(channel.sent) != 0 {
		t.Fatalf("first failure should stay quiet, sent = %#v", channel.sent)
	}
	item := store.items[0]
	if item.LastRunAt.IsZero() || item.ConsecutiveFailures != 1 || !strings.Contains(item.LastError, "Agent 已禁用") {
		t.Fatalf("failure state = %#v", item)
	}
	if retryIn := time.Until(item.TriggerAt); retryIn < 5*time.Minute-2*time.Second || retryIn > 6*time.Minute+2*time.Second {
		t.Fatalf("retry scheduled in %s", retryIn)
	}
	if item.PendingDelivery != "" || scheduleStatus(item) != "retrying" {
		t.Fatalf("schedule state = %#v status=%q", item, scheduleStatus(item))
	}

	for attempt := 2; attempt < defaultRecurringFailureAlertThreshold; attempt++ {
		store.items[0].TriggerAt = time.Now().Add(-time.Second)
		runtime.fireDueReminders(context.Background())
		if len(channel.sent) != 0 {
			t.Fatalf("failure %d should stay quiet, sent = %#v", attempt, channel.sent)
		}
	}

	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	runtime.fireDueReminders(context.Background())
	if len(channel.sent) != 1 || channel.sent[0].GroupID != "123456" {
		t.Fatalf("failure notices = %#v", channel.sent)
	}
	for _, want := range []string{fmt.Sprintf("连续 %d 次", defaultRecurringFailureAlertThreshold), "执行失败", "自动重试"} {
		if !strings.Contains(channel.sent[0].Text, want) {
			t.Fatalf("failure notice missing %q: %q", want, channel.sent[0].Text)
		}
	}
	if strings.Contains(channel.sent[0].Text, "query-fail") {
		t.Fatalf("failure notice leaked subscription id: %q", channel.sent[0].Text)
	}
	if store.items[0].FailureAlertedAt.IsZero() {
		t.Fatalf("alert was delivered but never marked: %#v", store.items[0])
	}

	// 报过一次就够了，同一轮故障继续失败不再重复打扰。
	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	runtime.fireDueReminders(context.Background())
	if len(channel.sent) != 1 {
		t.Fatalf("failure alert repeated within one outage: %#v", channel.sent)
	}
}

func TestScheduledQuerySendFailurePersistsResultAndRetriesWithoutLLM(t *testing.T) {
	now := time.Now()
	store := &stubReminderStore{items: []Reminder{{
		ID:              "delivery-fail",
		Kind:            ReminderKindQuery,
		OwnerID:         "10001",
		GroupID:         "123456",
		UserID:          "10001",
		Message:         "查询最新公告并总结变化",
		TriggerAt:       now.Add(-time.Minute),
		IntervalSeconds: int64((6 * time.Hour) / time.Second),
		CreatedAt:       now.Add(-7 * time.Hour),
	}}}
	channel := newScriptedBackoffChannel("123456")
	channel.alwaysFail["123456"] = true
	provider := &sequenceLLMProvider{replies: []string{`{"action":"final","content":"最新公告没有变化。"}`}}
	runtime := NewRuntime(BotConfig{
		OwnerID:        "10001",
		AgentEnabled:   true,
		RequestTimeout: 5 * time.Second,
	}, channel, NewPluginManager(), nil, store, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	ctx := withOutboundDeliveryPolicy(context.Background(), fastOutboundDeliveryPolicy())

	runtime.fireDueReminders(ctx)

	failed := store.items[0]
	if failed.ConsecutiveFailures != 1 || strings.TrimSpace(failed.PendingDelivery) == "" || failed.PendingSince.IsZero() {
		t.Fatalf("pending delivery state = %#v", failed)
	}
	if !strings.Contains(failed.PendingDelivery, "最新公告没有变化") || scheduleStatus(failed) != "retrying" {
		t.Fatalf("pending result = %#v", failed)
	}
	if retryIn := time.Until(failed.TriggerAt); retryIn < 30*time.Minute-2*time.Second || retryIn > 36*time.Minute+2*time.Second {
		t.Fatalf("delivery retry scheduled in %s", retryIn)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("initial LLM requests = %d", len(provider.requests))
	}
	// 结果已经留住了，第一次发不出去不值得单独告警一次。
	if notices := channel.attemptTexts(""); len(notices) != 0 {
		t.Fatalf("first delivery failure should stay quiet: %#v", notices)
	}

	// 连着坏够次数还发不出去才出声，而且说清楚结果没丢。
	for attempt := 2; attempt <= defaultRecurringFailureAlertThreshold; attempt++ {
		time.Sleep(fastOutboundDeliveryPolicy().DropCooldown + 10*time.Millisecond)
		store.items[0].TriggerAt = time.Now().Add(-time.Second)
		runtime.fireDueReminders(ctx)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("delivery retries reran LLM: requests=%d", len(provider.requests))
	}
	privateNotices := channel.attemptTexts("")
	if len(privateNotices) != 1 || !strings.Contains(privateNotices[0], fmt.Sprintf("连续 %d 次发送失败", defaultRecurringFailureAlertThreshold)) || !strings.Contains(privateNotices[0], "结果已保留") {
		t.Fatalf("private failure notices = %#v", privateNotices)
	}
	if strings.Contains(privateNotices[0], "delivery-fail") {
		t.Fatalf("delivery failure notice leaked subscription id: %q", privateNotices[0])
	}

	channel.mu.Lock()
	channel.alwaysFail["123456"] = false
	channel.mu.Unlock()
	time.Sleep(fastOutboundDeliveryPolicy().DropCooldown + 10*time.Millisecond)
	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	pendingMessage := store.items[0].PendingDelivery

	runtime.fireDueReminders(ctx)

	recovered := store.items[0]
	if recovered.PendingDelivery != "" || !recovered.PendingSince.IsZero() || recovered.ConsecutiveFailures != 0 || recovered.LastError != "" {
		t.Fatalf("recovered state = %#v", recovered)
	}
	if !recovered.FailureAlertedAt.IsZero() || recovered.RecoveryNoticePending {
		t.Fatalf("failure alert state survived recovery: %#v", recovered)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("delivery retry reran LLM: requests=%d", len(provider.requests))
	}
	groupAttempts := channel.attemptTexts("123456")
	if !containsText(groupAttempts, pendingMessage) {
		t.Fatalf("held result never reached the group: attempts=%#v pending=%q", groupAttempts, pendingMessage)
	}
	// 报过警就得说一声好了，否则订阅者只知道坏消息。
	if len(groupAttempts) == 0 || !strings.Contains(groupAttempts[len(groupAttempts)-1], "已恢复") {
		t.Fatalf("recovery notice missing: %#v", groupAttempts)
	}
	if nextIn := time.Until(recovered.TriggerAt); nextIn < 5*time.Hour+59*time.Minute || nextIn > 6*time.Hour+time.Minute {
		t.Fatalf("normal schedule did not resume: next in %s", nextIn)
	}
}

func containsText(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestOneTimeReminderSendFailureRetriesAndNotifies(t *testing.T) {
	now := time.Now()
	store := &stubReminderStore{items: []Reminder{{
		ID:        "reminder-fail",
		Kind:      ReminderKindMessage,
		OwnerID:   "10001",
		GroupID:   "123456",
		UserID:    "10001",
		Message:   "去听课",
		TriggerAt: now.Add(-time.Minute),
		CreatedAt: now.Add(-time.Hour),
	}}}
	channel := newScriptedBackoffChannel("123456")
	channel.alwaysFail["123456"] = true
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, store, nil, nil)
	ctx := withOutboundDeliveryPolicy(context.Background(), fastOutboundDeliveryPolicy())

	runtime.fireDueReminders(ctx)

	failed := store.items[0]
	if !failed.LastRunAt.IsZero() || failed.ConsecutiveFailures != 1 || failed.LastError == "" || reminderStatus(failed) != "retrying" {
		t.Fatalf("failed reminder state = %#v status=%q", failed, reminderStatus(failed))
	}
	if retryIn := time.Until(failed.TriggerAt); retryIn < 30*time.Minute-2*time.Second || retryIn > 36*time.Minute+2*time.Second {
		t.Fatalf("reminder retry scheduled in %s", retryIn)
	}
	privateNotices := channel.attemptTexts("")
	if len(privateNotices) != 1 || !strings.Contains(privateNotices[0], "提醒 reminder-fail") || !strings.Contains(privateNotices[0], "自动重试") {
		t.Fatalf("private notices = %#v", privateNotices)
	}

	channel.mu.Lock()
	channel.alwaysFail["123456"] = false
	channel.mu.Unlock()
	time.Sleep(fastOutboundDeliveryPolicy().DropCooldown + 10*time.Millisecond)
	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	runtime.fireDueReminders(ctx)

	recovered := store.items[0]
	if recovered.LastRunAt.IsZero() || recovered.ConsecutiveFailures != 0 || recovered.LastError != "" || reminderStatus(recovered) != "used" {
		t.Fatalf("recovered reminder = %#v status=%q", recovered, reminderStatus(recovered))
	}
}

func TestReminderFailureNoticeDoesNotRecursivelyNotify(t *testing.T) {
	store := &stubReminderStore{items: []Reminder{{
		ID:        "notice-fail",
		Kind:      ReminderKindMessage,
		OwnerID:   "10001",
		UserID:    "10001",
		Message:   "去听课",
		TriggerAt: time.Now().Add(-time.Minute),
		CreatedAt: time.Now().Add(-time.Hour),
	}}}
	channel := newScriptedBackoffChannel()
	channel.alwaysFail[""] = true
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, store, nil, nil)

	runtime.fireDueReminders(context.Background())

	attempts := channel.attemptTexts("")
	if len(attempts) != 2 {
		t.Fatalf("send attempts = %#v, want reminder plus one failure notice", attempts)
	}
	if !strings.Contains(attempts[0], "提醒你") || !strings.Contains(attempts[1], "自动重试") {
		t.Fatalf("send attempts = %#v", attempts)
	}
	if store.items[0].ConsecutiveFailures != 1 || !store.items[0].LastRunAt.IsZero() {
		t.Fatalf("retry state = %#v", store.items[0])
	}
}

func TestDurableReminderRetryDelayUsesSeparateQueryAndDeliveryBackoff(t *testing.T) {
	item := Reminder{ID: "retry-policy"}
	queryErr := errors.New("query failed")
	deliveryErr := &outboundSendError{Cause: errors.New("send failed")}
	queryDelay := durableReminderRetryDelay(item, queryErr, 1)
	deliveryDelay := durableReminderRetryDelay(item, deliveryErr, 1)
	if queryDelay < 5*time.Minute || queryDelay > 6*time.Minute {
		t.Fatalf("query delay = %s", queryDelay)
	}
	if deliveryDelay < 30*time.Minute || deliveryDelay > 36*time.Minute {
		t.Fatalf("delivery delay = %s", deliveryDelay)
	}
	if capped := durableReminderRetryDelay(item, deliveryErr, 20); capped != 6*time.Hour {
		t.Fatalf("maximum delay = %s", capped)
	}
}

func TestReminderRetriesPreserveCompleteErrors(t *testing.T) {
	longError := strings.Repeat("reminder-error-", 80) + "tail-marker"
	now := time.Now()
	store := &stubReminderStore{items: []Reminder{
		{ID: "query", Kind: ReminderKindQuery, TriggerAt: now, IntervalSeconds: 3600},
		{ID: "message", Kind: ReminderKindMessage, TriggerAt: now},
	}}
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(), nil, store, nil, nil)
	if _, err := runtime.finishScheduledQuery("query", now, errors.New(longError)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.rescheduleOneTimeReminder("message", errors.New(longError)); err != nil {
		t.Fatal(err)
	}
	if store.items[0].LastError != longError || store.items[1].LastError != longError {
		t.Fatalf("errors were truncated: query=%d message=%d want=%d", len(store.items[0].LastError), len(store.items[1].LastError), len(longError))
	}
}

func TestReminderPrivateFallbackKeepsProfileIdentity(t *testing.T) {
	// 群发失败后降级到私聊时，Platform/ProfileID/ContextNamespace 不能丢：
	// 它们决定发往哪个机器人。MultiChannel 只有单 binding 时才兜底，多 profile
	// 下裸造事件会让「提醒失败」的兜底通知自己也发不出去，故障就此静默。
	item := Reminder{
		Platform:         PlatformOneBotV11,
		ProfileID:        "onebot-main",
		ContextNamespace: "onebot-main",
		GroupID:          "123456",
		UserID:           "10001",
	}
	target := reminderPrivateFallbackTarget(item)
	if target.Kind != EventKindPrivate || target.GroupID != "" || target.UserID != "10001" {
		t.Fatalf("fallback target = %#v, want a private target for the subscriber", target)
	}
	if target.Platform != PlatformOneBotV11 || target.ProfileID != "onebot-main" || target.ContextNamespace != "onebot-main" {
		t.Fatalf("fallback dropped routing identity: %#v", target)
	}
}

func TestReminderPrivateFallbackRoutesUnderMultipleProfiles(t *testing.T) {
	// 端到端确认：两个 binding 时，降级通知仍要能落到正确的机器人。
	target := newQueueTestChannel()
	other := newQueueTestChannel()
	channel := NewMultiChannel([]ChannelBinding{
		{ProfileID: "onebot-other", Platform: PlatformOneBotV11, Channel: other},
		{ProfileID: "onebot-main", Platform: PlatformOneBotV11, Channel: target},
	})
	runtime := NewRuntime(BotConfig{ID: "onebot-main", Platform: PlatformOneBotV11, BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)

	item := Reminder{
		Platform:         PlatformOneBotV11,
		ProfileID:        "onebot-main",
		ContextNamespace: "onebot-main",
		GroupID:          "123456",
		UserID:           "10001",
	}
	if err := runtime.notifyReminderFailure(context.Background(), item, errOutboundSend); err != nil {
		t.Fatalf("notifyReminderFailure() error = %v", err)
	}
	if target.sentCount() == 0 {
		t.Fatal("failure notice never reached the reminder's own profile")
	}
	if leaked := other.sentCount(); leaked != 0 {
		t.Fatalf("failure notice leaked to another profile: %d message(s)", leaked)
	}
}

func TestDurableReminderRetryDelayCapsConnectionFailures(t *testing.T) {
	item := Reminder{ID: "retry-connection"}
	for _, message := range []string{
		`抓取 Feed 失败: Get "https://example.com/feed.xml": host resolution failed: lookup example.com: i/o timeout`,
		`telegram: request failed: EOF`,
		`Post "https://example.com/v1/responses": dial tcp 192.0.2.1:443: connect: connection refused`,
	} {
		if delay := durableReminderRetryDelay(item, errors.New(message), 20); delay != 30*time.Minute {
			t.Fatalf("connection failure %q delay = %s, want 30m", message, delay)
		}
	}
	if delay := durableReminderRetryDelay(item, errors.New("抓取 Feed 返回 HTTP 404：确认用户名或 Feed 地址是否正确"), 20); delay != 6*time.Hour {
		t.Fatalf("non-connection failure delay = %s, want 6h", delay)
	}
}

func TestDurableReminderRetryDelayNeverExceedsOwnInterval(t *testing.T) {
	item := Reminder{ID: "retry-interval", IntervalSeconds: 600}
	if delay := durableReminderRetryDelay(item, errors.New("HTTP 502"), 20); delay != 10*time.Minute {
		t.Fatalf("delay = %s, want the task's own 10m interval", delay)
	}
	if delay := durableReminderRetryDelay(item, errors.New("HTTP 404"), 20); delay != 6*time.Hour {
		t.Fatalf("permanent 404 delay = %s, want 6h backoff ceiling", delay)
	}
	deliveryErr := &outboundSendError{Cause: errors.New("send failed")}
	if delay := durableReminderRetryDelay(item, deliveryErr, 1); delay != 10*time.Minute {
		t.Fatalf("delivery delay = %s, want the task's own 10m interval", delay)
	}
	monthly := Reminder{ID: "retry-monthly", IntervalMonths: 1, IntervalSeconds: 30 * 24 * 3600}
	if delay := durableReminderRetryDelay(monthly, errors.New("HTTP 502"), 20); delay != 6*time.Hour {
		t.Fatalf("monthly delay = %s, want 6h", delay)
	}
}

func TestAdvanceConnectionRetriesOnlyMovesEarlierConnectionFailures(t *testing.T) {
	now := time.Now()
	recoveredAt := now.Add(-time.Second)
	later := now.Add(5 * time.Hour)
	items := []Reminder{
		{ID: "recovered", ConsecutiveFailures: 3, LastError: "EOF", LastRunAt: now.Add(-time.Hour), TriggerAt: later},
		{ID: "dns", ConsecutiveFailures: 9, LastError: "lookup example.com: i/o timeout", LastRunAt: now.Add(-time.Hour), TriggerAt: later},
		{ID: "not-found", ConsecutiveFailures: 9, LastError: "抓取 Feed 返回 HTTP 404", LastRunAt: now.Add(-time.Hour), TriggerAt: later},
		{ID: "failed-after", ConsecutiveFailures: 1, LastError: "EOF", LastRunAt: now, TriggerAt: later},
		{ID: "cancelled", ConsecutiveFailures: 2, LastError: "EOF", LastRunAt: now.Add(-time.Hour), TriggerAt: later, CancelledAt: now},
		{ID: "healthy", TriggerAt: later},
		{ID: "one-time", ConsecutiveFailures: 2, LastError: "telegram: request failed: EOF", TriggerAt: later},
	}
	if advanced := advanceConnectionRetries(items, "recovered", recoveredAt, now); advanced != 2 {
		t.Fatalf("advanced = %d, want 2", advanced)
	}
	for _, item := range items {
		moved := item.TriggerAt.Before(now.Add(time.Minute))
		if want := item.ID == "dns" || item.ID == "one-time"; moved != want {
			t.Fatalf("%s moved = %v, want %v", item.ID, moved, want)
		}
	}
	if !items[1].TriggerAt.Before(items[6].TriggerAt) {
		t.Fatalf("advanced tasks should be staggered: %s vs %s", items[1].TriggerAt, items[6].TriggerAt)
	}
}

func TestRecurringReminderRecoveryAdvancesOtherConnectionRetries(t *testing.T) {
	now := time.Now()
	later := now.Add(5 * time.Hour)
	store := &stubReminderStore{items: []Reminder{
		{ID: "probe", Kind: ReminderKindRepositoryWatch, Repository: "acme/demo", IntervalSeconds: 600, ConsecutiveFailures: 4, LastError: "EOF", LastRunAt: now.Add(-time.Hour)},
		{ID: "waiting", Kind: ReminderKindRepositoryWatch, Repository: "acme/other", IntervalSeconds: 600, ConsecutiveFailures: 9, LastError: "lookup example.com: i/o timeout", LastRunAt: now.Add(-time.Hour), TriggerAt: later},
	}}
	runtime := NewRuntime(BotConfig{}, &scriptedChannel{}, NewPluginManager(), nil, store, nil, nil)
	if _, err := runtime.finishRecurringReminder("probe", now, nil); err != nil {
		t.Fatal(err)
	}
	if got := store.items[1].TriggerAt; !got.Before(now.Add(time.Minute)) {
		t.Fatalf("waiting task still scheduled at %s", got)
	}
	// 平时的成功不代表网络刚恢复，不去动别的任务。
	store.items[1].TriggerAt = later
	if _, err := runtime.finishRecurringReminder("probe", now.Add(time.Second), nil); err != nil {
		t.Fatal(err)
	}
	if got := store.items[1].TriggerAt; !got.Equal(later) {
		t.Fatalf("healthy success moved waiting task to %s", got)
	}
}
