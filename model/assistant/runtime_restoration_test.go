// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

type restoredModelProvider struct {
	model string
	err   error
}

type restoredDynamicAgentProvider struct {
	model    string
	used     *[]string
	requests *[]llm.GenerateRequest
}

func (p restoredDynamicAgentProvider) Generate(_ context.Context, request llm.GenerateRequest) (*llm.GenerateResponse, error) {
	*p.used = append(*p.used, p.model)
	*p.requests = append(*p.requests, request)
	text := `{"action":"tool","tool":"history_images","input":{}}`
	if p.model == "vision-model" {
		text = `{"action":"final","content":"视觉细节已读取"}`
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: p.model, Text: text}, nil
}

type restoredRichImageTool struct{}

func (*restoredRichImageTool) Name() string        { return "history_images" }
func (*restoredRichImageTool) Description() string { return "load one historical image" }
func (*restoredRichImageTool) Run(context.Context, map[string]any) (string, error) {
	return `{"loaded":1}`, nil
}
func (*restoredRichImageTool) ToolResultParts(string) []llm.ContentPart {
	return []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: "data:image/png;base64,YQ==", Detail: "auto"}}
}

func (p restoredModelProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: p.model, Text: "ok from " + p.model}, nil
}

func TestRestoredGenerateReplyRoutesVisionGroup(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		ActiveID: "chat",
		Profiles: []llm.Profile{
			{ID: "chat", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "chat-key", Model: "chat-model"}},
			{ID: "vision", Group: llm.GroupVision, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "vision-key", Model: "vision-model"}},
		},
	}}
	var used []string
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		used = append(used, cfg.Model)
		return restoredModelProvider{model: cfg.Model}, nil
	})

	imageMessages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: "https://example.com/image.png"}}}}
	reply, err := runtime.generateReply(context.Background(), BotConfig{}, MessageEvent{}, RelationshipPolicy{}, imageMessages, nil)
	if err != nil || reply != "ok from vision-model" {
		t.Fatalf("image reply=%q err=%v", reply, err)
	}
	reply, err = runtime.generateReply(context.Background(), BotConfig{}, MessageEvent{}, RelationshipPolicy{}, []llm.Message{{Role: llm.RoleUser, Content: "hello"}}, nil)
	if err != nil || reply != "ok from chat-model" {
		t.Fatalf("chat reply=%q err=%v", reply, err)
	}
	if len(used) != 2 || used[0] != "vision-model" || used[1] != "chat-model" {
		t.Fatalf("used models=%v", used)
	}
	if store.set.ActiveID != "chat" {
		t.Fatalf("vision routing changed active profile to %q", store.set.ActiveID)
	}
}

func TestAgentSwitchesFromChatToVisionAfterRichToolResult(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		ActiveID: "chat",
		Profiles: []llm.Profile{
			{ID: "chat", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "chat-key", Model: "chat-model"}},
			{ID: "vision", Group: llm.GroupVision, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "vision-key", Model: "vision-model"}},
		},
	}}
	cfg := BotConfig{AgentEnabled: true, AgentMaxSteps: 2}
	var used []string
	var requests []llm.GenerateRequest
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(providerCfg llm.ProviderConfig) (LLMProvider, error) {
		return restoredDynamicAgentProvider{model: providerCfg.Model, used: &used, requests: &requests}, nil
	})
	registry := agent.NewToolRegistry(&restoredRichImageTool{})
	reply, err := runtime.generateReply(
		context.Background(),
		cfg,
		MessageEvent{Kind: EventKindPrivate, UserID: "owner", MessageID: "question"},
		RelationshipPolicy{Owner: true},
		[]llm.Message{{Role: llm.RoleUser, Content: "需要查看历史图片细节"}},
		registry,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "视觉细节已读取" || strings.Join(used, ",") != "chat-model,vision-model" {
		t.Fatalf("reply=%q used=%v", reply, used)
	}
	if len(requests) != 2 || requestHasAnyImage(requests[0]) || !requestHasAnyImage(requests[1]) {
		t.Fatalf("dynamic agent requests = %#v", requests)
	}
}

