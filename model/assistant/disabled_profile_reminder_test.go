// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// 机器人关掉之后它的订阅不该继续跑。线上那台 Telegram 停用了，仓库订阅照样每隔几
// 分钟抓一次 GitHub，再因为「outbound profile is not configured」失败，最后把失败
// 通知推给主人——关掉的那台反而比开着时更吵。
func TestDisabledProfileSubscriptionsDoNotRun(t *testing.T) {
	now := time.Now()
	store := &stubReminderStore{items: []Reminder{{
		ID:              "watch-off",
		Kind:            ReminderKindRSSWatch,
		ProfileID:       "telegram-off",
		OwnerID:         "10001",
		UserID:          "10001",
		FeedURL:         "https://example.invalid/feed.xml",
		Message:         "有新动态就说一声",
		TriggerAt:       now.Add(-time.Minute),
		IntervalSeconds: int64(15 * time.Minute / time.Second),
		CreatedAt:       now.Add(-time.Hour),
	}}}
	runtime := NewRuntime(BotConfig{ID: "qq-on", OwnerID: "10001"}, &recordingChannel{}, NewPluginManager(), nil, store, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{
		{ID: "qq-on", OwnerID: "10001", Enabled: true},
		{ID: "telegram-off", Platform: PlatformTelegram, OwnerID: "10001", Enabled: false},
	}})

	runtime.fireDueReminders(context.Background())

	if got := store.items[0].ConsecutiveFailures; got != 0 {
		t.Fatalf("停用机器人的订阅仍然执行并失败了：ConsecutiveFailures = %d", got)
	}
	if !store.items[0].LastRunAt.IsZero() {
		t.Fatalf("停用机器人的订阅仍然被执行：LastRunAt = %s", store.items[0].LastRunAt)
	}

	// 重新启用后按原周期继续，不需要用户再去点一次。
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{
		{ID: "qq-on", OwnerID: "10001", Enabled: true},
		{ID: "telegram-off", Platform: PlatformTelegram, OwnerID: "10001", Enabled: true},
	}})
	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	runtime.fireDueReminders(context.Background())
	if store.items[0].ConsecutiveFailures == 0 && store.items[0].LastRunAt.IsZero() {
		t.Fatal("重新启用后订阅没有恢复执行")
	}
}

// 没有绑定机器人的老提醒（ProfileID 为空）不受影响，照常执行。
func TestRemindersWithoutProfileStillRun(t *testing.T) {
	now := time.Now()
	store := &stubReminderStore{items: []Reminder{{
		ID:              "legacy",
		Kind:            ReminderKindRSSWatch,
		OwnerID:         "10001",
		UserID:          "10001",
		FeedURL:         "https://example.invalid/feed.xml",
		Message:         "有新动态就说一声",
		TriggerAt:       now.Add(-time.Minute),
		IntervalSeconds: int64(15 * time.Minute / time.Second),
		CreatedAt:       now.Add(-time.Hour),
	}}}
	runtime := NewRuntime(BotConfig{ID: "qq-on", OwnerID: "10001"}, &recordingChannel{}, NewPluginManager(), nil, store, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "qq-on", OwnerID: "10001", Enabled: true}}})

	runtime.fireDueReminders(context.Background())
	if store.items[0].ConsecutiveFailures == 0 && store.items[0].LastRunAt.IsZero() {
		t.Fatal("未绑定机器人的提醒被跳过了")
	}
}

// 关机器人/重启时，在途的 GitHub、Feed 请求会带着 context canceled 回来。那不是订阅
// 坏了：计进连败之后，下一次真失败就更快攒够阈值，于是「把机器人关掉」反而把失败告警
// 推了出去。线上 04:47:40 几个仓库同时报 context canceled，就是这么来的。
func TestReminderRunInterruptedOnlyCountsOurOwnCancellation(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()

	wrapped := fmt.Errorf("读取 SuInk/Diana releases: %w", context.Canceled)
	if !reminderRunInterrupted(cancelled, wrapped) {
		t.Fatal("退出过程中的 context canceled 应当算「被打断」")
	}
	// 父 ctx 还活着说明不是我们掐的，是对端断了：这仍然是一次真失败。
	if reminderRunInterrupted(live, wrapped) {
		t.Fatal("父 ctx 未取消时不该算被打断")
	}
	// 任务自己的超时是真失败，即使正好赶上退出也要照常计。
	if reminderRunInterrupted(cancelled, fmt.Errorf("读取超时: %w", context.DeadlineExceeded)) {
		t.Fatal("任务自身超时不该被当成被打断")
	}
	if reminderRunInterrupted(cancelled, nil) {
		t.Fatal("没有错误就不该算被打断")
	}
}

