// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
)

const (
	PlatformOneBotV11  = "onebot-v11"
	PlatformTelegram   = "telegram"
	PlatformQQOfficial = "qq-official"
	PlatformDingTalk   = "dingtalk"
	PlatformFeishu     = "feishu"
	PlatformWeCom      = "wecom"

	ProtocolOneBotV11     = "onebot-v11-reverse-ws"
	ProtocolTelegramBot   = "telegram-bot-api"
	ProtocolQQOfficialWS  = "qq-official-gateway-ws"
	ProtocolDingTalkWS    = "dingtalk-stream-ws"
	ProtocolFeishuWebhook = "feishu-event-callback"
	ProtocolWeComWebhook  = "wecom-event-callback"

	// PlatformCategory* 用于在 WebUI 里按聊天平台分组。
	PlatformCategoryOneBotV11  = "onebot_v11"
	PlatformCategoryTelegram   = "telegram"
	PlatformCategoryQQOfficial = "qq_official"
	PlatformCategoryDingTalk   = "dingtalk"
	PlatformCategoryFeishu     = "feishu"
	PlatformCategoryWeCom      = "wecom"
)

// PlatformInbound 说明消息是怎么进来的。这决定了部署形态：只有 InboundCallback
// 的平台需要一个公网可达的 HTTPS 回调地址，其余两种都是本机主动出站。
type PlatformInbound string

const (
	// InboundReverseWS 由接入端反连到本机监听的 WebSocket。
	InboundReverseWS PlatformInbound = "reverse_ws"
	// InboundOutbound 由本机主动出站建立连接（长轮询或长连接）。
	InboundOutbound PlatformInbound = "outbound"
	// InboundCallback 由平台把事件 POST 到本机暴露的回调地址。
	InboundCallback PlatformInbound = "callback"
)

// PlatformDefinition 描述一个机器人接入平台及其使用的协议适配器。
type PlatformDefinition struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	// Category 是聊天平台本身；Label 是分类的展示名。
	Category      string          `json:"category"`
	CategoryLabel string          `json:"category_label"`
	Description   string          `json:"description,omitempty"`
	Inbound       PlatformInbound `json:"inbound,omitempty"`
	// CallbackPath 是 InboundCallback 平台在本机监听的回调路径，供 WebUI 拼出
	// 完整地址让用户填到对方后台。
	CallbackPath string `json:"callback_path,omitempty"`
}

var supportedPlatforms = []PlatformDefinition{
	{ID: PlatformOneBotV11, Name: "OneBot v11", Protocol: ProtocolOneBotV11, Category: PlatformCategoryOneBotV11, CategoryLabel: "OneBot v11", Description: "统一的 OneBot v11 反向 WebSocket 接入", Inbound: InboundReverseWS},
	{ID: PlatformTelegram, Name: "Telegram", Protocol: ProtocolTelegramBot, Category: PlatformCategoryTelegram, CategoryLabel: "Telegram", Description: "官方 Bot API 长轮询，不需要公网地址", Inbound: InboundOutbound},
	{ID: PlatformQQOfficial, Name: "QQ 官方机器人", Protocol: ProtocolQQOfficialWS, Category: PlatformCategoryQQOfficial, CategoryLabel: "QQ 官方机器人", Description: "QQ 开放平台 WebSocket 网关，出站长连接，不需要公网地址", Inbound: InboundOutbound},
	{ID: PlatformDingTalk, Name: "钉钉", Protocol: ProtocolDingTalkWS, Category: PlatformCategoryDingTalk, CategoryLabel: "钉钉", Description: "Stream 模式出站长连接，不需要公网地址", Inbound: InboundOutbound},
	{ID: PlatformFeishu, Name: "飞书", Protocol: ProtocolFeishuWebhook, Category: PlatformCategoryFeishu, CategoryLabel: "飞书", Description: "事件订阅回调，需要一个公网可达的回调地址", Inbound: InboundCallback, CallbackPath: FeishuCallbackPath},
	{ID: PlatformWeCom, Name: "企业微信", Protocol: ProtocolWeComWebhook, Category: PlatformCategoryWeCom, CategoryLabel: "企业微信", Description: "应用回调，需要一个公网可达的回调地址", Inbound: InboundCallback, CallbackPath: WeComCallbackPath},
}

