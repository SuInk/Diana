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
		GroupID: "20005",
		NotificationTargetsJSON: encodeReminderDeliveryTargets([]ReminderDeliveryTarget{
			{ProfileID: "telegram-bot", Platform: PlatformTelegram, GroupID: "-100200400"},
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

// 停用的机器人不该做任何后台活儿。纪念日问候要扫一遍用户表、再让模型写一段话，
// 发不出去还照样花钱——停用就该是安静的。
func TestRomanceGreetingSkipsDisabledProfile(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{
		{ID: "on", Enabled: true, RomanceEnabled: boolPtr(true)},
		{ID: "off", Enabled: false, RomanceEnabled: boolPtr(true)},
	}})
	configs := runtime.romanceEnabledConfigs()
	for _, cfg := range configs {
		if cfg.ID == "off" {
			t.Fatalf("停用的机器人不该参与纪念日问候：%#v", configs)
		}
	}
	if len(configs) != 1 || configs[0].ID != "on" {
		t.Fatalf("启用的那台该照常参与：%#v", configs)
	}
}

// 「停用」的直觉是这台机器人整个安静下来。插件的开关聚合点只有一个，停用档案
// 在这里一刀切：新加的插件不用做任何事就自动遵守。
func TestDisabledProfileDisablesEveryPlugin(t *testing.T) {
	plugins := NewDefaultPluginManager()
	runtime := NewRuntime(BotConfig{}, nilChannel{}, plugins, nil, nil, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{
		{ID: "on", Enabled: true},
		{ID: "off", Enabled: false},
	}})

	overrides := runtime.pluginOverridesForEvent(MessageEvent{ProfileID: "off"})
	if len(overrides) == 0 {
		t.Fatal("停用档案该拿到一张全关的覆盖表")
	}
	for id, enabled := range overrides {
		if enabled {
			t.Fatalf("插件 %s 在停用的机器人上仍然是启用的", id)
		}
	}
	// 启用的那台不受影响：这里不该把别人一起关掉。
	for id, enabled := range runtime.pluginOverridesForEvent(MessageEvent{ProfileID: "on"}) {
		if !enabled && id == rssWatchPluginID {
			t.Fatalf("启用的机器人不该被连坐关掉插件：%s", id)
		}
	}
}

// 记忆抽取要花一次模型调用，停用的机器人不该继续烧这个钱。
func TestMemoryExtractionSkipsDisabledProfile(t *testing.T) {
	memory := &testStructuredMemoryStore{}
	provider := &capturingLLMProvider{reply: `{"memories":[]}`}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetStructuredMemoryStore(memory)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{{ID: "off", Enabled: false}}})

	err := runtime.processEventMemoryJobs(context.Background(), memory, []MemoryJobPayload{{
		Kind: MemoryJobEvent, Session: "off:group:1", Event: MessageEvent{
			ProfileID: "off", Kind: EventKindGroup, GroupID: "1", UserID: "u", MessageID: "m1",
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "我养了只猫"}}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 0 {
		t.Fatalf("停用的机器人不该产生记忆抽取调用，实际 %d 次", provider.calls)
	}
}
