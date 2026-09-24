// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"

	"github.com/SuInk/diana/internal/secretmask"
)

// 这里把运行时手上的凭据登记给 secretmask。登记之后，Agent 的工具结果和报错、
// 外发消息、聊天里的错误说明再出现原文就会被换成掩码（见 internal/secretmask 和
// agent.Runner 里的统一出口）。
//
// 按原文认是兜底：HTTP 客户端报错带出整条地址、平台接口回显请求、插件把 Cookie
// 串进报错——这些形态千奇百怪，按形态认总有漏的，而原文只要出现就能对上。

// registerBotConfigSecrets 登记一台机器人的平台凭据，以及连接地址里嵌着的令牌
// （主人把 access_token 直接写进 ws 地址的查询参数、代理地址里带账号密码）。
func registerBotConfigSecrets(cfg BotConfig) {
	secretmask.Register(
		cfg.OneBotHTTPSecret,
		cfg.OneBotAccessToken,
		cfg.TelegramBotToken,
		cfg.QQAppSecret,
		cfg.DingTalkClientSecret,
		cfg.FeishuAppSecret,
		cfg.FeishuVerificationToken,
		cfg.FeishuEncryptKey,
		cfg.WeComSecret,
		cfg.WeComToken,
		cfg.WeComEncodingAESKey,
		cfg.NoneBotBridgeToken,
	)
	secretmask.RegisterURL(
		cfg.OneBotWSEndpoint,
		cfg.OneBotHTTPURL,
		cfg.OneBotReverseWSEndpoint,
		cfg.TelegramAPIBaseURL,
		cfg.TelegramProxyURL,
		cfg.FeishuAPIBaseURL,
		cfg.NoneBotBridgeEndpoint,
		cfg.AgentBrowserCDPURL,
	)
}

// registerPluginSecrets 登记插件设置里的凭据：声明为 Secret 的设置整值登记（Cookie
// 串、API Key、按仓库分开的 Token 表），其余字符串设置只登记里面嵌着的地址凭据
// （带账号密码的代理、查询参数里带 key 的接口地址）。
func registerPluginSecrets(state PluginState) {
	secrets := secretSettingKeys(state.Manifest.Settings)
	register := func(settings map[string]any) {
		for key, value := range settings {
			if secrets[key] {
				registerSecretSettingValue(value)
				continue
			}
			if text, ok := value.(string); ok {
				secretmask.RegisterURL(text)
			}
		}
	}
	register(state.Settings)
	for _, settings := range state.ProfileSettings {
		register(settings)
	}
}

// registerSecretSettingValue 登记一个凭据设置的值。有的凭据设置存的是 JSON（按
// 仓库分开的 Token 表），报错里只会出现其中一个令牌，所以把里面的字符串逐个登记。
func registerSecretSettingValue(value any) {
	switch typed := value.(type) {
	case string:
		secretmask.Register(typed)
		trimmed := strings.TrimSpace(typed)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var decoded any
			if json.Unmarshal([]byte(trimmed), &decoded) == nil {
				registerSecretSettingValue(decoded)
			}
		}
	case map[string]any:
		for _, item := range typed {
			registerSecretSettingValue(item)
		}
	case []any:
		for _, item := range typed {
			registerSecretSettingValue(item)
		}
	}
}
