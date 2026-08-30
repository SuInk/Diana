// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"strings"

	"github.com/SuInk/diana/model/llmauth"

	"github.com/gin-gonic/gin"
)

// OAuth 登录的控制台接口。
//
// 控制台跑在服务器上，浏览器未必和它同一台机器，所以这里不假设回调能落回本机：
// 后端只负责给出授权地址，用户在自己的浏览器里授权完，把地址栏里那条回调地址
// 粘回来。这是 Pi 在远程机器上的做法，对 WebUI 来说它是主路径而不是兜底。

// SetOAuthManager 挂上 OAuth 管理器；没挂时这组接口整体返回「未启用」。
func (h *LLMConfigHandler) SetOAuthManager(manager *llmauth.Manager) {
	h.oauth = manager
}

func (h *LLMConfigHandler) registerOAuthRoutes(router gin.IRouter) {
	router.GET("/api/llm/oauth/providers", h.oauthProviders)
	router.POST("/api/llm/oauth/providers", h.oauthSaveProvider)
	router.POST("/api/llm/oauth/providers/delete", h.oauthDeleteProvider)
	router.POST("/api/llm/oauth/login/start", h.oauthLoginStart)
	router.POST("/api/llm/oauth/login/complete", h.oauthLoginComplete)
	router.POST("/api/llm/oauth/login/cancel", h.oauthLoginCancel)
	router.POST("/api/llm/oauth/logout", h.oauthLogout)
}

// requireOAuth 在没配 OAuth 管理器时给出明确回答。
//
// 直接 404 会让前端以为是版本太旧；说清楚「没启用」，用户才知道该去看部署配置。
func (h *LLMConfigHandler) requireOAuth(c *gin.Context) bool {
	if h.oauth == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth 登录未启用：当前部署没有配置持久化存储"})
		return false
	}
	return true
}

type oauthProviderPayload struct {
	Key          string            `json:"key"`
	Label        string            `json:"label"`
	AuthorizeURL string            `json:"authorize_url"`
	TokenURL     string            `json:"token_url"`
	ClientID     string            `json:"client_id"`
	ClientSecret string            `json:"client_secret"`
	RedirectURI  string            `json:"redirect_uri"`
	Scopes       []string          `json:"scopes"`
	TokenHeaders map[string]string `json:"token_headers"`
	TokenHeader  string            `json:"token_header"`
	TokenScheme  string            `json:"token_scheme"`
	Notes        string            `json:"notes"`
}

type oauthLoginPayload struct {
	Provider string `json:"provider"`
	LoginID  string `json:"login_id"`
	// Callback 是用户从浏览器地址栏粘回来的内容：整条回调地址或其中的授权码。
	Callback string `json:"callback"`
}

// oauthProviders 返回全部提供商及其登录状态。令牌永不出现在这里。
func (h *LLMConfigHandler) oauthProviders(c *gin.Context) {
	if !h.requireOAuth(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"providers": h.oauth.Statuses()})
}

func (h *LLMConfigHandler) oauthSaveProvider(c *gin.Context) {
	if !h.requireOAuth(c) {
		return
	}
	var payload oauthProviderPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不正确"})
		return
	}
	provider := llmauth.Provider{
		Key:          payload.Key,
		Label:        payload.Label,
		AuthorizeURL: payload.AuthorizeURL,
		TokenURL:     payload.TokenURL,
		ClientID:     payload.ClientID,
		ClientSecret: payload.ClientSecret,
		RedirectURI:  payload.RedirectURI,
		Scopes:       payload.Scopes,
		TokenHeaders: payload.TokenHeaders,
		TokenHeader:  payload.TokenHeader,
		TokenScheme:  payload.TokenScheme,
		Notes:        payload.Notes,
	}
	// 读接口把 client secret 脱敏成 ***，原样提交回来时视为「没改」，
	// 否则用户只改了个 scope 就会把 secret 冲成三个星号。
	if strings.TrimSpace(payload.ClientSecret) == redactedSecretPlaceholder {
		if existing, ok := h.oauth.Provider(payload.Key); ok {
			provider.ClientSecret = existing.ClientSecret
		}
	}
	saved, err := h.oauth.SaveCustomProvider(c.Request.Context(), provider)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "llm.oauth.provider.save", err, payload.Key, nil)
		return
	}
	recordRequestOperation(c, h.logs, "llm.oauth.provider.save", "已保存 OAuth 提供商", saved.Key,
		map[string]any{"provider": saved.Key, "authorize_url": saved.AuthorizeURL})
	c.JSON(http.StatusOK, gin.H{"providers": h.oauth.Statuses()})
}

