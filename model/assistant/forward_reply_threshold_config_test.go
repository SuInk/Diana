// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

// 新建 OneBot 机器人默认 140 字走合并转发卡片。
func TestNewProfileDefaultsToForwardingAt140Chars(t *testing.T) {
	cfg := DefaultBotConfig().WithDefaults()
	if cfg.ForwardReplyThreshold != 140 {
		t.Fatalf("default forward threshold=%d, want 140", cfg.ForwardReplyThreshold)
	}
	long := strings.Repeat("字", 141)
	if !shouldUseForwardReply(long, []string{long}, cfg.ForwardReplyThreshold, cfg.ForwardReplyChunkThreshold) {
		t.Fatal("141 chars must go through a forward card by default")
	}
	short := strings.Repeat("字", 140)
	if shouldUseForwardReply(short, []string{short}, cfg.ForwardReplyThreshold, cfg.ForwardReplyChunkThreshold) {
		t.Fatal("140 chars is still at the threshold and must stay an ordinary message")
	}
}

// 0 是「关掉这条触发」，不是「没填」：清空输入框后不能被默认值顶回去，已经在跑
// 的部署（存量配置里这两项本来就是 0）升级后也不该凭空多出转发卡片。
func TestZeroForwardThresholdsStayOffAcrossSaveAndReload(t *testing.T) {
	cfg := BotConfig{ForwardReplyThreshold: 0, ForwardReplyChunkThreshold: 0}.WithDefaults()
	if cfg.ForwardReplyThreshold != 0 || cfg.ForwardReplyChunkThreshold != 0 {
		t.Fatalf("zero thresholds must stay off, got %d/%d", cfg.ForwardReplyThreshold, cfg.ForwardReplyChunkThreshold)
	}
	long := strings.Repeat("字", 5000)
	if shouldUseForwardReply(long, []string{long, long, long}, cfg.ForwardReplyThreshold, cfg.ForwardReplyChunkThreshold) {
		t.Fatal("both conditions are off, nothing should trigger a forward card")
	}

	// 控制台保存走 payload 往返，留空同样落成 0，而不是回落成 140。
	raw, err := json.Marshal(PayloadFromConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var payload ConfigPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	reloaded := ConfigFromPayload(payload, DefaultBotConfig()).WithDefaults()
	if reloaded.ForwardReplyThreshold != 0 || reloaded.ForwardReplyChunkThreshold != 0 {
		t.Fatalf("round trip resurrected thresholds: %d/%d", reloaded.ForwardReplyThreshold, reloaded.ForwardReplyChunkThreshold)
	}

	// 显式填的值照旧原样保留。
	if got := (BotConfig{ForwardReplyThreshold: 900}).WithDefaults().ForwardReplyThreshold; got != 900 {
		t.Fatalf("explicit threshold=%d, want 900", got)
	}
}

// 群级设置留空跟随机器人：新建群配置不再抄一份机器人当时的值，机器人页后来
// 改了也照样生效。阈值填 0 或负数都当没填，关闭只认显式开关。
func TestGroupConfigInheritsForwardSettings(t *testing.T) {
	base := DefaultBotConfig().WithDefaults()
	group := DefaultGroupConfig("12345", base).WithDefaults("12345", base)
	if group.ForwardReplyEnabled != nil || group.ForwardReplyThreshold != nil || group.ForwardReplyChunkThreshold != nil {
		t.Fatalf("new group must follow the bot, got %v/%v/%v", group.ForwardReplyEnabled, group.ForwardReplyThreshold, group.ForwardReplyChunkThreshold)
	}
	for _, value := range []int{0, -3} {
		group.ForwardReplyThreshold = intPointer(value)
		if got := group.WithDefaults("12345", base).ForwardReplyThreshold; got != nil {
			t.Fatalf("group threshold %d normalized to %v, want follow the bot", value, *got)
		}
	}
}

// 线上报的「合并转发字数不生效」：旧群配置里这项是 0（int 的 omitempty 根本没
// 写进 JSON），机器人页填 140 后群里的长回复照样散装发出。读回来的旧配置要跟随
// 机器人；本群关闭走显式开关，本群单独设置时没填的阈值仍跟随机器人。
func TestGroupForwardSettingsFollowBotUnlessOverridden(t *testing.T) {
	var legacy GroupConfig
	if err := json.Unmarshal([]byte(`{"group_id":"legacy","enabled":true}`), &legacy); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("字", 300)
	base := BotConfig{ForwardReplyThreshold: 140, ForwardReplyChunkThreshold: 4}.WithDefaults()
	botOff := base
	botOff.ForwardReplyEnabled = boolPointer(false)
	for _, tc := range []struct {
		name          string
		bot           BotConfig
		group         GroupConfig
		chars, chunks int
		forward       bool
	}{
		{"legacy group follows bot", base, legacy, 140, 4, true},
		{"group off", base, GroupConfig{ForwardReplyEnabled: boolPointer(false)}, 140, 4, false},
		{"group override", base, GroupConfig{ForwardReplyEnabled: boolPointer(true), ForwardReplyThreshold: intPointer(500)}, 500, 4, false},
		{"group on while bot off", botOff, GroupConfig{ForwardReplyEnabled: boolPointer(true)}, 140, 4, true},
		{"bot off reaches group", botOff, GroupConfig{}, 140, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.group.GroupID = "g"
			runtime := NewRuntime(tc.bot, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"g": tc.group}})
			cfg := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: "g"})
			if cfg.ForwardReplyThreshold != tc.chars || cfg.ForwardReplyChunkThreshold != tc.chunks {
				t.Fatalf("thresholds=%d/%d, want %d/%d", cfg.ForwardReplyThreshold, cfg.ForwardReplyChunkThreshold, tc.chars, tc.chunks)
			}
			if got := shouldUseForwardReplyFor(cfg, long, []string{long}); got != tc.forward {
				t.Fatalf("forward=%v, want %v", got, tc.forward)
			}
		})
	}

	// 本群关闭必须能存下来，不能被 omitempty 吃掉又变回跟随。
	raw, err := json.Marshal(GroupConfig{GroupID: "off", ForwardReplyEnabled: boolPointer(false)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"forward_reply_enabled":false`) {
		t.Fatalf("explicit off was dropped: %s", raw)
	}
}

// 机器人级总开关：旧配置没有这个字段，按阈值推出实际状态；显式关闭后阈值保留但
// 不触发，保存往返也不丢。
func TestBotForwardSwitch(t *testing.T) {
	if got := (BotConfig{}).WithDefaults().ForwardReplyEnabled; got == nil || *got {
		t.Fatalf("legacy bot without thresholds must read as off, got %v", got)
	}
	if got := (BotConfig{ForwardReplyThreshold: 300}).WithDefaults().ForwardReplyEnabled; got == nil || !*got {
		t.Fatalf("legacy bot with a threshold must read as on, got %v", got)
	}
	if got := DefaultBotConfig().WithDefaults().ForwardReplyEnabled; got == nil || !*got {
		t.Fatal("new bots start with forward cards on")
	}

	off := BotConfig{ForwardReplyThreshold: 140, ForwardReplyEnabled: boolPointer(false)}.WithDefaults()
	long := strings.Repeat("字", 300)
	if shouldUseForwardReplyFor(off, long, []string{long}) {
		t.Fatal("switched-off bot still sent a forward card")
	}
	raw, err := json.Marshal(PayloadFromConfig(off))
	if err != nil {
		t.Fatal(err)
	}
	var payload ConfigPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	reloaded := ConfigFromPayload(payload, DefaultBotConfig()).WithDefaults()
	if boolValue(reloaded.ForwardReplyEnabled, true) || reloaded.ForwardReplyThreshold != 140 {
		t.Fatalf("round trip lost the switch or threshold: %v/%d", reloaded.ForwardReplyEnabled, reloaded.ForwardReplyThreshold)
	}
}
