package assistant

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 运行时自己按配置档直接调 llm 包的那些路（生图、改图、embedding、拉模型列表、
// 注册表路由的对话）都要和聊天工厂从同一个钩子取客户端选项。

type oauthRuntimeResolver struct {
	token string
	calls atomic.Int32
}

func (f *oauthRuntimeResolver) Credential(context.Context, string) (llm.Credential, error) {
	f.calls.Add(1)
	return llm.Credential{Kind: llm.CredentialKindOAuth, Token: f.token, ReplaceProviderAuth: true}, nil
}

// registryBackedLLMProfileStore 和线上的持久化存储一样：注册表每次从落库的文档现建。
type registryBackedLLMProfileStore struct {
	stubLLMProfileStore
}

func (s *registryBackedLLMProfileStore) ProviderRegistry() (*llm.ProviderRegistry, error) {
	built, _, err := llm.NewProviderRegistryFromProfiles(s.set)
	if err != nil {
		return nil, err
	}
	return llm.RegistryFromDocument(built.Document())
}

type oauthRuntimeServer struct {
	*httptest.Server
	mu    sync.Mutex
	auths map[string][]string
}

func newOAuthRuntimeServer(t *testing.T) *oauthRuntimeServer {
	t.Helper()
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n0123456789"))
	server := &oauthRuntimeServer{auths: map[string][]string{}}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path[strings.LastIndex(r.URL.Path, "/v1/")+len("/v1/"):]
		server.mu.Lock()
		server.auths[key] = append(server.auths[key], r.Header.Get("Authorization"))
		server.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch key {
		case "images/generations", "images/edits":
			_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + png + `"}]}`))
		case "embeddings":
			_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]}]}`))
		case "model", "models":
			_, _ = w.Write([]byte(`{"data":[{"id":"chat-model","object":"model"}]}`))
		case "chat/completions":
			_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"chat-model","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Server.Close)
	return server
}

func (s *oauthRuntimeServer) received(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auths[key]...)
}

func oauthRuntimeProfile(baseURL string) llm.ProviderConfig {
	return llm.ProviderConfig{
		Provider:      llm.ProviderOpenAICompatible,
		APIFormat:     llm.APIFormatChatCompletions,
		BaseURL:       baseURL + "/v1",
		Model:         "chat-model",
		ImageModel:    "image-model",
		OAuthProvider: "fake-login",
	}
}

func newOAuthOptionsRuntime(t *testing.T, cfg llm.ProviderConfig, resolver llm.CredentialResolver) *Runtime {
	t.Helper()
	store := &registryBackedLLMProfileStore{stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "only", Name: "Only", Config: cfg}}}}}
	r := NewRuntime(BotConfig{ID: "bot"}.WithDefaults(), nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	r.SetLLMClientOptions(func(cfg llm.ProviderConfig) []llm.ClientOption {
		return llm.ClientOptionsFor(cfg, resolver)
	})
	return r
}

func requireBearer(t *testing.T, what string, got []string, token string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("%s：服务商一次请求都没收到", what)
	}
	for _, auth := range got {
		if auth != "Bearer "+token {
			t.Fatalf("%s：Authorization = %q，想要 Bearer %s", what, auth, token)
		}
	}
}

func TestRuntimeOAuthOnlyProfileCarriesOAuthOnEveryDirectCall(t *testing.T) {
	server := newOAuthRuntimeServer(t)
	token := "oauth-" + "runtime0123456789"
	resolver := &oauthRuntimeResolver{token: token}
	cfg := oauthRuntimeProfile(server.URL)
	r := newOAuthOptionsRuntime(t, cfg, resolver)
	ctx := context.Background()

	// 生图、改图：image 用途没单独配置，沿用 chat 的配置档。
	if _, _, err := r.generateImageWithFailover(ctx, llm.ImageGenerateRequest{Prompt: "a cat"}); err != nil {
		t.Fatalf("generateImageWithFailover() error = %v", err)
	}
	requireBearer(t, "生图", server.received("images/generations"), token)
	source := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n0123456789"))
	if _, _, err := r.editImageWithFailover(ctx, llm.ImageEditRequest{Prompt: "make it blue", Images: []string{source}}); err != nil {
		t.Fatalf("editImageWithFailover() error = %v", err)
	}
	requireBearer(t, "改图", server.received("images/edits"), token)

	// embedding。
	if _, err := r.embedTextsFunc()(ctx, cfg, []string{"hello"}); err != nil {
		t.Fatalf("embedTexts error = %v", err)
	}
	requireBearer(t, "embedding", server.received("embeddings"), token)

	// 没注入模型列表读取器时的默认实现。
	if _, err := r.llmModelLister()(ctx, cfg); err != nil {
		t.Fatalf("llmModelLister() error = %v", err)
	}
	requireBearer(t, "拉模型列表", server.received("model"), token)

	// 对话实际走注册表：落库文档要记住绑定，路由时接上同一个钩子。
	run := func(p LLMProvider) (string, error) {
		resp, err := p.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "ping"}}})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	}
	if text, err := r.runRawLLMProviderForGroup(ctx, llm.GroupChat, run); err != nil || text != "pong" {
		t.Fatalf("registry chat = %q, %v", text, err)
	}
	requireBearer(t, "注册表对话", server.received("chat/completions"), token)

	// 切换模型前的探测走的也是同一套。
	if err := r.probeModelSwitch(ctx, nil, "only", cfg, llmConfigRoleImage); err != nil {
		t.Fatalf("probeModelSwitch(image) error = %v", err)
	}
	requireBearer(t, "切换探测", server.received("images/generations"), token)
}

// 没绑 OAuth 的配置档：钩子给出空选项，请求带的就是配置里的 API Key，OAuth 层一次都不问。
func TestRuntimeProfileWithoutOAuthKeepsAPIKey(t *testing.T) {
	server := newOAuthRuntimeServer(t)
	resolver := &oauthRuntimeResolver{token: "oauth-" + "unused0123456789"}
	cfg := oauthRuntimeProfile(server.URL)
	cfg.OAuthProvider = ""
	cfg.APIKey = "key-" + "configured0123456789"
	r := newOAuthOptionsRuntime(t, cfg, resolver)
	ctx := context.Background()
	if _, _, err := r.generateImageWithFailover(ctx, llm.ImageGenerateRequest{Prompt: "a cat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.embedTextsFunc()(ctx, cfg, []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	requireBearer(t, "生图", server.received("images/generations"), cfg.APIKey)
	requireBearer(t, "embedding", server.received("embeddings"), cfg.APIKey)
	if resolver.calls.Load() != 0 {
		t.Fatal("没绑 OAuth 的配置档不该去问 OAuth 层")
	}
	if opts := r.llmClientOptionsFor(cfg); len(opts) != 0 {
		t.Fatalf("没绑 OAuth 时选项应为空：%d", len(opts))
	}
}
