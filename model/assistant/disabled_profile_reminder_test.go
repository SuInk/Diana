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

// 「错误提示」开关的契约是「控制所有面向聊天的诊断消息」，但各插件以前各发各的，
// 开关只在回复出错那条路径上生效：关掉的人照样在群里收到「仓库订阅连续 3 次失败」。
// 现在诊断统一走 sendDiagnosticNotice / sendDiagnosticNoticeWithEvidence，这个用例把
// 每个入口都过一遍——失败和恢复都要受同一个开关控制。
func TestErrorNoticeSwitchSilencesEveryPluginDiagnostic(t *testing.T) {
	now := time.Now()
	newRuntime := func(enabled *bool, kind ReminderKind) (*Runtime, Reminder, *recordingChannel) {
		item := Reminder{
			ID:                    "watch-alert",
			Kind:                  kind,
			ProfileID:             "qq",
			OwnerID:               "10001",
			UserID:                "10001",
			Repository:            "SuInk/Diana",
			TriggerAt:             now.Add(-time.Minute),
			IntervalSeconds:       int64(15 * time.Minute / time.Second),
			CreatedAt:             now.Add(-time.Hour),
			ConsecutiveFailures:   defaultRecurringFailureAlertThreshold,
			RecoveryNoticePending: true,
		}
		store := &stubReminderStore{items: []Reminder{item}}
		channel := &recordingChannel{}
		runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001", ErrorNotifyEnabled: enabled}, channel, NewPluginManager(), nil, store, nil, nil)
		runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "qq", OwnerID: "10001", Enabled: true, ErrorNotifyEnabled: enabled}}})
		return runtime, item, channel
	}

	entries := []struct {
		name string
		kind ReminderKind
		call func(*Runtime, Reminder) error
	}{
		{"提醒失败", ReminderKindRepositoryWatch, func(rt *Runtime, item Reminder) error {
			return rt.notifyReminderFailure(context.Background(), item, fmt.Errorf("发送失败"))
		}},
		{"周期订阅失败", ReminderKindRSSWatch, func(rt *Runtime, item Reminder) error {
			rt.reportRecurringReminderFailure(context.Background(), item, fmt.Errorf("抓取失败"))
			return nil
		}},
		{"周期订阅恢复", ReminderKindRSSWatch, func(rt *Runtime, item Reminder) error {
			rt.deliverRecurringRecoveryNotice(context.Background(), item)
			return nil
		}},
		{"仓库订阅失败", ReminderKindRepositoryWatch, func(rt *Runtime, item Reminder) error {
			return rt.notifyRepositoryWatchFailure(context.Background(), item, fmt.Errorf("读取失败"))
		}},
		{"仓库订阅恢复", ReminderKindRepositoryWatch, func(rt *Runtime, item Reminder) error {
			return rt.notifyRepositoryWatchRecovery(context.Background(), item)
		}},
	}

	off, on := false, true
	for _, entry := range entries {
		t.Run(entry.name+"/开关关闭", func(t *testing.T) {
			runtime, item, channel := newRuntime(&off, entry.kind)
			if err := entry.call(runtime, item); err != nil {
				t.Fatalf("开关关闭时应当静默返回，却报错：%v", err)
			}
			if len(channel.sent) != 0 {
				t.Fatalf("关掉「错误提示」后仍然发了出去：%#v", channel.sent)
			}
		})
		t.Run(entry.name+"/开关打开", func(t *testing.T) {
			runtime, item, channel := newRuntime(&on, entry.kind)
			if err := entry.call(runtime, item); err != nil {
				t.Fatalf("开关打开时发送失败：%v", err)
			}
			if len(channel.sent) == 0 {
				t.Fatal("开关打开时应当照常发出")
			}
		})
	}
}

