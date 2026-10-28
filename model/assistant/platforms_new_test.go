// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSupportedPlatformsCoverAllAdapters(t *testing.T) {
	want := map[string]bool{
		PlatformOneBotV11:  false,
		PlatformTelegram:   false,
		PlatformQQOfficial: false,
		PlatformWeixin:     false,
		PlatformIMessage:   false,
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
		"":       PlatformOneBotV11,
		"onebot": PlatformOneBotV11,
		"tg":     PlatformTelegram,
		"qqbot":  PlatformQQOfficial,
		"QQ Bot": PlatformQQOfficial,
		"wechat": PlatformWeixin,
		"微信":     PlatformWeixin,
	}
	cases["BlueBubbles"] = PlatformIMessage
	cases["iMessage"] = PlatformIMessage
	for input, want := range cases {
		if got := NormalizePlatformID(input); got != want {
			t.Fatalf("NormalizePlatformID(%q) = %q, want %q", input, got, want)
		}
	}
}

// 只有 iMessage 需要公网回调；把出站平台误判成回调平台会让 WebUI 要求
// 用户去配一个根本用不上的地址。
func TestPlatformNeedsCallbackOnlyForWebhookPlatforms(t *testing.T) {
	for _, id := range []string{PlatformIMessage} {
		if !PlatformNeedsCallback(id) {
			t.Fatalf("%q should require a public callback address", id)
		}
	}
	for _, id := range []string{PlatformOneBotV11, PlatformTelegram, PlatformQQOfficial, PlatformWeixin} {
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
		{PlatformWeixin, BotConfig{}},
		{PlatformIMessage, BotConfig{IMessageServerURL: "http://mac.local:1234", IMessagePassword: "p"}},
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
		{"imessage without password", BotConfig{Platform: PlatformIMessage, Enabled: true, IMessageServerURL: "http://mac.local:1234"}, ErrMissingIMessageCredentials},
		{
			"imessage with a bad server url",
			BotConfig{Platform: PlatformIMessage, Enabled: true, IMessageServerURL: "mac.local:1234", IMessagePassword: "p"},
			ErrInvalidIMessageServerURL,
		},
		{
			"imessage polling too often",
			BotConfig{Platform: PlatformIMessage, Enabled: true, IMessageServerURL: "http://mac.local:1234", IMessagePassword: "p", IMessagePollSeconds: 1},
			ErrInvalidIMessagePoll,
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
// Telegram 的规则去要求别的平台填 Bot Token。
func TestValidateDoesNotDemandTelegramTokenFromOtherPlatforms(t *testing.T) {
	valid := []BotConfig{
		{Platform: PlatformQQOfficial, Enabled: true, QQAppID: "a", QQAppSecret: "s"},
		{Platform: PlatformIMessage, Enabled: true, IMessageServerURL: "https://mac.example:1234", IMessagePassword: "p", IMessagePollSeconds: 30},
	}
	for _, cfg := range valid {
		if err := cfg.WithDefaults().Validate(); err != nil {
			t.Fatalf("platform %q with complete credentials failed validation: %v", cfg.Platform, err)
		}
	}
}

// 停用的机器人允许留着不完整的凭据，否则用户没法先建档再慢慢填。
func TestValidateSkipsCredentialsWhenDisabled(t *testing.T) {
	for _, platform := range []string{PlatformQQOfficial, PlatformIMessage} {
		cfg := BotConfig{Platform: platform, Enabled: false}.WithDefaults()
		if err := cfg.Validate(); err != nil {
			t.Fatalf("disabled %q profile failed validation: %v", platform, err)
		}
	}
}

func TestPayloadFromConfigMasksNewPlatformSecrets(t *testing.T) {
	cfg := BotConfig{
		Platform:             PlatformIMessage,
		QQAppSecret:          "qq-secret",
		IMessagePassword:     "bb-password",
		IMessageWebhookToken: "bb-webhook",
	}
	payload := PayloadFromConfig(cfg)

	secrets := map[string]string{
		"qq app secret":     payload.QQAppSecret,
		"imessage password": payload.IMessagePassword,
		"imessage webhook":  payload.IMessageWebhookToken,
	}
	for name, value := range secrets {
		if value != "" {
			t.Fatalf("%s leaked into the plain payload: %q", name, value)
		}
	}
	if !payload.QQAppSecretConfigured || !payload.IMessagePasswordConfigured || !payload.IMessageWebhookTokenConfigured {
		t.Fatal("configured flags should still tell the UI a secret is stored")
	}
	// 回调路径是公开信息，前端要靠它告诉用户该往对方后台填什么。
	if payload.CallbackPath != IMessageCallbackPath {
		t.Fatalf("callback path = %q, want %q", payload.CallbackPath, IMessageCallbackPath)
	}

	revealed := PayloadFromConfigWithSecrets(cfg)
	if revealed.IMessagePassword != "bb-password" || revealed.QQAppSecret != "qq-secret" {
		t.Fatal("explicit secret retrieval should return the stored values")
	}
}

// 前端为了不回显明文会把密钥字段留空，留空必须沿用旧值。漏掉任何一个，用户改
// 个无关设置就会把凭据清空，机器人随之掉线。
func TestConfigFromPayloadPreservesBlankSecrets(t *testing.T) {
	existing := BotConfig{
		Platform:             PlatformIMessage,
		QQAppSecret:          "qq-secret",
		IMessagePassword:     "bb-password",
		IMessageWebhookToken: "bb-webhook",
	}
	// 只改一个无关字段，所有密钥字段留空。
	payload := ConfigPayload{Platform: PlatformIMessage, Name: "改个名字"}
	merged := ConfigFromPayload(payload, existing)

	if merged.QQAppSecret != "qq-secret" ||
		merged.IMessagePassword != "bb-password" ||
		merged.IMessageWebhookToken != "bb-webhook" {
		t.Fatalf("blank secret fields wiped stored credentials: %+v", merged)
	}

	// 明确填了新值时当然要覆盖。
	payload.QQAppSecret = "rotated"
	if merged := ConfigFromPayload(payload, existing); merged.QQAppSecret != "rotated" {
		t.Fatalf("a supplied secret was not applied: %q", merged.QQAppSecret)
	}
}

// 已下线平台的别名仍要收敛到原来的 ID：老配置写的可能是 lark、企业微信这类写法，
// 认不出来就没法提示「已暂时下线」，只会报一个看不懂的未知平台。
func TestRetiredPlatformsAreRecognizedButRejected(t *testing.T) {
	for input, name := range map[string]string{
		"dingtalk": "钉钉", "DingTalk": "钉钉", "lark": "飞书", "飞书": "飞书", "wework": "企业微信", "企业微信": "企业微信",
	} {
		got, ok := RetiredPlatformName(input)
		if !ok || got != name {
			t.Fatalf("RetiredPlatformName(%q) = %q, %v", input, got, ok)
		}
		if _, ok := PlatformByID(input); ok {
			t.Fatalf("%q should no longer be in the registry", input)
		}
		err := ValidatePlatform(input)
		if err == nil || !strings.Contains(err.Error(), "已暂时下线") {
			t.Fatalf("ValidatePlatform(%q) = %v", input, err)
		}
	}
	for _, id := range []string{PlatformTelegram, PlatformWeixin, PlatformIMessage} {
		if _, ok := RetiredPlatformName(id); ok {
			t.Fatalf("%q is not retired", id)
		}
	}
}

// 已下线平台的机器人拿到占位通道：不连接、不发送，状态里说清原因。
func TestNewChannelForConfigReportsRetiredPlatform(t *testing.T) {
	channel := NewChannelForConfig(BotConfig{ID: "old", Name: "老机器人", Platform: "feishu"})
	if channel == nil {
		t.Fatal("retired platform should get a placeholder channel")
	}
	status := channel.Status()
	if status.Connected || !strings.Contains(status.LastError, "飞书 平台已暂时下线") {
		t.Fatalf("status = %+v", status)
	}
	if err := channel.Send(context.Background(), OutgoingMessage{Text: "hi"}); err == nil {
		t.Fatal("placeholder channel must not pretend to send")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := channel.Connect(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect() = %v, want context.Canceled", err)
	}
}

// 老配置里还开着一台已下线平台的机器人，不能拖累其它机器人：行为配置照常能改，
// 运行时也不把它当成启用中的机器人去校验。
func TestRetiredPlatformProfileDoesNotBlockOtherProfiles(t *testing.T) {
	retired := BotConfig{ID: "old", Name: "老机器人", Platform: "dingtalk", Enabled: true}.WithDefaults()
	telegram := BotConfig{ID: "tg", Name: "电报", Platform: PlatformTelegram, Enabled: true, TelegramBotToken: "token"}.WithDefaults()
	runtime := NewRuntime(telegram, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	set := ProfileSet{Profiles: []BotConfig{retired, telegram}}
	if err := runtime.ApplyProfiles(context.Background(), set, nil); err != nil {
		t.Fatalf("ApplyProfiles() = %v", err)
	}
	runtime.mu.RLock()
	enabled := runtime.enabledProfilesLocked()
	runtime.mu.RUnlock()
	if len(enabled) != 1 || enabled[0].ID != "tg" {
		t.Fatalf("enabled profiles = %+v", enabled)
	}

	onlyRetired := NewRuntime(retired, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	onlyRetired.SetProfiles(ProfileSet{Profiles: []BotConfig{retired}})
	if err := onlyRetired.Start(context.Background()); !errors.Is(err, ErrBotDisabled) {
		t.Fatalf("Start() with only a retired profile = %v, want ErrBotDisabled", err)
	}
}
