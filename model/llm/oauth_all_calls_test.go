// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// 只用 OAuth 登录的配置档（没有 API Key）走到生图、改图、embedding、注册表路由、
// 拉模型列表时，请求都要带上 OAuth 令牌；没绑 OAuth 的配置档一切照旧。

var oauthTestPNG = []byte("\x89PNG\r\n\x1a\n0123456789")

type fakeOAuthResolver struct {
	token string
	err   error
	calls atomic.Int32
	keys  sync.Map
}

func (f *fakeOAuthResolver) Credential(_ context.Context, key string) (Credential, error) {
	f.calls.Add(1)
	f.keys.Store(key, true)
	if f.err != nil {
		return Credential{}, f.err
	}
	return Credential{Kind: CredentialKindOAuth, Token: f.token, ReplaceProviderAuth: true}, nil
}

// authRecorder 是一个假服务商：记下每个路径收到的 Authorization，按路径回固定内容。
type authRecorder struct {
	*httptest.Server
	mu    sync.Mutex
	auths map[string][]string
}

func newAuthRecorder(t *testing.T, status int) *authRecorder {
	t.Helper()
	recorder := &authRecorder{auths: map[string][]string{}}
	recorder.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		recorder.auths[r.URL.Path] = append(recorder.auths[r.URL.Path], r.Header.Get("Authorization"))
		recorder.mu.Unlock()
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"route not available for this token"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/images/generations"), strings.HasSuffix(r.URL.Path, "/images/edits"):
			_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(oauthTestPNG) + `"}]}`))
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]}]}`))
		case strings.HasSuffix(r.URL.Path, "/models"), strings.HasSuffix(r.URL.Path, "/model"):
			_, _ = w.Write([]byte(`{"data":[{"id":"m1","object":"model"}]}`))
		case strings.HasPrefix(r.URL.Path, "/files/"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(oauthTestPNG)
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(recorder.Server.Close)
	return recorder
}

func (r *authRecorder) received(pathSuffix string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for path, auths := range r.auths {
		if strings.HasSuffix(path, pathSuffix) {
			out = append(out, auths...)
		}
	}
	return out
}

func oauthOnlyConfig(baseURL string) ProviderConfig {
	return ProviderConfig{
		Provider:      ProviderOpenAICompatible,
		APIFormat:     APIFormatChatCompletions,
		BaseURL:       baseURL + "/v1",
		Model:         "m1",
		ImageModel:    "img-1",
		OAuthProvider: "fake-login",
	}
}

func testOAuthToken() string { return "oauth-" + "token0123456789abc" }

func assertOnlyBearer(t *testing.T, got []string, want string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatal("服务商一次请求都没收到")
	}
	for _, auth := range got {
		if auth != "Bearer "+want {
			t.Fatalf("Authorization = %q，想要 Bearer %s", auth, want)
		}
	}
}

func TestGenerateImageOAuthOnlyProfileSendsOAuthToken(t *testing.T) {
	server := newAuthRecorder(t, http.StatusOK)
	resolver := &fakeOAuthResolver{token: testOAuthToken()}
	cfg := oauthOnlyConfig(server.URL)
	resp, err := GenerateImage(context.Background(), cfg, ImageGenerateRequest{Prompt: "a cat"}, ClientOptionsFor(cfg, resolver)...)
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("images = %v", resp.Images)
	}
	assertOnlyBearer(t, server.received("/images/generations"), testOAuthToken())
	if _, ok := resolver.keys.Load("fake-login"); !ok {
		t.Fatal("凭据应当按配置档绑定的 OAuth 提供商取")
	}
}

func TestEditImageOAuthOnlyProfileSendsOAuthToken(t *testing.T) {
	server := newAuthRecorder(t, http.StatusOK)
	resolver := &fakeOAuthResolver{token: testOAuthToken()}
	cfg := oauthOnlyConfig(server.URL)
	source := "data:image/png;base64," + base64.StdEncoding.EncodeToString(oauthTestPNG)
	if _, err := EditImage(context.Background(), cfg, ImageEditRequest{Prompt: "make it blue", Images: []string{source}}, ClientOptionsFor(cfg, resolver)...); err != nil {
		t.Fatalf("EditImage() error = %v", err)
	}
	assertOnlyBearer(t, server.received("/images/edits"), testOAuthToken())
}

// 源图就在服务商自己的主机上（上一轮生成的图）：按 #764 的同源判断，OAuth 选项
// 接上后照样带令牌去取，改图请求本身也带令牌。
func TestEditImageOAuthSendsTokenToProviderOwnSourceImage(t *testing.T) {
	server := newAuthRecorder(t, http.StatusOK)
	resolver := &fakeOAuthResolver{token: testOAuthToken()}
	cfg := oauthOnlyConfig(server.URL)
	if _, err := EditImage(context.Background(), cfg, ImageEditRequest{Prompt: "make it blue", Images: []string{server.URL + "/files/out.png"}}, ClientOptionsFor(cfg, resolver)...); err != nil {
		t.Fatalf("EditImage() error = %v", err)
	}
	assertOnlyBearer(t, server.received("/files/out.png"), testOAuthToken())
	assertOnlyBearer(t, server.received("/images/edits"), testOAuthToken())
}

// 改图接上 OAuth 之后，源图链接背后的主机不能收到令牌：链接来自用户或模型，指向哪里都有可能。
func TestEditImageOAuthDoesNotSendTokenToSourceImageHost(t *testing.T) {
	// 源图服务在回环地址上；这里只看凭据，不看内网拦截。
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	server := newAuthRecorder(t, http.StatusOK)
	var sourceAuths []string
	var sourceMu sync.Mutex
	sourceHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceMu.Lock()
		sourceAuths = append(sourceAuths, r.Header.Get("Authorization"))
		sourceMu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(oauthTestPNG)
	}))
	defer sourceHost.Close()
	resolver := &fakeOAuthResolver{token: testOAuthToken()}
	cfg := oauthOnlyConfig(server.URL)
	for _, provider := range []Provider{ProviderOpenAICompatible, ProviderGemini} {
		if provider == ProviderGemini {
			// Gemini 这条只看源图下载：请求会发往默认地址之外的假服务商，失败无所谓。
			cfg = ProviderConfig{Provider: ProviderGemini, BaseURL: server.URL, Model: "gemini-image", ImageModel: "gemini-image", OAuthProvider: "fake-login"}
		}
		_, err := EditImage(context.Background(), cfg, ImageEditRequest{Prompt: "make it blue", Images: []string{sourceHost.URL + "/cat.png"}}, ClientOptionsFor(cfg, resolver)...)
		if provider == ProviderOpenAICompatible && err != nil {
			t.Fatalf("EditImage() error = %v", err)
		}
	}
	sourceMu.Lock()
	defer sourceMu.Unlock()
	if len(sourceAuths) != 2 {
		t.Fatalf("两家各应下载一次源图：%v", sourceAuths)
	}
	for _, auth := range sourceAuths {
		if auth != "" {
			t.Fatalf("源图主机收到了 Authorization：%q", auth)
		}
	}
	assertOnlyBearer(t, server.received("/images/edits"), testOAuthToken())
}

// 没绑 OAuth 的配置档：选项为空，客户端不包一层，请求带的就是配置里的 API Key。
func TestImageWithoutOAuthBindingKeepsAPIKey(t *testing.T) {
	server := newAuthRecorder(t, http.StatusOK)
	resolver := &fakeOAuthResolver{token: testOAuthToken()}
	cfg := oauthOnlyConfig(server.URL)
	cfg.OAuthProvider = ""
	cfg.APIKey = "key-" + "configured0123456789"
	opts := ClientOptionsFor(cfg, resolver)
	if len(opts) != 0 {
		t.Fatalf("没绑 OAuth 时不该有任何选项：%d", len(opts))
	}
	if _, err := GenerateImage(context.Background(), cfg, ImageGenerateRequest{Prompt: "a cat"}, opts...); err != nil {
		t.Fatal(err)
	}
	assertOnlyBearer(t, server.received("/images/generations"), cfg.APIKey)
	if resolver.calls.Load() != 0 {
		t.Fatal("没绑 OAuth 时不该去问 OAuth 层")
	}
}

// image_origin 会重建传输层去固定拨号地址，以前重建时把外面那层凭据注入整层丢了。
func TestGenerateImageWithImageOriginKeepsOAuthToken(t *testing.T) {
	server := newAuthRecorder(t, http.StatusOK)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fakeOAuthResolver{token: testOAuthToken()}
	cfg := oauthOnlyConfig("http://images.oauth-test.invalid:" + parsed.Port())
	cfg.ImageOrigin = net.JoinHostPort("127.0.0.1", parsed.Port())
	if _, err := GenerateImage(context.Background(), cfg, ImageGenerateRequest{Prompt: "a cat"}, ClientOptionsFor(cfg, resolver)...); err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	assertOnlyBearer(t, server.received("/images/generations"), testOAuthToken())
}

// 服务商对这把登录令牌不开放生图接口：报清楚原因，而不是一句「鉴权失败」。
func TestOAuthImageRouteRejectedExplainsLoginTokenScope(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden} {
		server := newAuthRecorder(t, status)
		resolver := &fakeOAuthResolver{token: testOAuthToken()}
		cfg := oauthOnlyConfig(server.URL)
		_, err := GenerateImage(context.Background(), cfg, ImageGenerateRequest{Prompt: "a cat"}, ClientOptionsFor(cfg, resolver)...)
		var unsupported *OAuthImageUnsupportedError
		if !errors.As(err, &unsupported) {
			t.Fatalf("status %d: err = %v，想要 OAuthImageUnsupportedError", status, err)
		}
		if unsupported.StatusCode != status || unsupported.OAuthProvider != "fake-login" || !strings.Contains(err.Error(), "API Key 的配置档") {
			t.Fatalf("status %d: 报错不够清楚：%v", status, err)
		}
	}
	// 同样的 404，没绑 OAuth 的配置档报错保持原样。
	server := newAuthRecorder(t, http.StatusNotFound)
	cfg := oauthOnlyConfig(server.URL)
	cfg.OAuthProvider = ""
	cfg.APIKey = "key-" + "configured0123456789"
	_, err := GenerateImage(context.Background(), cfg, ImageGenerateRequest{Prompt: "a cat"})
	var unsupported *OAuthImageUnsupportedError
	if err == nil || errors.As(err, &unsupported) {
		t.Fatalf("没绑 OAuth 的报错不该被改写：%v", err)
	}
}

// OAuth 取不到凭据、又没有 API Key 可回退时，直接报 OAuth 的原因，请求不发出去。
func TestOAuthOnlyProfileWithoutCredentialFailsClearly(t *testing.T) {
	server := newAuthRecorder(t, http.StatusOK)
	resolver := &fakeOAuthResolver{err: errors.New("llmauth: FakeLogin 还没有登录")}
	cfg := oauthOnlyConfig(server.URL)
	_, err := GenerateImage(context.Background(), cfg, ImageGenerateRequest{Prompt: "a cat"}, ClientOptionsFor(cfg, resolver)...)
	var credentialErr *OAuthCredentialError
	if !errors.As(err, &credentialErr) || !strings.Contains(err.Error(), "还没有登录") {
		t.Fatalf("err = %v，想要带着 OAuth 原因的 OAuthCredentialError", err)
	}
	var unsupported *OAuthImageUnsupportedError
	if errors.As(err, &unsupported) {
		t.Fatalf("没登录不该被说成接口不开放：%v", err)
	}
	if got := server.received("/images/generations"); len(got) != 0 {
		t.Fatalf("取不到凭据时不该把空鉴权的请求发出去：%v", got)
	}

	// 配了 API Key 兜底时照旧回退到 API Key。
	cfg.APIKey = "key-" + "fallback0123456789"
	if _, err := GenerateImage(context.Background(), cfg, ImageGenerateRequest{Prompt: "a cat"}, ClientOptionsFor(cfg, resolver)...); err != nil {
		t.Fatalf("有 API Key 兜底时应当成功：%v", err)
	}
	assertOnlyBearer(t, server.received("/images/generations"), cfg.APIKey)
}

func TestEmbedTextsOAuthOnlyProfileSendsOAuthToken(t *testing.T) {
	server := newAuthRecorder(t, http.StatusOK)
	resolver := &fakeOAuthResolver{token: testOAuthToken()}
	cfg := oauthOnlyConfig(server.URL)
	if _, _, err := EmbedTextsWithUsage(context.Background(), cfg, []string{"hello"}, ClientOptionsFor(cfg, resolver)...); err != nil {
		t.Fatalf("EmbedTextsWithUsage() error = %v", err)
	}
	assertOnlyBearer(t, server.received("/embeddings"), testOAuthToken())
}

// 聊天实际走的是注册表：落库的注册表文档要记住 OAuth 绑定，路由时按同一个钩子取凭据。
func TestRegistryOAuthOnlyProfileSendsOAuthToken(t *testing.T) {
	server := newAuthRecorder(t, http.StatusOK)
	resolver := &fakeOAuthResolver{token: testOAuthToken()}
	set := ProfileSet{Profiles: []Profile{{ID: "oauth-profile", Config: oauthOnlyConfig(server.URL)}}}
	built, selection, err := NewProviderRegistryFromProfiles(set)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := RegistryFromDocument(built.Document())
	if err != nil {
		t.Fatal(err)
	}
	if provider, ok := registry.PublicProvider("oauth-profile"); !ok || provider.OAuthProvider != "fake-login" {
		t.Fatalf("注册表文档丢了 OAuth 绑定：%#v", provider)
	}
	registry.SetClientOptions(func(cfg ProviderConfig) []ClientOption { return ClientOptionsFor(cfg, resolver) })
	request := ChatRequest{Messages: []ChatMessage{{Role: RoleUser, Content: "ping"}}}
	if _, err := registry.Generate(context.Background(), selection, request); err != nil {
		t.Fatalf("registry.Generate() error = %v", err)
	}
	if _, err := registry.ListModels(context.Background(), "oauth-profile"); err != nil {
		t.Fatalf("registry.ListModels() error = %v", err)
	}
	assertOnlyBearer(t, server.received("/chat/completions"), testOAuthToken())
	assertOnlyBearer(t, server.received("/model"), testOAuthToken())
}

// 没设钩子的注册表行为不变：只靠 OAuth 的配置档依旧因为没有凭据被拦下，
// 带 API Key 的配置档照常带 API Key。
func TestRegistryWithoutClientOptionsIsUnchanged(t *testing.T) {
	server := newAuthRecorder(t, http.StatusOK)
	apiKeyCfg := oauthOnlyConfig(server.URL)
	apiKeyCfg.OAuthProvider = ""
	apiKeyCfg.APIKey = "key-" + "configured0123456789"
	registry, selection, err := NewProviderRegistryFromProfiles(ProfileSet{Profiles: []Profile{{ID: "key-profile", Config: apiKeyCfg}}})
	if err != nil {
		t.Fatal(err)
	}
	var hookCalls atomic.Int32
	registry.SetClientOptions(func(cfg ProviderConfig) []ClientOption {
		hookCalls.Add(1)
		return ClientOptionsFor(cfg, &fakeOAuthResolver{token: testOAuthToken()})
	})
	if _, err := registry.Generate(context.Background(), selection, ChatRequest{Messages: []ChatMessage{{Role: RoleUser, Content: "ping"}}}); err != nil {
		t.Fatal(err)
	}
	assertOnlyBearer(t, server.received("/chat/completions"), apiKeyCfg.APIKey)
	if hookCalls.Load() == 0 {
		t.Fatal("钩子应当被问到，只是对没绑 OAuth 的配置档给出空选项")
	}
}

// Gemini：genai 没有 API Key 连客户端都建不起来。只靠 OAuth 的配置档接上凭据后，
// 生图请求带 OAuth 令牌，占位的 x-goog-api-key 不出网。
func TestGeminiGenerateImageOAuthOnlyProfileSendsOAuthToken(t *testing.T) {
	var gotAuth, gotGoogKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotGoogKey = r.Header.Get("Authorization"), r.Header.Get("X-Goog-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(oauthTestPNG) + `"}}]}}]}`))
	}))
	defer server.Close()
	resolver := &fakeOAuthResolver{token: testOAuthToken()}
	cfg := ProviderConfig{Provider: ProviderGemini, BaseURL: server.URL, Model: "gemini-image", ImageModel: "gemini-image", OAuthProvider: "fake-login"}
	resp, err := GenerateImage(context.Background(), cfg, ImageGenerateRequest{Prompt: "a cat"}, ClientOptionsFor(cfg, resolver)...)
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if len(resp.Images) != 1 || gotAuth != "Bearer "+testOAuthToken() || gotGoogKey != "" {
		t.Fatalf("images=%d auth=%q goog=%q", len(resp.Images), gotAuth, gotGoogKey)
	}
	// 没接上 OAuth 凭据的调用照旧报缺 API Key，不拿占位值去撞上游。
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	if _, err := NewClient(cfg); err == nil {
		t.Fatal("没接 OAuth 凭据的 Gemini 客户端应当照旧报缺 API Key")
	}
}

func TestListModelsOAuthOnlyGeminiProfileIsNotRejectedForMissingKey(t *testing.T) {
	var gotAuth, gotGoogKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotGoogKey = r.Header.Get("Authorization"), r.Header.Get("X-Goog-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-x","displayName":"Gemini X"}]}`))
	}))
	defer server.Close()
	resolver := &fakeOAuthResolver{token: testOAuthToken()}
	cfg := ProviderConfig{Provider: ProviderGemini, BaseURL: server.URL, Model: "gemini-x", OAuthProvider: "fake-login"}
	models, err := ListModels(context.Background(), cfg, ClientOptionsFor(cfg, resolver)...)
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 1 || gotAuth != "Bearer "+testOAuthToken() || gotGoogKey != "" {
		t.Fatalf("models=%v auth=%q goog=%q", models, gotAuth, gotGoogKey)
	}
}
