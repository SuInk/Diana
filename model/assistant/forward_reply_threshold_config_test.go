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

// 群级阈值留空跟随机器人：新建群配置不再抄一份机器人当时的值，机器人页后来
// 改了也照样生效；群里显式填 0 仍然是关掉，填别的数就用本群自己的。
func TestGroupConfigInheritsForwardThresholdAndKeepsZeroOff(t *testing.T) {
	base := DefaultBotConfig().WithDefaults()
	group := DefaultGroupConfig("12345", base).WithDefaults("12345", base)
	if group.ForwardReplyThreshold != nil || group.ForwardReplyChunkThreshold != nil {
		t.Fatalf("new group must follow the bot, got %v/%v", group.ForwardReplyThreshold, group.ForwardReplyChunkThreshold)
	}
	group.ForwardReplyThreshold = intPointer(0)
	if got := group.WithDefaults("12345", base).ForwardReplyThreshold; got == nil || *got != 0 {
		t.Fatalf("group zero threshold=%v, want it to stay off", got)
	}
	group.ForwardReplyThreshold = intPointer(-3)
	if got := group.WithDefaults("12345", base).ForwardReplyThreshold; got == nil || *got != 0 {
		t.Fatalf("negative group threshold=%v, want clamped to 0", got)
	}
}

// 线上报的「合并转发字数不生效」：旧群配置里这项是 0（int 的 omitempty 根本没
// 写进 JSON），机器人页填 140 后群里的长回复照样散装发出。读回来的旧配置要跟随
// 机器人，只有群里显式存了数才覆盖。
func TestGroupForwardThresholdFollowsBotUnlessOverridden(t *testing.T) {
	var legacy GroupConfig
	if err := json.Unmarshal([]byte(`{"group_id":"legacy","enabled":true}`), &legacy); err != nil {
		t.Fatal(err)
	}
	base := BotConfig{ForwardReplyThreshold: 140, ForwardReplyChunkThreshold: 4}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"legacy":   legacy,
		"off":      {GroupID: "off", ForwardReplyThreshold: intPointer(0), ForwardReplyChunkThreshold: intPointer(0)},
		"override": {GroupID: "override", ForwardReplyThreshold: intPointer(500)},
	}})
	for _, tc := range []struct {
		group         string
		chars, chunks int
	}{
		{"legacy", 140, 4},
		{"off", 0, 0},
		{"override", 500, 4},
	} {
		cfg := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, GroupID: tc.group})
		if cfg.ForwardReplyThreshold != tc.chars || cfg.ForwardReplyChunkThreshold != tc.chunks {
			t.Fatalf("group %s thresholds=%d/%d, want %d/%d", tc.group, cfg.ForwardReplyThreshold, cfg.ForwardReplyChunkThreshold, tc.chars, tc.chunks)
		}
	}

	// 显式 0 必须能存下来，不能被 omitempty 吃掉又变回跟随。
	raw, err := json.Marshal(GroupConfig{GroupID: "off", ForwardReplyThreshold: intPointer(0)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"forward_reply_threshold":0`) {
		t.Fatalf("explicit zero was dropped: %s", raw)
	}
}
