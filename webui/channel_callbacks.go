// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"

	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

// ChannelCallbackHandler 把飞书、企业微信的事件回调转给当前生效的通道实例。
//
// 路由在进程启动时就固定挂好，通道实例却会随配置改动重建；中间隔着 assistant
// 包里的注册表，这里只负责把 HTTP 请求送进去。
type ChannelCallbackHandler struct{}

// NewChannelCallbackHandler 创建回调处理器。
func NewChannelCallbackHandler() *ChannelCallbackHandler {
	return &ChannelCallbackHandler{}
}

// Register 注册回调路由。
//
// 带 :profile 的形式用于同一平台配置了多个机器人时精确指定；大多数部署只有一个，
// 直接用不带后缀的短地址即可。
func (h *ChannelCallbackHandler) Register(router gin.IRouter) {
	for _, path := range []string{assistant.FeishuCallbackPath, assistant.WeComCallbackPath} {
		router.Any(path, h.serve)
		router.Any(path+"/:profile", h.serve)
	}
}

// serve 按路径推断平台并转发。
func (h *ChannelCallbackHandler) serve(c *gin.Context) {
	platform := ""
	switch {
	case pathHasPrefix(c.Request.URL.Path, assistant.FeishuCallbackPath):
		platform = assistant.PlatformFeishu
	case pathHasPrefix(c.Request.URL.Path, assistant.WeComCallbackPath):
		platform = assistant.PlatformWeCom
	default:
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "未知的回调平台"})
		return
	}
	if assistant.ServeCallback(platform, c.Param("profile"), c.Writer, c.Request) {
		c.Abort()
		return
	}
	// 没有对应的运行中通道：多半是机器人没启用，或者平台字段配的不是这个。
	// 回 503 而不是 404，让对方后台的重试机制照常工作。
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "该平台当前没有启用中的机器人"})
}

// pathHasPrefix 判断请求路径是否属于某个回调前缀。
func pathHasPrefix(path, prefix string) bool {
	return path == prefix || (len(path) > len(prefix) && path[:len(prefix)] == prefix && path[len(prefix)] == '/')
}
