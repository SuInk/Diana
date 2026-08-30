// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/llmauth"

	"github.com/gin-gonic/gin"
)

func newOAuthTestRouter(t *testing.T, manager *llmauth.Manager) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := NewLLMConfigHandler(NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "sk-test", Model: "gpt-test",
	}))
	handler.SetOAuthManager(manager)
	router := gin.New()
	handler.Register(router)
	return router
}

func callJSON(t *testing.T, router *gin.Engine, method, path string, payload any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var body *strings.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		body = strings.NewReader(string(encoded))
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	decoded := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	return rec, decoded
}

// 没配持久化存储时说清楚「没启用」。直接 404 会让人以为是版本太旧。
func TestOAuthEndpointsSayWhenTheyAreDisabled(t *testing.T) {
	router := newOAuthTestRouter(t, nil)
	rec, decoded := callJSON(t, router, http.MethodGet, "/api/llm/oauth/providers", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(decoded["error"].(string), "未启用") {
		t.Fatalf("error = %v", decoded["error"])
	}
}

// 内置提供商开箱就在列表里，且都是未登录状态。
func TestOAuthProvidersListsBuiltinsAsLoggedOut(t *testing.T) {
	router := newOAuthTestRouter(t, llmauth.NewManager(nil, http.DefaultClient))
	rec, decoded := callJSON(t, router, http.MethodGet, "/api/llm/oauth/providers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	providers, _ := decoded["providers"].([]any)
	if len(providers) == 0 {
		t.Fatalf("providers = %v, want at least the built-ins", decoded["providers"])
	}
	first, _ := providers[0].(map[string]any)
	if loggedIn, _ := first["logged_in"].(bool); loggedIn {
		t.Fatalf("a fresh install reported a logged-in provider: %#v", first)
	}
}

// 发起登录返回授权地址；verifier 属于服务端，一个字节都不能出去。
func TestOAuthLoginStartReturnsAuthorizeURLWithoutTheVerifier(t *testing.T) {
	manager := llmauth.NewManager(nil, http.DefaultClient)
	router := newOAuthTestRouter(t, manager)
	if _, err := manager.SaveCustomProvider(context.Background(), llmauth.Provider{
		Key: "example", Label: "示例", ClientID: "diana",
		AuthorizeURL: "https://auth.example.invalid/authorize",
		TokenURL:     "https://auth.example.invalid/token",
	}); err != nil {
		t.Fatalf("SaveCustomProvider() error = %v", err)
	}
	rec, decoded := callJSON(t, router, http.MethodPost, "/api/llm/oauth/login/start", map[string]string{"provider": "example"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	login, _ := decoded["login"].(map[string]any)
	authorizeURL, _ := login["authorize_url"].(string)
	if !strings.Contains(authorizeURL, "code_challenge=") {
		t.Fatalf("authorize_url = %q, want PKCE parameters", authorizeURL)
	}
	if strings.Contains(rec.Body.String(), "code_verifier") {
		t.Fatalf("the code verifier leaked to the browser: %s", rec.Body.String())
	}
	if _, ok := login["id"].(string); !ok {
		t.Fatalf("login = %#v, want an id to complete with", login)
	}
}

// 未知提供商要给出可读的 400，而不是 500。
func TestOAuthLoginStartRejectsUnknownProviders(t *testing.T) {
	router := newOAuthTestRouter(t, llmauth.NewManager(nil, http.DefaultClient))
	rec, _ := callJSON(t, router, http.MethodPost, "/api/llm/oauth/login/start", map[string]string{"provider": "nope"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// 自定义提供商不能顶掉内置的。
func TestOAuthSaveProviderRefusesToShadowBuiltins(t *testing.T) {
	manager := llmauth.NewManager(nil, http.DefaultClient)
	router := newOAuthTestRouter(t, manager)
	builtin := manager.Providers()[0]
	rec, _ := callJSON(t, router, http.MethodPost, "/api/llm/oauth/providers", map[string]any{
		"key":           builtin.Key,
		"authorize_url": "https://evil.example.invalid/a",
		"token_url":     "https://evil.example.invalid/t",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// 明文地址会把授权码裸发出去，接口层就该拦住。
func TestOAuthSaveProviderRejectsPlaintextEndpoints(t *testing.T) {
	router := newOAuthTestRouter(t, llmauth.NewManager(nil, http.DefaultClient))
	rec, decoded := callJSON(t, router, http.MethodPost, "/api/llm/oauth/providers", map[string]any{
		"key":           "plain",
		"authorize_url": "http://auth.example.invalid/a",
		"token_url":     "https://auth.example.invalid/t",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(decoded["error"].(string), "https") {
		t.Fatalf("error = %v", decoded["error"])
	}
}

// 读接口把 client secret 脱敏成 ***，原样交回来时视为「没改」，
// 否则用户只改了个 scope 就会把 secret 冲成三个星号。
func TestOAuthSaveProviderKeepsTheSecretWhenTheMaskComesBack(t *testing.T) {
	manager := llmauth.NewManager(nil, http.DefaultClient)
	router := newOAuthTestRouter(t, manager)
	base := map[string]any{
		"key":           "gateway",
		"label":         "自建网关",
		"authorize_url": "https://gw.example.invalid/authorize",
		"token_url":     "https://gw.example.invalid/token",
		"client_id":     "diana",
		"client_secret": "super-secret",
	}
	if rec, _ := callJSON(t, router, http.MethodPost, "/api/llm/oauth/providers", base); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// 列表里不能出现明文 secret。
	rec, _ := callJSON(t, router, http.MethodGet, "/api/llm/oauth/providers", nil)
	if strings.Contains(rec.Body.String(), "super-secret") {
		t.Fatalf("client secret leaked: %s", rec.Body.String())
	}

	updated := map[string]any{}
	for key, value := range base {
		updated[key] = value
	}
	updated["client_secret"] = redactedSecretPlaceholder
	updated["label"] = "改了个名字"
	if rec, _ := callJSON(t, router, http.MethodPost, "/api/llm/oauth/providers", updated); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	provider, ok := manager.Provider("gateway")
	if !ok {
		t.Fatal("provider disappeared after the update")
	}
	if provider.ClientSecret != "super-secret" {
		t.Fatalf("client secret = %q, want the stored one to survive the masked round trip", provider.ClientSecret)
	}
	if provider.Label != "改了个名字" {
		t.Fatalf("the rest of the update was lost: %#v", provider)
	}
}

// 换令牌失败时把提供商给的原因带出来，但绝不回显用户粘贴的内容——那串里有授权码。
func TestOAuthLoginCompleteSurfacesReasonsWithoutEchoingTheCode(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"授权码已过期"}`))
	}))
	t.Cleanup(tokenServer.Close)

	manager := llmauth.NewManager(nil, tokenServer.Client())
	router := newOAuthTestRouter(t, manager)
	if _, err := manager.SaveCustomProvider(context.Background(), llmauth.Provider{
		Key: "example", ClientID: "diana",
		AuthorizeURL: "https://auth.example.invalid/authorize",
		TokenURL:     tokenServer.URL,
	}); err != nil {
		t.Fatalf("SaveCustomProvider() error = %v", err)
	}
	login, err := manager.StartLogin("example")
	if err != nil {
		t.Fatalf("StartLogin() error = %v", err)
	}
	const pasted = "https://console.example.invalid/callback?code=super-secret-code&state=" + "x"
	rec, decoded := callJSON(t, router, http.MethodPost, "/api/llm/oauth/login/complete", map[string]string{
		"provider": "example", "login_id": login.ID, "callback": pasted,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "super-secret-code") {
		t.Fatalf("the pasted authorization code was echoed back: %s", rec.Body.String())
	}
	_ = decoded
}

// 登录完成后状态要翻过来，退出后要翻回去，全程不带令牌。
func TestOAuthLoginCompleteAndLogoutRoundTrip(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-secret","refresh_token":"ref-secret","expires_in":3600}`))
	}))
	t.Cleanup(tokenServer.Close)

	manager := llmauth.NewManager(nil, tokenServer.Client())
	router := newOAuthTestRouter(t, manager)
	if _, err := manager.SaveCustomProvider(context.Background(), llmauth.Provider{
		Key: "example", Label: "示例", ClientID: "diana",
		AuthorizeURL: "https://auth.example.invalid/authorize",
		TokenURL:     tokenServer.URL,
	}); err != nil {
		t.Fatalf("SaveCustomProvider() error = %v", err)
	}
	_, startBody := callJSON(t, router, http.MethodPost, "/api/llm/oauth/login/start", map[string]string{"provider": "example"})
	login, _ := startBody["login"].(map[string]any)
	loginID, _ := login["id"].(string)

	rec, _ := callJSON(t, router, http.MethodPost, "/api/llm/oauth/login/complete", map[string]string{
		"provider": "example", "login_id": loginID, "callback": "bare-code",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"tok-secret", "ref-secret"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("token leaked to the console: %s", rec.Body.String())
		}
	}
	if !manager.HasToken("example") {
		t.Fatal("login did not stick")
	}

	rec, _ = callJSON(t, router, http.MethodPost, "/api/llm/oauth/logout", map[string]string{"provider": "example"})
	if rec.Code != http.StatusOK {
		t.Fatalf("logout status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if manager.HasToken("example") {
		t.Fatal("logout left the token behind")
	}
}

// 绑了 OAuth 的配置档没有 API Key 也算合法，否则这一页会拦下一份能正常工作的配置。
func TestProviderConfigWithOAuthNeedsNoAPIKey(t *testing.T) {
	cfg := llm.ProviderConfig{
		Provider:      llm.ProviderOpenAICompatible,
		OAuthProvider: "example",
		Model:         "gpt-test",
		BaseURL:       "https://api.example.invalid/v1",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	// 两样都没有仍然要拦。
	cfg.OAuthProvider = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("a config with neither an API key nor an OAuth login was accepted")
	}
}
