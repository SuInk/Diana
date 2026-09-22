// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"testing"
)

func runtimeWithDisabledProfile(t *testing.T, disabled string) *Runtime {
	t.Helper()
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.mu.Lock()
	runtime.disabledProfiles = map[string]bool{disabled: true}
	runtime.mu.Unlock()
	return runtime
}

// 停用的机器人没有出站通道，往它的会话投递注定失败。这种失败要能和「上游抖了一下」
// 分开：前者重试多少次都不会好，后者才该重试。
func TestSubscriberNoticeReportsDisabledProfile(t *testing.T) {
	runtime := runtimeWithDisabledProfile(t, "telegram-bot")
	err := runtime.sendSubscriberNotice(context.Background(), MessageEvent{
		ProfileID: "telegram-bot", Kind: EventKindGroup, GroupID: "-100123",
	}, "新推文")
	if !errors.Is(err, ErrDeliveryTargetDisabled) {
		t.Fatalf("err = %v，应当是可跳过的「档案已停用」", err)
	}
}

// 仓库订阅的正文投递走的是另一个出口，同样要认得这件事。
func TestNotificationSenderReportsDisabledProfile(t *testing.T) {
	runtime := runtimeWithDisabledProfile(t, "telegram-bot")
	_, err := runtime.sendNotificationWithIDs(context.Background(), MessageEvent{
		ProfileID: "telegram-bot", Kind: EventKindGroup, GroupID: "-100123",
	}, "仓库有新提交")
	if !errors.Is(err, ErrDeliveryTargetDisabled) {
		t.Fatalf("err = %v，应当是可跳过的「档案已停用」", err)
	}
}

// 一条订阅同时投 QQ 群和 Telegram 群、其中一台停用时：停用那份跳过，整条订阅
// 不该被判成失败——线上就是这个形态，连续失败 6 次还在每 15 分钟重试。
func TestRSSFanoutSkipsDisabledTargetWithoutFailing(t *testing.T) {
	runtime := runtimeWithDisabledProfile(t, "telegram-bot")
	item := Reminder{
		ID: "watch-1", OwnerID: "owner", ProfileID: "qq-bot", Kind: ReminderKindRSSWatch,
		GroupID: "1081572710",
		NotificationTargetsJSON: encodeReminderDeliveryTargets([]ReminderDeliveryTarget{
			{ProfileID: "telegram-bot", Platform: PlatformTelegram, GroupID: "-1004402809405"},
		}),
	}
	err := runtime.sendRSSWatchTargets(context.Background(), item, "新推文")
	if errors.Is(err, ErrDeliveryTargetDisabled) {
		t.Fatalf("停用目标不该把整条订阅判成失败：%v", err)
	}
}

// 诊断消息也不该发给停用的机器人：发过去是一次失败投递，再由失败告警变成第二条
// 发不出去的消息。
func TestDiagnosticNoticeSkipsDisabledProfile(t *testing.T) {
	runtime := runtimeWithDisabledProfile(t, "telegram-bot")
	if runtime.diagnosticAllowed(MessageEvent{ProfileID: "telegram-bot"}, rssWatchPluginID) {
		t.Fatal("停用的机器人不该收到诊断消息")
	}
}
