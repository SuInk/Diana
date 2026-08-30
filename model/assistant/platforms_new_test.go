// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"testing"
)

func TestSupportedPlatformsCoverAllAdapters(t *testing.T) {
	want := map[string]bool{
		PlatformOneBotV11:  false,
		PlatformTelegram:   false,
		PlatformQQOfficial: false,
		PlatformDingTalk:   false,
		PlatformFeishu:     false,
		PlatformWeCom:      false,
	}
	for _, platform := range SupportedPlatforms() {
		if _, ok := want[platform.ID]; !ok {
			t.Fatalf("unexpected platform in the registry: %q", platform.ID)
		}
		want[platform.ID] = true
		if platform.Name == "" || platform.Protocol == "" || platform.CategoryLabel == "" {
			t.Fatalf("platform %q is missing display metadata", platform.ID)
		}
		// 回调型平台必须给出路径，WebUI 要靠它拼出填到对方后台的地址。
		if platform.Inbound == InboundCallback && platform.CallbackPath == "" {
			t.Fatalf("callback platform %q has no callback path", platform.ID)
		}
		if platform.Inbound != InboundCallback && platform.CallbackPath != "" {
			t.Fatalf("non-callback platform %q should not advertise a callback path", platform.ID)
		}
	}
	for id, found := range want {
		if !found {
			t.Fatalf("platform %q is missing from the registry", id)
		}
	}
}

func TestNormalizePlatformIDAcceptsCommonSpellings(t *testing.T) {
	cases := map[string]string{
		"":         PlatformOneBotV11,
		"onebot":   PlatformOneBotV11,
		"tg":       PlatformTelegram,
		"qqbot":    PlatformQQOfficial,
		"QQ Bot":   PlatformQQOfficial,
		"钉钉":       PlatformDingTalk,
		"DingTalk": PlatformDingTalk,
		"lark":     PlatformFeishu,
		"飞书":       PlatformFeishu,
		"wework":   PlatformWeCom,
		"企业微信":     PlatformWeCom,
	}
	for input, want := range cases {
		if got := NormalizePlatformID(input); got != want {
			t.Fatalf("NormalizePlatformID(%q) = %q, want %q", input, got, want)
		}
	}
}

// 只有飞书和企业微信需要公网回调；把出站平台误判成回调平台会让 WebUI 要求
// 用户去配一个根本用不上的地址。
func TestPlatformNeedsCallbackOnlyForWebhookPlatforms(t *testing.T) {
	for _, id := range []string{PlatformFeishu, PlatformWeCom} {
		if !PlatformNeedsCallback(id) {
			t.Fatalf("%q should require a public callback address", id)
		}
	}
	for _, id := range []string{PlatformOneBotV11, PlatformTelegram, PlatformQQOfficial, PlatformDingTalk} {
		if PlatformNeedsCallback(id) {
			t.Fatalf("%q should not require a public callback address", id)
		}
	}
}

func TestNewChannelForConfigBuildsEachPlatform(t *testing.T) {
	cases := []struct {
		platform string
		cfg      BotConfig
	}{
		{PlatformTelegram, BotConfig{TelegramBotToken: "t"}},
		{PlatformQQOfficial, BotConfig{QQAppID: "a", QQAppSecret: "s"}},
		{PlatformDingTalk, BotConfig{DingTalkClientID: "a", DingTalkClientSecret: "s"}},
		{PlatformFeishu, BotConfig{FeishuAppID: "a", FeishuAppSecret: "s"}},
		{PlatformWeCom, BotConfig{WeComCorpID: "c", WeComAgentID: "1", WeComSecret: "s"}},
	}
	for _, testCase := range cases {
		cfg := testCase.cfg
		cfg.Platform = testCase.platform
		if channel := NewChannelForConfig(cfg); channel == nil {
			t.Fatalf("no channel was built for platform %q", testCase.platform)
		}
	}
	// OneBot 是进程内共享的监听器，只能由调用方决定，工厂这里必须留空。
	if channel := NewChannelForConfig(BotConfig{Platform: PlatformOneBotV11}); channel != nil {
		t.Fatal("OneBot must be supplied by the caller, not built here")
	}
}

func TestValidateRequiresPerPlatformCredentials(t *testing.T) {
	cases := []struct {
		name string
		cfg  BotConfig
		want error
	}{
		{"qq without secret", BotConfig{Platform: PlatformQQOfficial, Enabled: true, QQAppID: "a"}, ErrMissingQQCredentials},
		{"dingtalk without secret", BotConfig{Platform: PlatformDingTalk, Enabled: true, DingTalkClientID: "a"}, ErrMissingDingTalkCredentials},
		{"feishu without secret", BotConfig{Platform: PlatformFeishu, Enabled: true, FeishuAppID: "a"}, ErrMissingFeishuCredentials},
		{"wecom without secret", BotConfig{Platform: PlatformWeCom, Enabled: true, WeComCorpID: "c", WeComAgentID: "1"}, ErrMissingWeComCredentials},
		{
			"wecom with non numeric agent",
			BotConfig{Platform: PlatformWeCom, Enabled: true, WeComCorpID: "c", WeComAgentID: "abc", WeComSecret: "s"},
			ErrInvalidWeComAgentID,
		},
		{
			"wecom without callback keys",
			BotConfig{Platform: PlatformWeCom, Enabled: true, WeComCorpID: "c", WeComAgentID: "1", WeComSecret: "s"},
			ErrMissingWeComCallbackKeys,
		},
		{
			"feishu with a bad api base",
			BotConfig{Platform: PlatformFeishu, Enabled: true, FeishuAppID: "a", FeishuAppSecret: "s", FeishuAPIBaseURL: "ftp://x"},
			ErrInvalidFeishuAPIBase,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.cfg.WithDefaults().Validate(); !errors.Is(err, testCase.want) {
				t.Fatalf("Validate() = %v, want %v", err, testCase.want)
			}
		})
	}
}

