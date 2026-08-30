// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"net/http"
	"strings"
	"sync"
)

// 飞书和企业微信没有出站长连接模式，事件只能由平台 POST 到本机。通道实例是配置
// 工厂按需重建的，而 HTTP 路由在进程启动时就挂好了——中间需要一层注册表，把
// 「当前生效的那个通道」和固定的回调路径对上。

const (
	// FeishuCallbackPath 是飞书事件订阅的回调路径。
	FeishuCallbackPath = "/api/channels/feishu/callback"
	// WeComCallbackPath 是企业微信应用回调的路径。
	WeComCallbackPath = "/api/channels/wecom/callback"
)

var callbackRegistry = struct {
	mu       sync.RWMutex
	handlers map[string]http.Handler
}{handlers: map[string]http.Handler{}}

// callbackKey 用平台加配置档拼出注册键。
func callbackKey(platform, profileID string) string {
	platform = NormalizePlatformID(platform)
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return platform
	}
	return platform + "/" + profileID
}

// RegisterCallbackHandler 登记一个平台回调处理器。
//
// 同平台重复登记会覆盖：配置改动后工厂会重建通道，新实例必须顶掉旧的，否则
// 回调还会打到已经停用的那个连接上。
func RegisterCallbackHandler(platform, profileID string, handler http.Handler) {
	if handler == nil {
		return
	}
	callbackRegistry.mu.Lock()
	defer callbackRegistry.mu.Unlock()
	callbackRegistry.handlers[callbackKey(platform, profileID)] = handler
	// 同时登记一个不带配置档的键，让没有配置多机器人的部署可以用短地址。
	if strings.TrimSpace(profileID) != "" {
		callbackRegistry.handlers[callbackKey(platform, "")] = handler
	}
}

// UnregisterCallbackHandler 注销回调处理器。
func UnregisterCallbackHandler(platform, profileID string) {
	callbackRegistry.mu.Lock()
	defer callbackRegistry.mu.Unlock()
	key := callbackKey(platform, profileID)
	delete(callbackRegistry.handlers, key)
	// 短地址只有在它确实指向这个实例时才一并删掉。
	if bare := callbackKey(platform, ""); bare != key {
		if current, ok := callbackRegistry.handlers[bare]; ok {
			if existing, found := callbackRegistry.handlers[key]; !found || current == existing {
				delete(callbackRegistry.handlers, bare)
			}
		}
	}
}

// ServeCallback 把一个回调请求转给对应的通道，没有匹配的通道时返回 false。
func ServeCallback(platform, profileID string, w http.ResponseWriter, r *http.Request) bool {
	callbackRegistry.mu.RLock()
	handler, ok := callbackRegistry.handlers[callbackKey(platform, profileID)]
	if !ok && strings.TrimSpace(profileID) != "" {
		handler, ok = callbackRegistry.handlers[callbackKey(platform, "")]
	}
	callbackRegistry.mu.RUnlock()
	if !ok {
		return false
	}
	handler.ServeHTTP(w, r)
	return true
}

// CallbackPathFor 返回平台的回调路径，非回调平台返回空串。
func CallbackPathFor(platform string) string {
	def, ok := PlatformByID(platform)
	if !ok {
		return ""
	}
	return def.CallbackPath
}
