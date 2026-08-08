package assistant

import "testing"

func TestNormalizePlatformIDMigratesLegacyNames(t *testing.T) {
	cases := map[string]string{
		"":                    PlatformNapCat,
		"NapCat / OneBot V11": PlatformNapCat,
		"Lagrange.Core":       PlatformLagrange,
		"go-cqhttp":           PlatformGoCQHTTP,
		"Telegram":            PlatformTelegram,
		"tg":                  PlatformTelegram,
	}
	for input, want := range cases {
		if got := NormalizePlatformID(input); got != want {
			t.Fatalf("NormalizePlatformID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidatePlatformRejectsUnknownAdapter(t *testing.T) {
	if err := ValidatePlatform(PlatformLagrange); err != nil {
		t.Fatalf("ValidatePlatform(%q) error = %v", PlatformLagrange, err)
	}
	if err := ValidatePlatform(PlatformTelegram); err != nil {
		t.Fatalf("ValidatePlatform(%q) error = %v", PlatformTelegram, err)
	}
	if err := ValidatePlatform("discord"); err == nil {
		t.Fatal("ValidatePlatform(discord) unexpectedly succeeded")
	}
}

func TestPlatformCategories(t *testing.T) {
	byID := map[string]PlatformDefinition{}
	for _, p := range SupportedPlatforms() {
		byID[p.ID] = p
	}
	for _, id := range []string{PlatformNapCat, PlatformLagrange, PlatformGoCQHTTP} {
		if byID[id].Category != PlatformCategoryQQ {
			t.Fatalf("%s 应属于 QQ 分类，实际 %q", id, byID[id].Category)
		}
		if !IsOneBotPlatform(id) {
			t.Fatalf("%s 应走 OneBot 适配器", id)
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
	onebot := BotConfig{Platform: PlatformNapCat, Enabled: true}
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
