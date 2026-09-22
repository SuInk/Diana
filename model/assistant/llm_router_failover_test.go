// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// decisionOnlyAdapter 复刻 TypeSafe System One：没有判断题表就直接拒，不出网。
type decisionOnlyAdapter struct{ calls int }

func (a *decisionOnlyAdapter) Generate(_ context.Context, _ llm.ModelDefinition, _ llm.ChatRequest) (llm.ChatResponse, error) {
	a.calls++
	return llm.ChatResponse{}, llm.ErrDecisionRequired
}

func (a *decisionOnlyAdapter) Stream(context.Context, llm.ModelDefinition, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	return nil, llm.ErrDecisionRequired
}

type textRegistryAdapter struct {
	calls int
	text  string
}

func (a *textRegistryAdapter) Generate(_ context.Context, _ llm.ModelDefinition, _ llm.ChatRequest) (llm.ChatResponse, error) {
	a.calls++
	return llm.ChatResponse{Text: a.text}, nil
}

func (a *textRegistryAdapter) Stream(_ context.Context, _ llm.ModelDefinition, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	events := make(chan llm.ChatEvent)
	close(events)
	return events, nil
}

func newTwoProviderRegistry(t *testing.T, first, second llm.LLMAdapter) (*llm.ProviderRegistry, llm.ProfileSet) {
	t.Helper()
	registry := llm.NewProviderRegistry()
	for _, item := range []struct {
		id      string
		adapter llm.LLMAdapter
	}{{"judge", first}, {"chat", second}} {
		if err := registry.RegisterProvider(llm.ProviderDefinition{ID: item.id, Name: item.id, Protocol: llm.ProtocolOpenAIResponses, Enabled: true}, item.adapter); err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterModel(llm.ModelDefinition{ID: item.id + ":" + item.id + "-model", ProviderID: item.id, ModelID: item.id + "-model", Name: item.id + "-model"}); err != nil {
			t.Fatal(err)
		}
	}
	return registry, llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "judge", Group: "judge-group", Config: llm.ProviderConfig{Model: "judge-model", Models: []llm.ModelInfo{{ID: "judge-model"}}}},
		{ID: "chat", Group: "chat-group", Config: llm.ProviderConfig{Model: "chat-model", Models: []llm.ModelInfo{{ID: "chat-model"}}}},
	}}
}

// 判定链路必须和对话链路一样走降级：绑定里配的 fallbacks 以前在注册表路径上
// 完全没被用过，于是把 intent 绑到只做判断的模型会让判定整条失败。
func TestRouterHonorsRoleFallbacks(t *testing.T) {
	judge := &decisionOnlyAdapter{}
	chat := &textRegistryAdapter{text: "fallback answered"}
	registry, profiles := newTwoProviderRegistry(t, judge, chat)
	runtime := NewRuntime(BotConfig{ModelRoles: map[string]ModelRole{
		"intent": {
			ProfileID: "judge", Model: "judge-model",
			Fallbacks: []ModelRole{{ProfileID: "chat", Model: "chat-model"}},
		},
	}}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: profiles}, nil, nil, nil)
	runtime.SetLLMProviderRegistry(registry)

	result, err := runtime.runLLMRouterProvider(context.Background(), func(provider LLMProvider) (string, error) {
		response, generateErr := provider.Generate(context.Background(), llm.GenerateRequest{})
		if generateErr != nil {
			return "", generateErr
		}
		return response.Text, nil
	})
	if err != nil || result != "fallback answered" {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if judge.calls == 0 || chat.calls == 0 {
		t.Fatalf("judge calls=%d chat calls=%d，两档都该被试过", judge.calls, chat.calls)
	}
}

// 「只跑一次」说的是同一档不重试，不是不许降级：摘要、语义承接这些用途走的
// 正是 Once 变体，它们同样要能落到下一档。
func TestRouterOnceStillFallsOverToNextProfile(t *testing.T) {
	judge := &decisionOnlyAdapter{}
	chat := &textRegistryAdapter{text: "fallback answered"}
	registry, profiles := newTwoProviderRegistry(t, judge, chat)
	runtime := NewRuntime(BotConfig{ModelRoles: map[string]ModelRole{
		"intent": {
			ProfileID: "judge", Model: "judge-model",
			Fallbacks: []ModelRole{{ProfileID: "chat", Model: "chat-model"}},
		},
	}}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: profiles}, nil, nil, nil)
	runtime.SetLLMProviderRegistry(registry)

	result, err := runtime.runLLMRouterProviderOnce(context.Background(), func(provider LLMProvider) (string, error) {
		response, generateErr := provider.Generate(context.Background(), llm.GenerateRequest{})
		if generateErr != nil {
			return "", generateErr
		}
		return response.Text, nil
	})
	if err != nil || result != "fallback answered" {
		t.Fatalf("result=%q err=%v", result, err)
	}
	if judge.calls != 1 {
		t.Fatalf("judge calls=%d，同一档不该重试", judge.calls)
	}
}
