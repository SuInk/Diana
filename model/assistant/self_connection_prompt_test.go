// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 10-31 一台 QQ 官方机器人被问「你是怎么跑起来的」，说自己靠 SnowLuma 反向 WebSocket
// 连 QQ。系统提示词要常驻写明它自己的接入方式，OneBot 机器人照旧是 SnowLuma。
func TestSystemPromptStatesOwnConnection(t *testing.T) {
	cases := []struct {
		platform string
		want     string
		reject   string
	}{
		{PlatformQQOfficial, "你这台机器人是 QQ 官方机器人", "推荐 SnowLuma"},
		{PlatformOneBotV11, "你这台机器人是 QQ 个人号，经 OneBot v11 协议端（推荐 SnowLuma）", "官方机器人"},
		{"", "你这台机器人是 QQ 个人号", "官方机器人"},
		{PlatformTelegram, "你这台机器人在Telegram上", "SnowLuma"},
	}
	for _, tc := range cases {
		cfg := BotConfig{Platform: tc.platform}.WithDefaults()
		runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		relationship := RelationshipPolicyFor(UserMemoryProfile{}, cfg.OwnerID, "1")
		event := MessageEvent{Platform: tc.platform, Kind: EventKindGroup, GroupID: "10001", UserID: "1", RawMessage: "你是怎么跑起来的"}
		prompt := runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false, relationship, true, nil)
		start := strings.Index(prompt, "关于你自己：")
		if start < 0 || !strings.Contains(prompt[start:], tc.want) {
			t.Fatalf("%q: prompt is missing own connection %q", tc.platform, tc.want)
		}
		line := prompt[start:]
		if end := strings.Index(line, "\n"); end >= 0 {
			line = line[:end]
		}
		if strings.Contains(line, tc.reject) {
			t.Fatalf("%q: connection line should not mention %q: %q", tc.platform, tc.reject, line)
		}
		if !strings.Contains(line, "不要编一个别的接入方式") {
			t.Fatalf("%q: connection line should forbid inventing one: %q", tc.platform, line)
		}
	}
}

func TestSelfConnectionFactsDoNotNameOtherQQClients(t *testing.T) {
	for _, platform := range SupportedPlatforms() {
		lower := strings.ToLower(selfConnectionFact(platform.ID))
		if lower == "" {
			t.Fatalf("%s has no connection fact", platform.ID)
		}
		for _, name := range []string{"napcat", "llonebot", "lagrange", "go-cqhttp"} {
			if strings.Contains(lower, name) {
				t.Fatalf("%s names %s", platform.ID, name)
			}
		}
	}
	if got := selfConnectionFact(""); got != "" {
		t.Fatalf("unknown platform should not guess, got %q", got)
	}
}