// 被打断的周期任务排到下一个周期，失败状态原样保留：既不计连败，也不按失败退避提前重试。
func TestRescheduleInterruptedReminderKeepsFailureState(t *testing.T) {
	now := time.Now()
	store := &stubReminderStore{items: []Reminder{{
		ID:                  "watch-cancel",
		Kind:                ReminderKindRSSWatch,
		OwnerID:             "10001",
		UserID:              "10001",
		FeedURL:             "https://example.invalid/feed.xml",
		TriggerAt:           now.Add(-time.Minute),
		IntervalSeconds:     int64(15 * time.Minute / time.Second),
		CreatedAt:           now.Add(-time.Hour),
		ConsecutiveFailures: 1,
		LastError:           "上一次真的失败了",
	}}}
	runtime := NewRuntime(BotConfig{ID: "qq-on", OwnerID: "10001"}, &recordingChannel{}, NewPluginManager(), nil, store, nil, nil)

	runtime.rescheduleInterruptedReminder("watch-cancel", now)

	if got := store.items[0].ConsecutiveFailures; got != 1 {
		t.Fatalf("被打断的这次被计进了连败：ConsecutiveFailures = %d, want 1", got)
	}
	if store.items[0].LastError != "上一次真的失败了" {
		t.Fatalf("原有的失败状态被覆盖：LastError = %q", store.items[0].LastError)
	}
	if wait := time.Until(store.items[0].TriggerAt); wait < time.Minute {
		t.Fatalf("应排到下一个周期，实际 %s 后就重试", wait)
	}
}

// 「错误提示」开关的契约是「控制所有面向聊天的诊断消息」，但订阅的失败告警以前绕过
// 它照发：关掉开关的人照样在群里收到「仓库订阅连续 3 次失败」。
func TestErrorNoticeSwitchSilencesSubscriptionAlerts(t *testing.T) {
	off := false
	on := true
	now := time.Now()
	newRuntime := func(enabled *bool) (*Runtime, *stubReminderStore, *recordingChannel) {
		store := &stubReminderStore{items: []Reminder{{
			ID:                  "watch-alert",
			Kind:                ReminderKindRepositoryWatch,
			ProfileID:           "qq",
			OwnerID:             "10001",
			UserID:              "10001",
			Repository:          "SuInk/Diana",
			TriggerAt:           now.Add(-time.Minute),
			IntervalSeconds:     int64(15 * time.Minute / time.Second),
			CreatedAt:           now.Add(-time.Hour),
			ConsecutiveFailures: defaultRecurringFailureAlertThreshold,
		}}}
		channel := &recordingChannel{}
		runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001", ErrorNotifyEnabled: enabled}, channel, NewPluginManager(), nil, store, nil, nil)
		runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "qq", OwnerID: "10001", Enabled: true, ErrorNotifyEnabled: enabled}}})
		return runtime, store, channel
	}

	runtime, store, channel := newRuntime(&off)
	if err := runtime.notifyRepositoryWatchFailure(context.Background(), store.items[0], fmt.Errorf("读取失败")); err != nil {
		t.Fatalf("开关关闭时应当静默返回，却报错：%v", err)
	}
	if len(channel.sent) != 0 {
		t.Fatalf("关掉「错误提示」后仍然发出了失败告警：%#v", channel.sent)
	}
	if err := runtime.notifyRepositoryWatchRecovery(context.Background(), store.items[0]); err != nil || len(channel.sent) != 0 {
		t.Fatalf("恢复通知也不该发：err=%v sent=%#v", err, channel.sent)
	}

	// 开着的时候照常发，否则这个开关就变成「永远不报」了。
	runtime, store, channel = newRuntime(&on)
	if err := runtime.notifyRepositoryWatchFailure(context.Background(), store.items[0], fmt.Errorf("读取失败")); err != nil {
		t.Fatalf("开关打开时发送失败：%v", err)
	}
	if len(channel.sent) == 0 {
		t.Fatal("开关打开时应当发出失败告警")
	}
}
