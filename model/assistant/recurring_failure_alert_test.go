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

// 用户遇到的就是这条：抓 Feed 一次 TLS 超时，群里立刻收到「本次执行失败」。
// 周期订阅有下一个周期，抖一下不该出声，连着坏够次数才值得打扰。
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

	for attempt := 1; attempt < defaultRecurringFailureAlertThreshold; attempt++ {
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
		t.Fatalf("failure at the threshold should alert once, sent = %#v", channel.sent)
	}
	notice := channel.sent[0].Text
	for _, want := range []string{"RSS 订阅", fmt.Sprintf("连续 %d 次", defaultRecurringFailureAlertThreshold), "自动重试"} {
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
	if store.items[0].ConsecutiveFailures != defaultRecurringFailureAlertThreshold+1 {
		t.Fatalf("failure counter stalled: %#v", store.items[0])
	}
}

// 一次性提醒没有下一个周期，漏报就等于这条提醒悄悄没了，所以仍然失败即报。
func TestRecurringFailureShouldAlertKeepsOneTimeRemindersImmediate(t *testing.T) {
	oneTime := Reminder{ID: "once", Kind: ReminderKindMessage, ConsecutiveFailures: 1}
	if !recurringFailureShouldAlert(oneTime, defaultRecurringFailureAlertThreshold) {
		t.Fatal("one-time reminder must alert on the first failure")
	}

	recurringKinds := []Reminder{
		{ID: "query", Kind: ReminderKindQuery, IntervalSeconds: 900},
		{ID: "rss", Kind: ReminderKindRSSWatch, IntervalSeconds: 900, FeedURL: "https://example.invalid/feed.xml"},
		{ID: "repo", Kind: ReminderKindRepositoryWatch, IntervalSeconds: 900, Repository: "SuInk/diana", LastErrorFingerprint: "fp"},
	}
	for _, item := range recurringKinds {
		for failures := 0; failures < defaultRecurringFailureAlertThreshold; failures++ {
			item.ConsecutiveFailures = failures
			if recurringFailureShouldAlert(item, defaultRecurringFailureAlertThreshold) {
				t.Fatalf("%s alerted after %d failure(s)", item.ID, failures)
			}
		}
		item.ConsecutiveFailures = defaultRecurringFailureAlertThreshold
		if !recurringFailureShouldAlert(item, defaultRecurringFailureAlertThreshold) {
			t.Fatalf("%s never alerted at the threshold", item.ID)
		}
		item.FailureAlertedAt = time.Now()
		if recurringFailureShouldAlert(item, defaultRecurringFailureAlertThreshold) {
			t.Fatalf("%s re-alerted within one outage", item.ID)
		}
	}
}

// 没报过警就别报「已恢复」：一次抖动的失败本来就没出声，恢复通知会凭空多出一条消息。
func TestResetRecurringFailureStateOnlyPromisesRecoveryNoticeAfterAnAlert(t *testing.T) {
	quiet := Reminder{ConsecutiveFailures: defaultRecurringFailureAlertThreshold - 1}
	resetRecurringFailureStateAfterSuccess(&quiet)
	if quiet.RecoveryNoticePending {
		t.Fatalf("recovery notice queued for an outage nobody was told about: %#v", quiet)
	}

	alerted := Reminder{ConsecutiveFailures: defaultRecurringFailureAlertThreshold + 1, FailureAlertedAt: time.Now()}
	resetRecurringFailureStateAfterSuccess(&alerted)
	if !alerted.RecoveryNoticePending || !alerted.FailureAlertedAt.IsZero() {
		t.Fatalf("recovery bookkeeping = %#v", alerted)
	}
}