func TestRestoredModelRoleGroupBindingFailsOver(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		ActiveID: "primary-a",
		Profiles: []llm.Profile{
			{ID: "primary-a", Group: "primary", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-a", Model: "old-a"}},
			{ID: "primary-b", Group: "primary", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-b", Model: "old-b"}},
		},
	}}
	cfg := BotConfig{ModelRoles: map[string]ModelRole{"chat": {Group: "primary", Model: "bound-model"}}}
	var attempts []string
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(providerCfg llm.ProviderConfig) (LLMProvider, error) {
		attempts = append(attempts, providerCfg.APIKey+"/"+providerCfg.Model)
		if providerCfg.APIKey == "key-a" {
			return restoredModelProvider{err: fmt.Errorf("401 unauthorized")}, nil
		}
		return restoredModelProvider{model: providerCfg.Model}, nil
	})

	reply, err := runtime.generateReply(context.Background(), cfg, MessageEvent{}, RelationshipPolicy{}, []llm.Message{{Role: llm.RoleUser, Content: "hello"}}, nil)
	if err != nil || reply != "ok from bound-model" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if len(attempts) != 2 || attempts[0] != "key-a/bound-model" || attempts[1] != "key-b/bound-model" {
		t.Fatalf("attempts=%v", attempts)
	}
	if store.set.ActiveID != "primary-a" {
		t.Fatalf("role routing changed active profile to %q", store.set.ActiveID)
	}
}

func TestModelRoleGroupFiltersKnownIncompatibleProviders(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "incompatible", Group: "primary", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "skip", Model: "old", Models: []llm.ModelInfo{{ID: "other-model"}}}},
		{ID: "compatible", Group: "primary", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "use", APIStyle: llm.APIStyleResponses, Model: "old", Models: []llm.ModelInfo{{ID: "target-model"}}}},
		{ID: "also-incompatible", Group: "primary", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "skip-too", Model: "old", Models: []llm.ModelInfo{{ID: "third-model"}}}},
	}}}
	cfg := BotConfig{ModelRoles: map[string]ModelRole{"chat": {Group: "primary", Model: "target-model"}}}
	var attempts []string
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(providerCfg llm.ProviderConfig) (LLMProvider, error) {
		attempts = append(attempts, providerCfg.APIKey+"/"+string(providerCfg.APIStyle)+"/"+providerCfg.Model)
		return restoredModelProvider{model: providerCfg.Model}, nil
	})
	reply, err := runtime.generateReply(context.Background(), cfg, MessageEvent{}, RelationshipPolicy{}, []llm.Message{{Role: llm.RoleUser, Content: "hello"}}, nil)
	if err != nil || reply != "ok from target-model" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if got := strings.Join(attempts, ","); got != "use/responses/target-model" {
		t.Fatalf("attempts=%q", got)
	}
}

func TestModelRoleGroupFailsOverOnModelNotFoundAndPreservesAPIStyle(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "unknown-a", Group: "primary", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "a", APIStyle: llm.APIStyleResponses, Model: "old-a"}},
		{ID: "unknown-b", Group: "primary", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "b", APIStyle: llm.APIStyleChatCompletions, Model: "old-b"}},
	}}}
	cfg := BotConfig{ModelRoles: map[string]ModelRole{"chat": {Group: "primary", Model: "target-model"}}}
	var attempts []string
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(providerCfg llm.ProviderConfig) (LLMProvider, error) {
		attempts = append(attempts, providerCfg.APIKey+"/"+string(providerCfg.APIStyle))
		if providerCfg.APIKey == "a" {
			return restoredModelProvider{err: errors.New(`status 404: {"type":"model_not_found"}`)}, nil
		}
		return restoredModelProvider{model: providerCfg.Model}, nil
	})
	reply, err := runtime.generateReply(context.Background(), cfg, MessageEvent{}, RelationshipPolicy{}, []llm.Message{{Role: llm.RoleUser, Content: "hello"}}, nil)
	if err != nil || reply != "ok from target-model" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if got := strings.Join(attempts, ","); got != "a/responses,b/chat_completions" {
		t.Fatalf("attempts=%q", got)
	}
}

func TestModelRoleGroupRejectsModelUnsupportedByEveryKnownProvider(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "a", Group: "primary", Config: llm.ProviderConfig{Models: []llm.ModelInfo{{ID: "model-a"}}}},
		{ID: "b", Group: "primary", Config: llm.ProviderConfig{Models: []llm.ModelInfo{{ID: "model-b"}}}},
	}}}
	cfg := BotConfig{ModelRoles: map[string]ModelRole{"chat": {Group: "primary", Model: "target-model"}}}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	factoryCalls := 0
	runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) {
		factoryCalls++
		return restoredModelProvider{}, nil
	})
	_, err := runtime.generateReply(context.Background(), cfg, MessageEvent{}, RelationshipPolicy{}, []llm.Message{{Role: llm.RoleUser, Content: "hello"}}, nil)
	if err == nil || !strings.Contains(err.Error(), `has no provider supporting model "target-model"`) {
		t.Fatalf("err=%v", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory calls=%d, want 0", factoryCalls)
	}
}

