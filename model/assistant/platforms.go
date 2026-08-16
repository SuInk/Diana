// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
)

const (
	PlatformOneBotV11 = "onebot-v11"
	PlatformTelegram  = "telegram"

	// Deprecated: implementation names are no longer separate platforms.
	// Keep the legacy values as migration inputs; all normalized data uses OneBot v11.
	PlatformNapCat   = "napcat"
	PlatformLagrange = "lagrange"
	PlatformGoCQHTTP = "go-cqhttp"

	ProtocolOneBotV11   = "onebot-v11-reverse-ws"
	ProtocolTelegramBot = "telegram-bot-api"

	// PlatformCategory* 用于在 WebUI 里按聊天平台分组。
	PlatformCategoryQQ       = "qq"
	PlatformCategoryTelegram = "telegram"
)

// PlatformDefinition 描述一个机器人接入平台及其使用的协议适配器。
type PlatformDefinition struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	// Category 是聊天平台本身；Label 是分类的展示名。
	Category      string `json:"category"`
	CategoryLabel string `json:"category_label"`
	Description   string `json:"description,omitempty"`
}

var supportedPlatforms = []PlatformDefinition{
	{ID: PlatformOneBotV11, Name: "OneBot v11", Protocol: ProtocolOneBotV11, Category: PlatformCategoryQQ, CategoryLabel: "QQ", Description: "QQ 的统一 OneBot v11 反向 WebSocket 接入"},
	{ID: PlatformTelegram, Name: "Telegram", Protocol: ProtocolTelegramBot, Category: PlatformCategoryTelegram, CategoryLabel: "Telegram", Description: "官方 Bot API 长轮询，不需要公网地址"},
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

// NormalizePlatformID 把旧版实现名称迁移为统一的平台 ID。
func NormalizePlatformID(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "onebot", "onebot-v11", "onebot v11", "napcat", "napcat / onebot v11",
		"lagrange", "lagrange.core", "lagrange core", "go-cqhttp", "gocqhttp", "go cqhttp":
		return PlatformOneBotV11
	case "telegram", "tg", "telegram bot":
		return PlatformTelegram
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
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
		return fmt.Errorf("qqbot: unsupported platform %q", id)
	}
	return nil
}