// IsOneBotPlatform 判断平台是否走 OneBot 反向 WebSocket 适配器。
func IsOneBotPlatform(id string) bool {
	def, ok := PlatformByID(id)
	return ok && def.Protocol == ProtocolOneBotV11
}

// SupportedPlatforms 返回平台注册表副本。
func SupportedPlatforms() []PlatformDefinition {
	return append([]PlatformDefinition(nil), supportedPlatforms...)
}

// NormalizePlatformID 归一化平台 ID：大小写、空白和常见写法都收敛到注册表里的
// 那一个值，空值按默认平台处理。调用方遍布配置、路由和存储，不能直接比较字符串。
func NormalizePlatformID(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "onebot", "onebot-v11", "onebot v11":
		return PlatformOneBotV11
	case "telegram", "tg", "telegram bot":
		return PlatformTelegram
	case "qq-official", "qq_official", "qqbot", "qq bot", "qq official":
		return PlatformQQOfficial
	case "dingtalk", "ding", "ding-talk", "钉钉":
		return PlatformDingTalk
	case "feishu", "lark", "飞书":
		return PlatformFeishu
	case "wecom", "wework", "work-weixin", "企业微信":
		return PlatformWeCom
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

// PlatformInboundMode 返回平台的消息入站方式，未知平台按出站处理。
func PlatformInboundMode(id string) PlatformInbound {
	def, ok := PlatformByID(id)
	if !ok {
		return InboundOutbound
	}
	return def.Inbound
}

// PlatformNeedsCallback 判断平台是否要求本机暴露公网回调地址。
func PlatformNeedsCallback(id string) bool {
	return PlatformInboundMode(id) == InboundCallback
}

// NewChannelForConfig 按平台造出对应的通道实现。
//
// 三处工厂（启动时的配置集、WebUI 单机器人保存、默认工厂）以前各写一份平台判断，
// 分叉过一次就出过「一边认这个平台、另一边不认」的线上问题。新增平台后分支更多，
// 所以收敛到这一个函数，OneBot 除外——它是进程内共享的监听器，只能由调用方决定。
func NewChannelForConfig(cfg BotConfig) Channel {
	switch NormalizePlatformID(cfg.Platform) {
	case PlatformTelegram:
		return NewTelegramChannel(TelegramConfig{
			BotToken:   cfg.TelegramBotToken,
			APIBaseURL: cfg.TelegramAPIBaseURL,
			ProxyURL:   cfg.TelegramProxyURL,
		})
	case PlatformQQOfficial:
		return NewQQOfficialChannel(QQOfficialConfig{
			AppID:     cfg.QQAppID,
			AppSecret: cfg.QQAppSecret,
			Sandbox:   cfg.QQSandbox,
		})
	case PlatformDingTalk:
		return NewDingTalkChannel(DingTalkConfig{
			ClientID:     cfg.DingTalkClientID,
			ClientSecret: cfg.DingTalkClientSecret,
			RobotCode:    cfg.DingTalkRobotCode,
		})
	case PlatformFeishu:
		return NewFeishuChannel(FeishuConfig{
			ProfileID:         cfg.ID,
			AppID:             cfg.FeishuAppID,
			AppSecret:         cfg.FeishuAppSecret,
			VerificationToken: cfg.FeishuVerificationToken,
			EncryptKey:        cfg.FeishuEncryptKey,
			APIBaseURL:        cfg.FeishuAPIBaseURL,
		})
	case PlatformWeCom:
		return NewWeComChannel(WeComConfig{
			ProfileID:      cfg.ID,
			CorpID:         cfg.WeComCorpID,
			AgentID:        cfg.WeComAgentID,
			Secret:         cfg.WeComSecret,
			Token:          cfg.WeComToken,
			EncodingAESKey: cfg.WeComEncodingAESKey,
		})
	}
	return nil
}

// PlatformByID 查找平台定义。
func PlatformByID(id string) (PlatformDefinition, bool) {
	id = NormalizePlatformID(id)
	for _, platform := range supportedPlatforms {
		if platform.ID == id {
			return platform, true
		}
	}
	return PlatformDefinition{}, false
}

// ValidatePlatform 校验平台是否有可用适配器。
func ValidatePlatform(id string) error {
	if _, ok := PlatformByID(id); !ok {
		return fmt.Errorf("chatbot: unsupported platform %q", id)
	}
	return nil
}