func TestSingleProfileModelRoleDoesNotFailOverOnModelNotFound(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "bound", Group: "primary", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "bound", Model: "old"}},
		{ID: "other", Group: "primary", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "other", Model: "target-model"}},
	}}}
	cfg := BotConfig{ModelRoles: map[string]ModelRole{"chat": {ProfileID: "bound", Model: "target-model"}}}
	var attempts []string
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(providerCfg llm.ProviderConfig) (LLMProvider, error) {
		attempts = append(attempts, providerCfg.APIKey)
		return restoredModelProvider{err: errors.New(`status 404: {"type":"model_not_found"}`)}, nil
	})
	_, err := runtime.generateReply(context.Background(), cfg, MessageEvent{}, RelationshipPolicy{}, []llm.Message{{Role: llm.RoleUser, Content: "hello"}}, nil)
	if err == nil || !isModelUnavailableLLMError(err) {
		t.Fatalf("err=%v", err)
	}
	if got := strings.Join(attempts, ","); got != "bound" {
		t.Fatalf("attempts=%q, single-profile role must remain strict", got)
	}
}

func TestRestoredIntentAndImageRoleBindings(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		ActiveID: "chat",
		Profiles: []llm.Profile{
			{ID: "chat", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "chat-key", Model: "chat-model", ImageModel: "chat-image"}},
			{ID: "special-a", Group: "special", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "special-a", Model: "old-a", ImageModel: "old-image-a"}},
			{ID: "special-b", Group: "special", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "special-b", Model: "old-b", ImageModel: "old-image-b"}},
		},
	}}
	cfg := BotConfig{ModelRoles: map[string]ModelRole{
		"intent": {ProfileID: "special-a", Model: "intent-model"},
		"image":  {Group: "special", Model: "image-model"},
	}}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	var usedModel string
	runtime.SetLLMProviderConfigFactory(func(providerCfg llm.ProviderConfig) (LLMProvider, error) {
		usedModel = providerCfg.Model
		return restoredModelProvider{model: providerCfg.Model}, nil
	})
	_, err := runtime.runLLMRouterProvider(context.Background(), func(client LLMProvider) (string, error) {
		resp, runErr := client.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "route"}}})
		if runErr != nil {
			return "", runErr
		}
		return resp.Text, nil
	})
	if err != nil || usedModel != "intent-model" {
		t.Fatalf("intent model=%q err=%v", usedModel, err)
	}
	imageConfigs := runtime.imageProviderConfigs()
	if len(imageConfigs) != 2 || imageConfigs[0].ImageModel != "image-model" || imageConfigs[1].ImageModel != "image-model" {
		t.Fatalf("image configs=%#v", imageConfigs)
	}
}

type restoredStatusChannel struct {
	nilChannel
	status ChannelStatus
}

func (c restoredStatusChannel) Status() ChannelStatus { return c.status }

type restoredConfigSaver struct {
	saved BotConfig
	calls int
}

func (s *restoredConfigSaver) SaveBotConfig(cfg BotConfig) {
	s.saved = cfg
	s.calls++
}

func TestRestoredRuntimeLearnsBotIdentityOnce(t *testing.T) {
	saver := &restoredConfigSaver{}
	runtime := NewRuntime(BotConfig{}, restoredStatusChannel{status: ChannelStatus{Connected: true, SelfID: "1784464"}}, NewPluginManager(), nil, nil, saver, nil)
	if got := runtime.Status().Config.BotAccount; got != "1784464" {
		t.Fatalf("BotAccount=%q", got)
	}
	runtime.Status()
	if saver.calls != 1 || saver.saved.BotAccount != "1784464" {
		t.Fatalf("saved=%#v calls=%d", saver.saved, saver.calls)
	}

	explicitSaver := &restoredConfigSaver{}
	explicit := NewRuntime(BotConfig{BotAccount: "10001"}, restoredStatusChannel{status: ChannelStatus{SelfID: "1784464"}}, NewPluginManager(), nil, nil, explicitSaver, nil)
	if got := explicit.Status().Config.BotAccount; got != "10001" || explicitSaver.calls != 0 {
		t.Fatalf("explicit BotAccount=%q save calls=%d", got, explicitSaver.calls)
	}
}

func TestRestoredRuntimeTriggersSupportedSocialLinksOnly(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "456"}
	if !runtime.shouldHandle(event, "https://www.bilibili.com/video/BV1xx411c7mD") {
		t.Fatal("supported social link should trigger the resolver")
	}
	if runtime.shouldHandle(event, "https://example.com/article") {
		t.Fatal("ordinary link should not trigger a group reply")
	}
	if _, err := runtime.plugins.SetEnabled(resolverPluginID, false); err != nil {
		t.Fatalf("SetEnabled() error = %v", err)
	}
	if runtime.shouldHandle(event, "https://youtu.be/example") {
		t.Fatal("disabled resolver should not trigger")
	}
}

