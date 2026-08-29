// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 用户遇到的就是这条：抓 Feed 一次 TLS 超时，群里立刻收到「本次执行失败」。
// 周期订阅有下一个周期，抖一下不该出声，连着坏三次才值得打扰。
func TestRSSWatchFailureAlertsOnlyAfterThreshold(t *testing.T) {
	now := time.Now()
	store := &stubReminderStore{items: []Reminder{{
		ID:              "rss-fail",
		Kind:            ReminderKindRSSWatch,
		OwnerID:         "10001",
		GroupID:         "123456",
		UserID:          "10001",
		FeedURL:         "https://example.invalid/feed.xml",
		Message:         "有新动态就说一声",
		TriggerAt:       now.Add(-time.Minute),
		IntervalSeconds: int64(15 * time.Minute / time.Second),
		CreatedAt:       now.Add(-time.Hour),
	}}}
	channel := &recordingChannel{}
	// 插件停用是个不依赖网络的确定失败，走的仍然是 RSS 订阅那条失败通路。
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, channel, NewPluginManager(), nil, store, nil, nil)

	for attempt := 1; attempt < recurringFailureAlertThreshold; attempt++ {
		store.items[0].TriggerAt = time.Now().Add(-time.Second)
		runtime.fireDueReminders(context.Background())
		if len(channel.sent) != 0 {
			t.Fatalf("failure %d should stay quiet, sent = %#v", attempt, channel.sent)
		}
		if store.items[0].ConsecutiveFailures != attempt {
			t.Fatalf("failure %d: ConsecutiveFailures = %d", attempt, store.items[0].ConsecutiveFailures)
		}
	}

	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	runtime.fireDueReminders(context.Background())
	if len(channel.sent) != 1 {
		t.Fatalf("third failure should alert once, sent = %#v", channel.sent)
	}
	notice := channel.sent[0].Text
	for _, want := range []string{"RSS 订阅", "连续 3 次", "自动重试"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("alert missing %q: %q", want, notice)
		}
	}
	if strings.Contains(notice, "rss-fail") {
		t.Fatalf("alert leaked subscription id: %q", notice)
	}
	if store.items[0].FailureAlertedAt.IsZero() {
		t.Fatalf("delivered alert was not marked: %#v", store.items[0])
	}

	// 同一轮故障不重复报警，但失败计数照常往上走，排查时看得见。
	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	runtime.fireDueReminders(context.Background())
	if len(channel.sent) != 1 {
		t.Fatalf("alert repeated within one outage: %#v", channel.sent)
	}
	if store.items[0].ConsecutiveFailures != recurringFailureAlertThreshold+1 {
		t.Fatalf("failure counter stalled: %#v", store.items[0])
	}
}

// 一次性提醒没有下一个周期，漏报就等于这条提醒悄悄没了，所以仍然失败即报。
func TestRecurringFailureShouldAlertKeepsOneTimeRemindersImmediate(t *testing.T) {
	oneTime := Reminder{ID: "once", Kind: ReminderKindMessage, ConsecutiveFailures: 1}
	if !recurringFailureShouldAlert(oneTime) {
		t.Fatal("one-time reminder must alert on the first failure")
	}

	recurringKinds := []Reminder{
		{ID: "query", Kind: ReminderKindQuery, IntervalSeconds: 900},
		{ID: "rss", Kind: ReminderKindRSSWatch, IntervalSeconds: 900, FeedURL: "https://example.invalid/feed.xml"},
		{ID: "repo", Kind: ReminderKindRepositoryWatch, IntervalSeconds: 900, Repository: "SuInk/diana", LastErrorFingerprint: "fp"},
	}
	for _, item := range recurringKinds {
		for failures := 0; failures < recurringFailureAlertThreshold; failures++ {
			item.ConsecutiveFailures = failures
			if recurringFailureShouldAlert(item) {
				t.Fatalf("%s alerted after %d failure(s)", item.ID, failures)
			}
		}
		item.ConsecutiveFailures = recurringFailureAlertThreshold
		if !recurringFailureShouldAlert(item) {
			t.Fatalf("%s never alerted at the threshold", item.ID)
		}
		item.FailureAlertedAt = time.Now()
		if recurringFailureShouldAlert(item) {
			t.Fatalf("%s re-alerted within one outage", item.ID)
		}
	}
}

// 没报过警就别报「已恢复」：一次抖动的失败本来就没出声，恢复通知会凭空多出一条消息。
func TestResetRecurringFailureStateOnlyPromisesRecoveryNoticeAfterAnAlert(t *testing.T) {
	quiet := Reminder{ConsecutiveFailures: recurringFailureAlertThreshold - 1}
	resetRecurringFailureStateAfterSuccess(&quiet)
	if quiet.RecoveryNoticePending {
		t.Fatalf("recovery notice queued for an outage nobody was told about: %#v", quiet)
	}

	alerted := Reminder{ConsecutiveFailures: recurringFailureAlertThreshold + 1, FailureAlertedAt: time.Now()}
	resetRecurringFailureStateAfterSuccess(&alerted)
	if !alerted.RecoveryNoticePending || !alerted.FailureAlertedAt.IsZero() {
		t.Fatalf("recovery bookkeeping = %#v", alerted)
	}
}
