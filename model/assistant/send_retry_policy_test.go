// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// 没配过的机器人和改成可配置之前的常量完全一致，升级不改变行为。
func TestSendRetrySettingsDefaultsMatchPreviousConstants(t *testing.T) {
	cfg := DefaultBotConfig().WithDefaults()
	policy := cfg.sendRetrySettings.outboundDeliveryPolicy()
	if policy != defaultOutboundDeliveryPolicy() {
		t.Fatalf("默认退避策略变了：%+v", policy)
	}
	if got := cfg.sendRetrySettings.inboundRetryMaxAttempts(); got != inboundMaxAttempts {
		t.Fatalf("默认重跑上限 = %d，want %d", got, inboundMaxAttempts)
	}
}

// 嵌入的字段要能从配置 JSON 里读出来，也要能写回去，否则界面上存了等于没存。
func TestSendRetrySettingsJSONRoundTrip(t *testing.T) {
	var cfg BotConfig
	raw := `{"send_backoff_initial_seconds":10,"send_backoff_max_seconds":120,"send_failure_window_minutes":5,"send_drop_cooldown_minutes":2,"inbound_retry_max_attempts":2}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	want := sendRetrySettings{SendBackoffInitialSeconds: 10, SendBackoffMaxSeconds: 120, SendFailureWindowMinutes: 5, SendDropCooldownMinutes: 2, InboundRetryMaxAttempts: 2}
	if cfg.sendRetrySettings != want {
		t.Fatalf("读出来 %+v，want %+v", cfg.sendRetrySettings, want)
	}
	payload := PayloadFromConfig(cfg.WithDefaults())
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var back ConfigPayload
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if got := ConfigFromPayload(back, BotConfig{}).sendRetrySettings; got != want {
		t.Fatalf("经过 payload 往返后 %+v，want %+v", got, want)
	}
}

func TestSendRetrySettingsClampOutOfRange(t *testing.T) {
	cfg := BotConfig{sendRetrySettings: sendRetrySettings{
		SendBackoffInitialSeconds: 1,
		SendBackoffMaxSeconds:     99999,
		SendFailureWindowMinutes:  99999,
		SendDropCooldownMinutes:   -3,
		InboundRetryMaxAttempts:   100,
	}}.WithDefaults()
	got := cfg.sendRetrySettings
	if got.SendBackoffInitialSeconds != minSendBackoffSeconds || got.SendBackoffMaxSeconds != maxSendBackoffSeconds ||
		got.SendFailureWindowMinutes != maxSendFailureWindowMinutes || got.SendDropCooldownMinutes != defaultSendDropCooldownMinutes ||
		got.InboundRetryMaxAttempts != maxInboundRetryMaxAttempts {
		t.Fatalf("越界值没收回范围：%+v", got)
	}
}

// 分群只覆盖填了的那几项，没填的跟随机器人当前的值。
func TestGroupSendRetrySettingsOverrideOnlyFilledFields(t *testing.T) {
	base := BotConfig{ID: "bot-a", sendRetrySettings: sendRetrySettings{SendBackoffMaxSeconds: 300, InboundRetryMaxAttempts: 3}}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(GroupConfigSet{Groups: []GroupConfig{{
		BotProfileID:      "bot-a",
		GroupID:           "g",
		sendRetrySettings: sendRetrySettings{SendBackoffInitialSeconds: 10, InboundRetryMaxAttempts: 1},
	}}})
	group := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-a", GroupID: "g"}
	policy := runtime.outboundDeliveryPolicyForEvent(context.Background(), group)
	want := outboundDeliveryPolicy{InitialDelay: 10 * time.Second, MaximumDelay: 5 * time.Minute, FailureWindow: defaultOutboundFailureWindow, DropCooldown: defaultOutboundDropCooldown}
	if policy != want {
		t.Fatalf("群策略 %+v，want %+v", policy, want)
	}
	if got := runtime.inboundRetryMaxAttemptsForEvent(group); got != 1 {
		t.Fatalf("群重跑上限 = %d，want 1", got)
	}
	private := MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-a", UserID: "u"}
	if got := runtime.inboundRetryMaxAttemptsForEvent(private); got != 3 {
		t.Fatalf("私聊重跑上限 = %d，want 机器人级的 3", got)
	}
	other := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-a", GroupID: "other"}
	if got := runtime.outboundDeliveryPolicyForEvent(context.Background(), other).InitialDelay; got != defaultOutboundInitialDelay {
		t.Fatalf("没有分群配置的群初始间隔 = %v，want 机器人级默认", got)
	}
}

// ctx 里显式给的策略优先：测试和特殊调用方靠它把分钟级退避压到毫秒级。
func TestContextOutboundPolicyWinsOverConfig(t *testing.T) {
	base := BotConfig{ID: "bot-a", sendRetrySettings: sendRetrySettings{SendBackoffInitialSeconds: 30}}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	ctx := withOutboundDeliveryPolicy(context.Background(), outboundDeliveryPolicy{InitialDelay: time.Millisecond})
	got := runtime.outboundDeliveryPolicyForEvent(ctx, MessageEvent{Kind: EventKindGroup, ProfileID: "bot-a", GroupID: "g"})
	if got.InitialDelay != time.Millisecond {
		t.Fatalf("ctx 策略被配置盖掉了：%+v", got)
	}
}

// 分群的越界值不管这个群设没设过启用状态都要收回范围，0 保留为「跟随机器人」。
func TestGroupSendRetrySettingsAreClamped(t *testing.T) {
	for _, enabledSet := range []bool{false, true} {
		cfg := GroupConfig{GroupID: "g", Enabled: true, EnabledSet: enabledSet, sendRetrySettings: sendRetrySettings{
			SendBackoffInitialSeconds: 1,
			InboundRetryMaxAttempts:   100,
		}}.WithDefaults("g", DefaultBotConfig().WithDefaults())
		got := cfg.sendRetrySettings
		if got.SendBackoffInitialSeconds != minSendBackoffSeconds || got.InboundRetryMaxAttempts != maxInboundRetryMaxAttempts {
			t.Fatalf("enabledSet=%v: out-of-range group values not clamped: %+v", enabledSet, got)
		}
		if got.SendBackoffMaxSeconds != 0 || got.SendFailureWindowMinutes != 0 {
			t.Fatalf("enabledSet=%v: unset group values should stay 0 (inherit): %+v", enabledSet, got)
		}
	}
}