func (h *LLMConfigHandler) oauthDeleteProvider(c *gin.Context) {
	if !h.requireOAuth(c) {
		return
	}
	var payload oauthLoginPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不正确"})
		return
	}
	if err := h.oauth.DeleteCustomProvider(c.Request.Context(), payload.Provider); err != nil {
		h.writeError(c, http.StatusBadRequest, "llm.oauth.provider.delete", err, payload.Provider, nil)
		return
	}
	recordRequestOperation(c, h.logs, "llm.oauth.provider.delete", "已删除 OAuth 提供商", payload.Provider,
		map[string]any{"provider": payload.Provider})
	c.JSON(http.StatusOK, gin.H{"providers": h.oauth.Statuses()})
}

// oauthLoginStart 发起授权，返回要让用户打开的地址。
func (h *LLMConfigHandler) oauthLoginStart(c *gin.Context) {
	if !h.requireOAuth(c) {
		return
	}
	var payload oauthLoginPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不正确"})
		return
	}
	login, err := h.oauth.StartLogin(payload.Provider)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "llm.oauth.login.start", err, payload.Provider, nil)
		return
	}
	recordRequestOperation(c, h.logs, "llm.oauth.login.start", "已发起 OAuth 授权", payload.Provider,
		map[string]any{"provider": payload.Provider})
	// login 结构里的 CodeVerifier 带 json:"-"，不会随响应出去。
	c.JSON(http.StatusOK, gin.H{"login": login})
}

// oauthLoginComplete 用粘回来的回调地址换取令牌。
func (h *LLMConfigHandler) oauthLoginComplete(c *gin.Context) {
	if !h.requireOAuth(c) {
		return
	}
	var payload oauthLoginPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不正确"})
		return
	}
	if _, err := h.oauth.CompleteLogin(c.Request.Context(), payload.Provider, payload.LoginID, payload.Callback); err != nil {
		// 失败原因要照实说（state 不符、授权码过期、提供商拒绝），
		// 但绝不带上用户粘贴的内容——那串里就有授权码。
		h.writeError(c, http.StatusBadRequest, "llm.oauth.login.complete", err, payload.Provider,
			map[string]any{"provider": payload.Provider})
		return
	}
	recordRequestOperation(c, h.logs, "llm.oauth.login.complete", "OAuth 登录成功", payload.Provider,
		map[string]any{"provider": payload.Provider})
	c.JSON(http.StatusOK, gin.H{"providers": h.oauth.Statuses()})
}

func (h *LLMConfigHandler) oauthLoginCancel(c *gin.Context) {
	if !h.requireOAuth(c) {
		return
	}
	var payload oauthLoginPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不正确"})
		return
	}
	h.oauth.CancelLogin(payload.LoginID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *LLMConfigHandler) oauthLogout(c *gin.Context) {
	if !h.requireOAuth(c) {
		return
	}
	var payload oauthLoginPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不正确"})
		return
	}
	if err := h.oauth.Logout(c.Request.Context(), payload.Provider); err != nil {
		h.writeError(c, http.StatusBadRequest, "llm.oauth.logout", err, payload.Provider, nil)
		return
	}
	recordRequestOperation(c, h.logs, "llm.oauth.logout", "已退出 OAuth 登录", payload.Provider,
		map[string]any{"provider": payload.Provider})
	c.JSON(http.StatusOK, gin.H{"providers": h.oauth.Statuses()})
}

const redactedSecretPlaceholder = "***"
