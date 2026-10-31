package assistant

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 生图、改图、embedding、模型列表和注册表对话都应使用提供商配置里的 API Key。

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

type apiKeyRuntimeServer struct {
	*httptest.Server
	mu    sync.Mutex
	auths map[string][]string
}

func newAPIKeyRuntimeServer(t *testing.T) *apiKeyRuntimeServer {
	t.Helper()
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n0123456789"))
	server := &apiKeyRuntimeServer{auths: map[string][]string{}}
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

func (s *apiKeyRuntimeServer) received(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auths[key]...)
}

func apiKeyRuntimeProfile(baseURL string) llm.ProviderConfig {
	return llm.ProviderConfig{
		Provider:   llm.ProviderOpenAICompatible,
		APIFormat:  llm.APIFormatChatCompletions,
		BaseURL:    baseURL + "/v1",
		Model:      "chat-model",
		ImageModel: "image-model",
		APIKey:     "key-runtime0123456789",
	}
}

func newAPIKeyRuntime(t *testing.T, cfg llm.ProviderConfig) *Runtime {
	t.Helper()
	store := &registryBackedLLMProfileStore{stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "only", Name: "Only", Config: cfg}}}}}
	r := NewRuntime(BotConfig{ID: "bot"}.WithDefaults(), nilChannel{}, NewPluginManager(), store, nil, nil, nil)
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

func TestRuntimeCarriesAPIKeyOnEveryDirectCall(t *testing.T) {
	server := newAPIKeyRuntimeServer(t)
	cfg := apiKeyRuntimeProfile(server.URL)
	r := newAPIKeyRuntime(t, cfg)
	token := cfg.APIKey
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

	// 对话实际走注册表：落库文档里的提供商 Key 必须传到真实请求。
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
