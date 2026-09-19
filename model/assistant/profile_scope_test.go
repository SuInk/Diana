// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

func twoBotRuntime(t *testing.T, a, b BotConfig) *Runtime {
	t.Helper()
	r := NewRuntime(a, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{a, b}})
	return r
}

// 模型调用的开关按调用所属的机器人取，不再读某一台「当前」机器人的。
func TestLLMSwitchesFollowTheCallingBot(t *testing.T) {
	a := BotConfig{ID: "a", LLMStreamingEnabled: boolPointer(false), LLMIdentityMaskingEnabled: boolPointer(true)}
	b := BotConfig{ID: "b", LLMStreamingEnabled: boolPointer(true), LLMIdentityMaskingEnabled: boolPointer(false)}
	r := twoBotRuntime(t, a, b)
	for id, want := range map[string]bool{"a": false, "b": true} {
		ctx := context.WithValue(context.Background(), modelProfileContextKey{}, id)
		if got := boolValue(r.configForContext(ctx).LLMStreamingEnabled, true); got != want {
			t.Fatalf("机器人 %s 的流式开关 = %v，想要 %v", id, got, want)
		}
		if got := llmIdentityMaskingEnabled(r.configForContext(ctx)); got == want {
			t.Fatalf("机器人 %s 的身份脱敏 = %v", id, got)
		}
	}
	// 多台机器人又不知道是哪一台时用默认配置，不拿任何一台充数。
	if cfg := r.configForContext(context.Background()); cfg.ID != "" {
		t.Fatalf("没有机器人归属的调用取到了机器人 %q 的配置", cfg.ID)
	}
}

// 共用的入站并发取各台启用机器人里最大的设置，不随某一台变化。
func TestInboundConcurrencyUsesTheLargestEnabledBot(t *testing.T) {
	r := twoBotRuntime(t,
		BotConfig{ID: "a", Enabled: true, InboundGroupConcurrency: 2, InboundPrivateConcurrency: 9},
		BotConfig{ID: "b", Enabled: true, InboundGroupConcurrency: 6, InboundPrivateConcurrency: 1},
	)
	if got := r.inboundConcurrency(); got.Group != 6 || got.Private != 9 {
		t.Fatalf("并发上限 = %+v", got)
	}
}

// NoneBot 桥接按机器人各建一个，停用或关掉桥接的机器人没有桥接。
func TestNoneBotBridgesArePerBot(t *testing.T) {
	bridge := func(id string, enabled, bridgeOn bool) BotConfig {
		return BotConfig{ID: id, Enabled: enabled, NoneBotBridgeEnabled: bridgeOn, NoneBotBridgeEndpoint: "ws://127.0.0.1:1/" + id}
	}
	r := NewRuntime(bridge("a", true, true), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{bridge("a", true, true), bridge("b", true, false), bridge("c", false, true)}})
	statuses := r.bridgeStatuses()
	if _, ok := statuses["a"]; !ok || len(statuses) != 1 {
		t.Fatalf("桥接 = %+v，想要只有 a", statuses)
	}
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{bridge("a", true, false), bridge("b", true, true)}})
	if statuses = r.bridgeStatuses(); len(statuses) != 1 || statuses["b"].Endpoint != "ws://127.0.0.1:1/b" {
		t.Fatalf("改配置后的桥接 = %+v", statuses)
	}
}

// 运行时只要有一台启用的机器人就能启动；一台都没启用时报 ErrBotDisabled。
func TestStartNeedsAnyEnabledBot(t *testing.T) {
	off := twoBotRuntime(t, BotConfig{ID: "a"}, BotConfig{ID: "b"})
	if err := off.Start(context.Background()); err != ErrBotDisabled {
		t.Fatalf("没有启用的机器人时 err = %v", err)
	}
}
