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

// 群级覆盖跟着机器人走：新建机器人的 140 会带进群配置，群里填 0 仍然是关掉。
func TestGroupConfigInheritsForwardThresholdAndKeepsZeroOff(t *testing.T) {
	base := DefaultBotConfig().WithDefaults()
	group := DefaultGroupConfig("12345", base).WithDefaults("12345", base)
	if group.ForwardReplyThreshold != 140 {
		t.Fatalf("group inherited threshold=%d, want 140", group.ForwardReplyThreshold)
	}
	group.ForwardReplyThreshold = 0
	if got := group.WithDefaults("12345", base).ForwardReplyThreshold; got != 0 {
		t.Fatalf("group zero threshold=%d, want it to stay off", got)
	}
}
