// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
)

// OpenAPIPluginID 是对外开放接口插件的目录 ID，WebUI 层用它查询启用状态与设置。
const OpenAPIPluginID = "official.open-api"

const (
	openAPISettingRateLimit    = "rate_limit_per_minute"
	defaultOpenAPIRateLimit    = 60
	openAPIRateLimitLowerBound = 1
	openAPIRateLimitUpperBound = 600
)

// OpenAPIPlugin 把「对外开放接口」挂进插件目录。它不参与消息处理——HTTP 入口
// 在 webui 层——放在这里只为了两件事：插件页有一个统一的总开关，以及限流这类
// 参数走声明式插件设置，不用另起一套配置面板。
type OpenAPIPlugin struct{}

// NewOpenAPIPlugin 创建对外开放接口插件。
func NewOpenAPIPlugin() *OpenAPIPlugin {
	return &OpenAPIPlugin{}
}

// Manifest 返回对外开放接口插件清单。
func (p *OpenAPIPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:      OpenAPIPluginID,
		Name:    "对外 API",
		Version: "0.1.0",
		Description: "让 CI、监控这类外部系统凭密钥调用 HTTP 接口（/openapi/v1）向指定会话推送消息。" +
			"密钥在「设置 → 安全 → 对外 API」里管理；停用后外部调用立即收到 403，密钥管理不受影响。",
		Official: true,
		BuiltIn:  true,
		// 默认关闭：这是一个让外部系统开口说话的入口，装完就生效等于替用户
		// 把服务暴露的决定做了。
		DefaultDisabled: true,
		Permissions:     []string{"network:http", "message:write"},
		Settings: []PluginSettingSpec{
			{
				Key:         openAPISettingRateLimit,
				Label:       "单密钥限流",
				Description: "每把密钥每分钟允许的调用次数，超出返回 429。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultOpenAPIRateLimit,
				Min:         settingRange(openAPIRateLimitLowerBound),
				Max:         settingRange(openAPIRateLimitUpperBound),
				Step:        10,
				Unit:        "次/分钟",
			},
		},
	}
}

// Handle 不处理任何消息事件：插件的入口是 HTTP 请求，不在消息管线里。
func (p *OpenAPIPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

// OpenAPIRateLimitPerMinute 从插件生效设置里取限流值，越界回落默认。
func OpenAPIRateLimitPerMinute(settings SettingValues) int {
	limit := settings.Int(openAPISettingRateLimit, defaultOpenAPIRateLimit)
	if limit < openAPIRateLimitLowerBound || limit > openAPIRateLimitUpperBound {
		return defaultOpenAPIRateLimit
	}
	return limit
}