func TestRestoredPrivateMessageInterceptorConsumesBeforeChat(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	called := false
	runtime.SetPrivateMessageInterceptor(func(_ context.Context, event MessageEvent, text string) bool {
		called = event.Kind == EventKindPrivate && text == "123456"
		return called
	})
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", RawMessage: "123456"}
	if err := runtime.HandleEvent(context.Background(), event); err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}
	if !called {
		t.Fatal("private message interceptor was not called")
	}
	if history := runtime.contextHistory(event); len(history) != 0 {
		t.Fatalf("consumed login message entered chat history: %#v", history)
	}
	recent := runtime.Status().RecentEvents
	if len(recent) != 1 || recent[0].Text != "[控制台登录配对]" || !recent[0].Handled {
		t.Fatalf("recent events = %#v", recent)
	}
}

func TestRestoredModelRoleProfileBindingsAndVisionFallback(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		ActiveID: "chan-a",
		Profiles: []llm.Profile{
			{ID: "chan-a", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-a", Model: "old-default"}},
			{ID: "chan-b", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "key-b"}},
		},
	}}
	cfg := BotConfig{ModelRoles: map[string]ModelRole{
		"chat":   {ProfileID: "chan-a", Model: "gpt-chat"},
		"vision": {ProfileID: "chan-b", Model: "gpt-vision"},
	}}
	var used []string
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(providerCfg llm.ProviderConfig) (LLMProvider, error) {
		used = append(used, providerCfg.Model+"@"+providerCfg.APIKey)
		return restoredModelProvider{model: providerCfg.Model}, nil
	})
	textMessages := []llm.Message{{Role: llm.RoleUser, Content: "hello"}}
	imageMessages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: "https://example.com/image.png"}}}}
	if _, err := runtime.generateReply(context.Background(), cfg, MessageEvent{}, RelationshipPolicy{}, textMessages, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.generateReply(context.Background(), cfg, MessageEvent{}, RelationshipPolicy{}, imageMessages, nil); err != nil {
		t.Fatal(err)
	}
	if len(used) != 2 || used[0] != "gpt-chat@key-a" || used[1] != "gpt-vision@key-b" {
		t.Fatalf("used = %v", used)
	}

	used = nil
	fallbackCfg := BotConfig{ModelRoles: map[string]ModelRole{"chat": {ProfileID: "chan-b", Model: "only-chat"}}}
	fallback := NewRuntime(fallbackCfg, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	fallback.SetLLMProviderConfigFactory(func(providerCfg llm.ProviderConfig) (LLMProvider, error) {
		used = append(used, providerCfg.Model)
		return restoredModelProvider{model: providerCfg.Model}, nil
	})
	if _, err := fallback.generateReply(context.Background(), fallbackCfg, MessageEvent{}, RelationshipPolicy{}, imageMessages, nil); err != nil {
		t.Fatal(err)
	}
	if len(used) != 1 || used[0] != "only-chat" {
		t.Fatalf("fallback used = %v", used)
	}
}

func TestRestoredLLMConfigUsesStructuredAgentTool(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		ActiveID: "main",
		Profiles: []llm.Profile{{
			ID:   "main",
			Name: "主配置",
			Config: llm.ProviderConfig{
				Provider: llm.ProviderOpenAICompatible,
				APIKey:   "test-key",
				Model:    "old-model",
			},
		}},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMModelLister(func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{ID: "old-model"}, {ID: "gpt-4.1-mini"}}, nil
	})

	output, err := newDianaLLMConfigTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "owner"}).Run(context.Background(), map[string]any{"model": "gpt-4.1-mini"})
	// 这个部署没配模型分配，激活配置的默认模型就是它实际在用的，所以仍然改配置本身。
	if err != nil || !strings.Contains(output, "已把对话模型换成 gpt-4.1-mini") || store.Current().Model != "gpt-4.1-mini" {
		t.Fatalf("output=%q err=%v config=%#v", output, err, store.Current())
	}
	plugins := NewDefaultPluginManager()
	state, exposed := plugins.Get(llmConfigPluginID)
	if !exposed || !state.Installed || !state.Enabled {
		t.Fatalf("no-op LLM config plugin state=%#v exposed=%v", state, exposed)
	}
	resp, err := plugins.RunOneWithOverrides(context.Background(), llmConfigPluginID, PluginRequest{
		Event:    MessageEvent{Kind: EventKindPrivate, UserID: "owner"},
		OwnerID:  "owner",
		Text:     "把模型换回 old-model",
		LLMStore: store,
	}, nil)
	if err != nil || resp != nil || store.Current().Model != "gpt-4.1-mini" {
		t.Fatalf("natural-language plugin run resp=%#v err=%v config=%#v", resp, err, store.Current())
	}
}