// 会报错的插件统一带上「发送错误通知」开关，插件清单里不用各写一遍。
// 追加设置项时必须先复制切片：直接 append 会写进插件自己那份清单的底层数组，把同一批
// 里别的插件设置项覆盖掉——第一版就是这样让视频抽帧的开关失了效。
func TestErrorNoticeSettingIsAddedWithoutClobberingOthers(t *testing.T) {
	shared := []PluginSettingSpec{
		{Key: "keep_me", Label: "原有设置", Type: PluginSettingTypeBool, Default: true},
		{Key: "and_me", Label: "另一个", Type: PluginSettingTypeNumber, Default: 3},
	}
	manifest := PluginManifest{ID: "official.test", BuiltIn: true, ReportsErrors: true, Settings: shared[:1]}

	decorated := withErrorNoticeSetting(manifest)

	if len(decorated.Settings) != 2 || decorated.Settings[1].Key != pluginErrorNoticeSetting {
		t.Fatalf("没有补上错误通知开关：%#v", decorated.Settings)
	}
	if decorated.Settings[1].Default != true {
		t.Fatalf("默认应当是开着的：%#v", decorated.Settings[1])
	}
	if shared[1].Key != "and_me" {
		t.Fatalf("append 覆盖了共享数组里的下一项：%#v", shared[1])
	}
	// 不报错的插件不加，插件页上不该多出一个永远用不上的开关。
	if got := withErrorNoticeSetting(PluginManifest{ID: "official.quiet", BuiltIn: true}); len(got.Settings) != 0 {
		t.Fatalf("不报错的插件不该有这个开关：%#v", got.Settings)
	}
	// 插件自己声明过就不重复加。
	own := PluginManifest{ID: "official.own", ReportsErrors: true, Settings: []PluginSettingSpec{{Key: pluginErrorNoticeSetting}}}
	if got := withErrorNoticeSetting(own); len(got.Settings) != 1 {
		t.Fatalf("重复添加了：%#v", got.Settings)
	}
}

// 插件各自的开关：关掉某个插件的错误通知，只有它的诊断消息不发，别的插件照旧。
func TestPluginErrorNoticeSwitchIsPerPlugin(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001"}, &recordingChannel{}, NewDefaultPluginManager(), nil, &stubReminderStore{}, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "qq", OwnerID: "10001", Enabled: true}}})
	event := MessageEvent{Kind: EventKindPrivate, ProfileID: "qq", UserID: "10001"}

	if !runtime.diagnosticAllowed(event, repositoryWatchPluginID) {
		t.Fatal("默认应当允许发送仓库订阅的错误通知")
	}

	if _, err := runtime.plugins.UpdateSettings(repositoryWatchPluginID, map[string]any{pluginErrorNoticeSetting: false}); err != nil {
		t.Fatal(err)
	}
	if runtime.diagnosticAllowed(event, repositoryWatchPluginID) {
		t.Fatal("关掉仓库订阅的错误通知后不该再发")
	}
	if !runtime.diagnosticAllowed(event, rssWatchPluginID) {
		t.Fatal("关掉一个插件不该影响另一个插件")
	}
	if !runtime.diagnosticAllowed(event, "") {
		t.Fatal("不属于任何插件的诊断只看机器人的开关")
	}
}

// 第三方清单也能声明 reports_errors。清单是严格解码的，漏加字段的话插件作者照文档写
// 反而会被判成格式错误。
func TestRepoPluginManifestCarriesReportsErrors(t *testing.T) {
	const data = `{"id":"suink.hello","name":"示例","version":"1.0.0","description":"说明",` +
		`"permissions":["message:read"],"entry":"SKILL.md","reports_errors":true}`
	file, err := decodeRepoPluginManifest([]byte(data))
	if err != nil {
		t.Fatalf("清单被拒绝了：%v", err)
	}
	manifest := withErrorNoticeSetting(file.pluginManifest())
	if !manifest.ReportsErrors {
		t.Fatal("reports_errors 没有传到 PluginManifest")
	}
	if len(manifest.Settings) != 1 || manifest.Settings[0].Key != pluginErrorNoticeSetting {
		t.Fatalf("第三方插件没有拿到错误通知开关：%#v", manifest.Settings)
	}
}
