// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func TestNormalizePlatformIDMigratesLegacyNames(t *testing.T) {
	cases := map[string]string{
		"":                    PlatformOneBotV11,
		"OneBot v11":          PlatformOneBotV11,
		"onebot-v11":          PlatformOneBotV11,
		"NapCat / OneBot V11": PlatformOneBotV11,
		"Lagrange.Core":       PlatformOneBotV11,
		"go-cqhttp":           PlatformOneBotV11,
		"Telegram":            PlatformTelegram,
		"tg":                  PlatformTelegram,
	}
	for input, want := range cases {
		if got := NormalizePlatformID(input); got != want {
			t.Fatalf("NormalizePlatformID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLegacyOneBotPlatformPersistsAsUnifiedID(t *testing.T) {
	for _, legacyID := range []string{"napcat", "lagrange", "go-cqhttp"} {
		cfg := BotConfig{Platform: legacyID}.WithDefaults()
		if cfg.Platform != PlatformOneBotV11 {
			t.Fatalf("WithDefaults(%q) platform = %q", legacyID, cfg.Platform)
		}
		if payload := PayloadFromConfig(BotConfig{Platform: legacyID}); payload.Platform != PlatformOneBotV11 {
			t.Fatalf("PayloadFromConfig(%q) platform = %q", legacyID, payload.Platform)
		}
	}
}

func TestValidatePlatformRejectsUnknownAdapter(t *testing.T) {
	if err := ValidatePlatform(PlatformOneBotV11); err != nil {
		t.Fatalf("ValidatePlatform(%q) error = %v", PlatformOneBotV11, err)
	}
	if err := ValidatePlatform("lagrange"); err != nil {
		t.Fatalf("legacy platform should remain valid: %v", err)
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
	if len(platforms) != 2 {
		t.Fatalf("SupportedPlatforms() len = %d, want OneBot v11 and Telegram", len(platforms))
	}
	byID := map[string]PlatformDefinition{}
	for _, p := range platforms {
		byID[p.ID] = p
	}
	if byID[PlatformOneBotV11].Category != PlatformCategoryQQ {
		t.Fatalf("%s 应属于 QQ 分类，实际 %q", PlatformOneBotV11, byID[PlatformOneBotV11].Category)
	}
	if !IsOneBotPlatform(PlatformOneBotV11) {
		t.Fatalf("%s 应走 OneBot 适配器", PlatformOneBotV11)
	}
	for _, legacyID := range []string{"napcat", "lagrange", "go-cqhttp"} {
		if _, exposed := byID[legacyID]; exposed {
			t.Fatalf("旧实现 ID %q 不应继续作为独立平台暴露", legacyID)
		}
		if !IsOneBotPlatform(legacyID) {
			t.Fatalf("旧实现 ID %q 应兼容迁移到 OneBot v11", legacyID)
		}
	}
	if byID[PlatformTelegram].Category != PlatformCategoryTelegram {
		t.Fatal("Telegram 应自成一个分类")
	}
	if IsOneBotPlatform(PlatformTelegram) {
		t.Fatal("Telegram 不该走 OneBot 适配器")
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

func TestProfileSetPlatformContextIsolationDefaultsOnAndCanBeDisabled(t *testing.T) {
	set := ProfileSet{Profiles: []BotConfig{{ID: "qq", Platform: PlatformOneBotV11}}}.WithDefaults()
	if !set.PlatformContextsIsolated() {
		t.Fatal("legacy profile sets must default to isolated contexts")
	}
	payload := PayloadFromProfileSet(set)
	if payload.IsolatePlatformContexts == nil || !*payload.IsolatePlatformContexts {
		t.Fatalf("payload isolation=%#v, want true", payload.IsolatePlatformContexts)
	}
	set = set.WithPlatformContextIsolation(false)
	if set.PlatformContextsIsolated() {
		t.Fatal("context isolation should be disabled")
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
