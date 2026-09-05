// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"testing"
)

// 归一化本身是当前功能：配置、路由和存储都靠它把写法收敛到注册表里的值。
func TestNormalizePlatformIDCollapsesSpellingVariants(t *testing.T) {
	cases := map[string]string{
		"":           PlatformOneBotV11,
		"  ":         PlatformOneBotV11,
		"OneBot v11": PlatformOneBotV11,
		"onebot-v11": PlatformOneBotV11,
		"ONEBOT":     PlatformOneBotV11,
		"Telegram":   PlatformTelegram,
		"tg":         PlatformTelegram,
		"discord":    "discord",
	}
	for input, want := range cases {
		if got := NormalizePlatformID(input); got != want {
			t.Fatalf("NormalizePlatformID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidatePlatformRejectsUnknownAdapter(t *testing.T) {
	if err := ValidatePlatform(PlatformOneBotV11); err != nil {
		t.Fatalf("ValidatePlatform(%q) error = %v", PlatformOneBotV11, err)
	}
	if err := ValidatePlatform(PlatformTelegram); err != nil {
		t.Fatalf("ValidatePlatform(%q) error = %v", PlatformTelegram, err)
	}
	if err := ValidatePlatform("discord"); err == nil {
		t.Fatal("ValidatePlatform(discord) unexpectedly succeeded")
	}
}

func TestPlatformCategories(t *testing.T) {
	platforms := SupportedPlatforms()
	byID := map[string]PlatformDefinition{}
	for _, p := range platforms {
		byID[p.ID] = p
	}
	if byID[PlatformOneBotV11].Category != PlatformCategoryOneBotV11 {
		t.Fatalf("%s 应属于 OneBot v11 分类，实际 %q", PlatformOneBotV11, byID[PlatformOneBotV11].Category)
	}
	if !IsOneBotPlatform(PlatformOneBotV11) {
		t.Fatalf("%s 应走 OneBot 适配器", PlatformOneBotV11)
	}
	if byID[PlatformTelegram].Category != PlatformCategoryTelegram {
		t.Fatal("Telegram 应自成一个分类")
	}
	if IsOneBotPlatform(PlatformTelegram) {
		t.Fatal("Telegram 不该走 OneBot 适配器")
	}
	// 每个平台自成一个分类，WebUI 靠它给机器人列表分组；重名会让两个平台
	// 的机器人挤进同一组里。
	seenCategories := map[string]string{}
	for _, platform := range platforms {
		if owner, taken := seenCategories[platform.Category]; taken {
			t.Fatalf("分类 %q 同时被 %q 和 %q 占用", platform.Category, owner, platform.ID)
		}
		seenCategories[platform.Category] = platform.ID
		if IsOneBotPlatform(platform.ID) != (platform.ID == PlatformOneBotV11) {
			t.Fatalf("%q 的 OneBot 适配器判断不正确", platform.ID)
		}
	}
}

// Telegram 没有回连地址可填，校验必须走它自己的凭据。
func TestValidateTelegramConfig(t *testing.T) {
	enabled := BotConfig{Platform: PlatformTelegram, Enabled: true}
	if err := enabled.Validate(); err != ErrMissingTelegramToken {
		t.Fatalf("启用但没有 token 应报错，实际 %v", err)
	}

	ok := BotConfig{Platform: PlatformTelegram, Enabled: true, TelegramBotToken: "123:abc"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("有 token 时应通过，实际 %v", err)
	}

	badBase := ok
	badBase.TelegramAPIBaseURL = "ws://nope"
	if err := badBase.Validate(); err != ErrInvalidTelegramAPIBase {
		t.Fatalf("自建地址必须是 http(s)，实际 %v", err)
	}

	// OneBot 平台的回连地址要求不该被 Telegram 的分支影响。
	onebot := BotConfig{Platform: PlatformOneBotV11, Enabled: true}
	if err := onebot.Validate(); err != ErrMissingOneBotEndpoint {
		t.Fatalf("OneBot 启用时仍需回连地址，实际 %v", err)
	}
}

// 保存时留空 token 表示沿用旧值，不该把已配置的凭据抹掉。
func TestTelegramTokenKeptWhenPayloadOmitsIt(t *testing.T) {
	existing := BotConfig{Platform: PlatformTelegram, TelegramBotToken: "keep-me"}
	payload := PayloadFromConfig(existing)
	if payload.TelegramBotToken != "" {
		t.Fatal("读接口不该回传 token 明文")
	}
	if !payload.TelegramBotTokenConfigured {
		t.Fatal("应标记为已配置")
	}
	restored := ConfigFromPayload(payload, existing)
	if restored.TelegramBotToken != "keep-me" {
		t.Fatalf("留空提交应沿用旧 token，实际 %q", restored.TelegramBotToken)
	}
}

func TestOwnerLLMConfigSettingRoundTripsThroughPayload(t *testing.T) {
	disabled := false
	existing := BotConfig{OwnerLLMConfigEnabled: &disabled}
	payload := PayloadFromConfig(existing)
	if payload.OwnerLLMConfigEnabled == nil || *payload.OwnerLLMConfigEnabled {
		t.Fatalf("payload setting = %#v, want false", payload.OwnerLLMConfigEnabled)
	}
	restored := ConfigFromPayload(payload, existing)
	if restored.OwnerLLMConfigEnabled == nil || *restored.OwnerLLMConfigEnabled {
		t.Fatalf("restored setting = %#v, want false", restored.OwnerLLMConfigEnabled)
	}
}

func TestBotReplyLoopDetectionSettingDefaultsAndRoundTripsThroughPayload(t *testing.T) {
	defaultPayload := PayloadFromConfig(BotConfig{})
	if defaultPayload.BotReplyLoopDetectionEnabled == nil || !*defaultPayload.BotReplyLoopDetectionEnabled {
		t.Fatalf("default setting = %#v, want true", defaultPayload.BotReplyLoopDetectionEnabled)
	}

	disabled := false
	existing := BotConfig{BotReplyLoopDetectionEnabled: &disabled}
	payload := PayloadFromConfig(existing)
	if payload.BotReplyLoopDetectionEnabled == nil || *payload.BotReplyLoopDetectionEnabled {
		t.Fatalf("payload setting = %#v, want false", payload.BotReplyLoopDetectionEnabled)
	}
	restored := ConfigFromPayload(payload, existing)
	if restored.BotReplyLoopDetectionEnabled == nil || *restored.BotReplyLoopDetectionEnabled {
		t.Fatalf("restored setting = %#v, want false", restored.BotReplyLoopDetectionEnabled)
	}
}

func TestTelegramBotMessageSuppressionDefaultsAndRoundTripsThroughPayload(t *testing.T) {
	defaultPayload := PayloadFromConfig(BotConfig{})
	if defaultPayload.TelegramSuppressBotMessages == nil || !*defaultPayload.TelegramSuppressBotMessages {
		t.Fatalf("default setting = %#v, want true", defaultPayload.TelegramSuppressBotMessages)
	}

	disabled := false
	existing := BotConfig{TelegramSuppressBotMessages: &disabled}
	payload := PayloadFromConfig(existing)
	if payload.TelegramSuppressBotMessages == nil || *payload.TelegramSuppressBotMessages {
		t.Fatalf("payload setting = %#v, want false", payload.TelegramSuppressBotMessages)
	}
	restored := ConfigFromPayload(payload, existing)
	if restored.TelegramSuppressBotMessages == nil || *restored.TelegramSuppressBotMessages {
		t.Fatalf("restored setting = %#v, want false", restored.TelegramSuppressBotMessages)
	}
}

func TestProfileSetIgnoresLegacyContextIsolationSetting(t *testing.T) {
	for _, setting := range []any{nil, true, false} {
		legacy := map[string]any{
			"active_id": "qq",
			"profiles":  []BotConfig{{ID: "qq", Platform: PlatformOneBotV11}},
		}
		if setting != nil {
			legacy["isolate_platform_contexts"] = setting
		}
		data, err := json.Marshal(legacy)
		if err != nil {
			t.Fatal(err)
		}
		var set ProfileSet
		if err := json.Unmarshal(data, &set); err != nil {
			t.Fatal(err)
		}
		set = set.WithDefaults()
		if set.ActiveID != "qq" || len(set.Profiles) != 1 || set.Profiles[0].ID != "qq" {
			t.Fatalf("legacy setting %v changed profile identity: %#v", setting, set)
		}
		for _, value := range []any{set, PayloadFromProfileSet(set)} {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if _, exists := fields["isolate_platform_contexts"]; exists {
				t.Fatalf("removed setting is still serialized for %T", value)
			}
		}
	}
}

func TestProfileSetRuntimeConfigKeepsOtherEnabledChannelOnline(t *testing.T) {
	set := ProfileSet{
		ActiveID: "disabled",
		Profiles: []BotConfig{
			{ID: "disabled", Platform: PlatformOneBotV11, Enabled: false},
			{ID: "telegram", Platform: PlatformTelegram, Enabled: true, TelegramBotToken: "token"},
		},
	}
	cfg, ok := set.RuntimeConfig()
	if !ok || cfg.ID != "telegram" || !cfg.Enabled {
		t.Fatalf("runtime config=%#v ok=%v", cfg, ok)
	}
}
