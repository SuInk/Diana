// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/llmauth"

	"github.com/gin-gonic/gin"
)

// loggedInOAuthManager 走一遍真实的登录流程，返回一个已登录 example 的管理器。
func loggedInOAuthManager(t *testing.T, accessToken string) *llmauth.Manager {
	t.Helper()
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + accessToken + `","expires_in":3600}`))
	}))
	t.Cleanup(tokenServer.Close)
	manager := llmauth.NewManager(nil, tokenServer.Client())
	if _, err := manager.SaveCustomProvider(context.Background(), llmauth.Provider{
		Key: "example", Label: "示例", ClientID: "diana",
		AuthorizeURL: "https://auth.example.invalid/authorize",
		TokenURL:     tokenServer.URL,
	}); err != nil {
		t.Fatalf("SaveCustomProvider() error = %v", err)
	}
	router := newOAuthTestRouter(t, manager)
	_, startBody := callJSON(t, router, http.MethodPost, "/api/llm/oauth/login/start", map[string]string{"provider": "example"})
	login, _ := startBody["login"].(map[string]any)
	loginID, _ := login["id"].(string)
	if rec, _ := callJSON(t, router, http.MethodPost, "/api/llm/oauth/login/complete", map[string]string{
		"provider": "example", "login_id": loginID, "callback": "bare-code",
	}); rec.Code != http.StatusOK {
		t.Fatalf("login complete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return manager
}

// 「提供商」页的测试和拉模型走注册表，注册表也得按配置档补 OAuth 凭据，
// 否则只靠登录的配置档在这里以「没有 API Key」失败，而机器人实际运行是好的。
func TestProviderRegistryEndpointsUseOAuthCredentials(t *testing.T) {
	accessToken := "tok-" + "webui0123456789abc"
	var mu sync.Mutex
	auths := map[string][]string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths[r.URL.Path] = append(auths[r.URL.Path], r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"chat-model","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"chat-model","object":"model"}]}`))
	}))
	defer upstream.Close()

	manager := loggedInOAuthManager(t, accessToken)
	profileStore := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider:      llm.ProviderOpenAICompatible,
		APIFormat:     llm.APIFormatChatCompletions,
		BaseURL:       upstream.URL + "/v1",
		Model:         "chat-model",
		OAuthProvider: "example",
	})
	profile := profileStore.Profiles().Profiles[0]
	handler := NewLLMConfigHandler(profileStore)
	handler.SetOAuthManager(manager)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)

	rec, _ := callJSON(t, router, http.MethodPost, "/api/llm/providers/test", map[string]string{
		"providerId": profile.ID, "modelId": profile.ID + ":chat-model", "message": "hi",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("provider test status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec, _ = callJSON(t, router, http.MethodPost, "/api/llm/providers/models", map[string]string{"providerId": profile.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("provider models status = %d, body = %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) == 0 {
		t.Fatal("上游一次请求都没收到")
	}
	for path, values := range auths {
		for _, auth := range values {
			if auth != "Bearer "+accessToken {
				t.Fatalf("%s 收到的 Authorization = %q", path, auth)
			}
		}
	}
}

// 以前落库的注册表没有 OAuth 绑定这一项，启动时要认出来并重建。
func TestRegistryMissesOAuthBinding(t *testing.T) {
	set := llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "key", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-" + "configured0123456789", Model: "m"}},
		{ID: "login", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, OAuthProvider: "Example", Model: "m"}},
	}}
	current, _, err := llm.NewProviderRegistryFromProfiles(set)
	if err != nil {
		t.Fatal(err)
	}
	if registryMissesOAuthBinding(current.Document(), set) {
		t.Fatal("刚建的注册表不该被判成缺绑定")
	}
	stale := current.Document()
	for index := range stale.Providers {
		stale.Providers[index].OAuthProvider = ""
	}
	if !registryMissesOAuthBinding(stale, set) {
		t.Fatal("旧注册表漏了 OAuth 绑定，应当重建")
	}
	// 只有 API Key 的配置集和旧注册表对得上，不触发重建。
	keyOnly := llm.ProfileSet{Profiles: set.Profiles[:1]}
	if registryMissesOAuthBinding(stale, keyOnly) {
		t.Fatal("没有 OAuth 绑定的配置集不该触发重建")
	}
}