// 校验必须按平台分支。以前是「不是 OneBot 就当 Telegram」，那种写法会拿
// Telegram 的规则去要求飞书填 Bot Token。
func TestValidateDoesNotDemandTelegramTokenFromOtherPlatforms(t *testing.T) {
	valid := []BotConfig{
		{Platform: PlatformQQOfficial, Enabled: true, QQAppID: "a", QQAppSecret: "s"},
		{Platform: PlatformDingTalk, Enabled: true, DingTalkClientID: "a", DingTalkClientSecret: "s"},
		{Platform: PlatformFeishu, Enabled: true, FeishuAppID: "a", FeishuAppSecret: "s"},
		{
			Platform: PlatformWeCom, Enabled: true, WeComCorpID: "c", WeComAgentID: "1000002",
			WeComSecret: "s", WeComToken: "tok", WeComEncodingAESKey: "k",
		},
	}
	for _, cfg := range valid {
		if err := cfg.WithDefaults().Validate(); err != nil {
			t.Fatalf("platform %q with complete credentials failed validation: %v", cfg.Platform, err)
		}
	}
}

// 停用的机器人允许留着不完整的凭据，否则用户没法先建档再慢慢填。
func TestValidateSkipsCredentialsWhenDisabled(t *testing.T) {
	for _, platform := range []string{PlatformQQOfficial, PlatformDingTalk, PlatformFeishu, PlatformWeCom} {
		cfg := BotConfig{Platform: platform, Enabled: false}.WithDefaults()
		if err := cfg.Validate(); err != nil {
			t.Fatalf("disabled %q profile failed validation: %v", platform, err)
		}
	}
}

func TestPayloadFromConfigMasksNewPlatformSecrets(t *testing.T) {
	cfg := BotConfig{
		Platform:                PlatformWeCom,
		QQAppSecret:             "qq-secret",
		DingTalkClientSecret:    "ding-secret",
		FeishuAppSecret:         "feishu-secret",
		FeishuVerificationToken: "feishu-token",
		FeishuEncryptKey:        "feishu-key",
		WeComSecret:             "wecom-secret",
		WeComToken:              "wecom-token",
		WeComEncodingAESKey:     "wecom-aes",
	}
	payload := PayloadFromConfig(cfg)

	secrets := map[string]string{
		"qq app secret":      payload.QQAppSecret,
		"dingtalk secret":    payload.DingTalkClientSecret,
		"feishu app secret":  payload.FeishuAppSecret,
		"feishu token":       payload.FeishuVerificationToken,
		"feishu encrypt key": payload.FeishuEncryptKey,
		"wecom secret":       payload.WeComSecret,
		"wecom token":        payload.WeComToken,
		"wecom encoding key": payload.WeComEncodingAESKey,
	}
	for name, value := range secrets {
		if value != "" {
			t.Fatalf("%s leaked into the plain payload: %q", name, value)
		}
	}
	if !payload.WeComSecretConfigured || !payload.FeishuEncryptKeyConfigured || !payload.QQAppSecretConfigured {
		t.Fatal("configured flags should still tell the UI a secret is stored")
	}
	// 回调路径是公开信息，前端要靠它告诉用户该往对方后台填什么。
	if payload.CallbackPath != WeComCallbackPath {
		t.Fatalf("callback path = %q, want %q", payload.CallbackPath, WeComCallbackPath)
	}

	revealed := PayloadFromConfigWithSecrets(cfg)
	if revealed.WeComSecret != "wecom-secret" || revealed.FeishuEncryptKey != "feishu-key" {
		t.Fatal("explicit secret retrieval should return the stored values")
	}
}

// 前端为了不回显明文会把密钥字段留空，留空必须沿用旧值。漏掉任何一个，用户改
// 个无关设置就会把凭据清空，机器人随之掉线。
func TestConfigFromPayloadPreservesBlankSecrets(t *testing.T) {
	existing := BotConfig{
		Platform:                PlatformFeishu,
		QQAppSecret:             "qq-secret",
		DingTalkClientSecret:    "ding-secret",
		FeishuAppSecret:         "feishu-secret",
		FeishuVerificationToken: "feishu-token",
		FeishuEncryptKey:        "feishu-key",
		WeComSecret:             "wecom-secret",
		WeComToken:              "wecom-token",
		WeComEncodingAESKey:     "wecom-aes",
	}
	// 只改一个无关字段，所有密钥字段留空。
	payload := ConfigPayload{Platform: PlatformFeishu, Name: "改个名字"}
	merged := ConfigFromPayload(payload, existing)

	if merged.QQAppSecret != "qq-secret" ||
		merged.DingTalkClientSecret != "ding-secret" ||
		merged.FeishuAppSecret != "feishu-secret" ||
		merged.FeishuVerificationToken != "feishu-token" ||
		merged.FeishuEncryptKey != "feishu-key" ||
		merged.WeComSecret != "wecom-secret" ||
		merged.WeComToken != "wecom-token" ||
		merged.WeComEncodingAESKey != "wecom-aes" {
		t.Fatalf("blank secret fields wiped stored credentials: %+v", merged)
	}

	// 明确填了新值时当然要覆盖。
	payload.FeishuAppSecret = "rotated"
	if merged := ConfigFromPayload(payload, existing); merged.FeishuAppSecret != "rotated" {
		t.Fatalf("a supplied secret was not applied: %q", merged.FeishuAppSecret)
	}
}