// 后台把阈值设成 0 就是「别再往群里报错了」：连续失败多少次都不出声，
// 计数和 LastError 照常写，后台仍然看得见这条订阅坏了。
func TestRecurringFailureAlertThresholdZeroSilencesAlerts(t *testing.T) {
	now := time.Now()
	store := &stubReminderStore{items: []Reminder{{
		ID:              "rss-silent",
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
	runtime := NewRuntime(BotConfig{OwnerID: "10001", ErrorNotifyEnabled: boolPointer(false)}, channel, NewPluginManager(), nil, store, nil, nil)

	for attempt := 1; attempt <= defaultRecurringFailureAlertThreshold+2; attempt++ {
		store.items[0].TriggerAt = time.Now().Add(-time.Second)
		runtime.fireDueReminders(context.Background())
		if len(channel.sent) != 0 {
			t.Fatalf("failure %d broke silence, sent = %#v", attempt, channel.sent)
		}
		if store.items[0].ConsecutiveFailures != attempt {
			t.Fatalf("failure %d: ConsecutiveFailures = %d", attempt, store.items[0].ConsecutiveFailures)
		}
	}
	if strings.TrimSpace(store.items[0].LastError) == "" {
		t.Fatalf("silence must not swallow the diagnosis: %#v", store.items[0])
	}
	if !store.items[0].FailureAlertedAt.IsZero() {
		t.Fatalf("nothing was sent, so nothing should be marked as alerted: %#v", store.items[0])
	}
}

// 阈值改成 1 就是「第一次坏就告诉我」，仓库订阅也认同一个配置。
func TestRepositoryWatchFailureAlertThresholdIsConfigurable(t *testing.T) {
	item := Reminder{ID: "repo", Kind: ReminderKindRepositoryWatch, IntervalSeconds: 900, Repository: "SuInk/diana", LastErrorFingerprint: "fp", ConsecutiveFailures: 1}
	if repositoryWatchFailureShouldAlert(item, defaultRecurringFailureAlertThreshold) {
		t.Fatal("one failure must stay quiet at the default threshold")
	}
	if !repositoryWatchFailureShouldAlert(item, 1) {
		t.Fatal("threshold 1 must alert on the first failure")
	}
	if repositoryWatchFailureShouldAlert(item, 0) {
		t.Fatal("threshold 0 must stay quiet forever")
	}
}

// 阈值读取：报不报跟着「出错时在聊天里提示」和插件的「发送错误通知」，几次才报读 RSS / 仓库订阅插件自己的设置，
// 定时查询没有插件用默认值。
func TestRecurringFailureAlertThresholdReadsPluginSettings(t *testing.T) {
	rss := Reminder{ID: "rss", Kind: ReminderKindRSSWatch, IntervalSeconds: 900, UserID: "10001", FeedURL: "https://example.com/feed.xml"}
	repo := Reminder{ID: "repo", Kind: ReminderKindRepositoryWatch, IntervalSeconds: 900, UserID: "10001", Repository: "SuInk/diana"}
	plugins := NewPluginManager(NewRSSWatchPlugin(nil), NewRepositoryWatchPlugin(nil))
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, &recordingChannel{}, plugins, nil, &stubReminderStore{}, nil, nil)
	if got := runtime.recurringFailureAlertThreshold(rss); got != defaultRecurringFailureAlertThreshold {
		t.Fatalf("unset threshold = %d, want default %d", got, defaultRecurringFailureAlertThreshold)
	}
	if _, err := plugins.UpdateSettings(rssWatchPluginID, map[string]any{recurringFailureAlertSettingKey: 2}); err != nil {
		t.Fatal(err)
	}
	if got := runtime.recurringFailureAlertThreshold(rss); got != 2 {
		t.Fatalf("rss threshold = %d, want 2 from the plugin setting", got)
	}
	if got := runtime.recurringFailureAlertThreshold(repo); got != defaultRecurringFailureAlertThreshold {
		t.Fatalf("repository threshold = %d, the rss setting must not leak into it", got)
	}
	quiet := NewRuntime(BotConfig{OwnerID: "10001", ErrorNotifyEnabled: boolPointer(false)}, &recordingChannel{}, plugins, nil, &stubReminderStore{}, nil, nil)
	if got := quiet.recurringFailureAlertThreshold(rss); got != 0 {
		t.Fatalf("threshold with error notifications off = %d, want 0", got)
	}
	if _, err := plugins.UpdateSettings(rssWatchPluginID, map[string]any{recurringFailureAlertSettingKey: 2, pluginErrorNoticeSetting: false}); err != nil {
		t.Fatal(err)
	}
	if got := runtime.recurringFailureAlertThreshold(rss); got != 0 {
		t.Fatalf("threshold with the rss plugin's error notice off = %d, want 0", got)
	}
	if got := runtime.recurringFailureAlertThreshold(repo); got != defaultRecurringFailureAlertThreshold {
		t.Fatalf("repository threshold = %d, the rss switch must not silence it", got)
	}
	if recurringFailureShouldAlert(Reminder{ID: "once", Kind: ReminderKindMessage}, 0) {
		t.Fatal("a one-off reminder must stay quiet when error notifications are off")
	}
}
