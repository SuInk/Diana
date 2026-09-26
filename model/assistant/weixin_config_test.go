// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"testing"
)

// 微信凭据只能来自扫码：普通保存接口既不能写入、也不能因为留空把它清掉。
func TestConfigFromPayloadNeverTakesWeixinCredentials(t *testing.T) {
	existing := BotConfig{Platform: PlatformWeixin, WeixinBotToken: "tok", WeixinBotID: "bot@im.bot", WeixinUserID: "me@im.wechat", WeixinBaseURL: "https://ilinkai.weixin.qq.com"}
	merged := ConfigFromPayload(ConfigPayload{Platform: PlatformWeixin, Name: "改名", WeixinBotToken: "forged", WeixinBotID: "other"}, existing)
	if merged.WeixinBotToken != "tok" || merged.WeixinBotID != "bot@im.bot" || merged.WeixinUserID != "me@im.wechat" {
		t.Fatalf("weixin credentials were taken from the payload: %+v", merged)
	}

	payload := PayloadFromConfig(existing)
	if payload.WeixinBotToken != "" || !payload.WeixinBotTokenConfigured || payload.WeixinBotID != "bot@im.bot" {
		t.Fatalf("plain payload leaked or lost weixin state: token=%q configured=%v id=%q", payload.WeixinBotToken, payload.WeixinBotTokenConfigured, payload.WeixinBotID)
	}
	if PayloadFromConfigWithSecrets(existing).WeixinBotToken != "tok" {
		t.Fatal("explicit secret retrieval should return the bot token")
	}
}

// 扫码要挂在一台已保存的机器人上，所以启用但还没登录的微信配置必须能存下来。
func TestValidateAllowsWeixinBeforeLogin(t *testing.T) {
	if err := (BotConfig{Platform: PlatformWeixin, Enabled: true}).WithDefaults().Validate(); err != nil {
		t.Fatalf("enabled weixin profile without login failed validation: %v", err)
	}
	bad := BotConfig{Platform: PlatformWeixin, Enabled: true, WeixinBaseURL: "ftp://x"}
	if err := bad.WithDefaults().Validate(); !errors.Is(err, ErrInvalidWeixinBaseURL) {
		t.Fatalf("Validate() = %v, want ErrInvalidWeixinBaseURL", err)
	}
}
