// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestMemoryUsesIntentRoleInsteadOfActiveImageProfile(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		ActiveID: "image-p",
		Profiles: []llm.Profile{
			{ID: "chat-p", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "chat-model"}},
			{ID: "intent-p", Group: llm.GroupIntent, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "intent-model"}},
			{ID: "image-p", Group: llm.GroupImage, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "gpt-image-2"}},
		},
	}}
	runtime := NewRuntime(BotConfig{ModelRoles: map[string]ModelRole{
		"chat":   {ProfileID: "chat-p", Model: "chat-model"},
		"intent": {ProfileID: "intent-p", Model: "intent-model"},
	}}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	selected := ""
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		selected = cfg.Model
		return &capturingLLMProvider{reply: `{}`}, nil
	})
	_, err := runtime.runLLMMemoryProvider(context.Background(), func(client LLMProvider) (string, error) {
		resp, err := client.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "extract"}}})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "intent-model" {
		t.Fatalf("memory selected %q, want intent-model", selected)
	}
}

func TestMemoryWithoutRolesSkipsActiveImageProfile(t *testing.T) {
	store := &stubLLMProfileStore{set: groupFallbackSet()}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	selected := ""
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		selected = cfg.Model
		return &capturingLLMProvider{reply: `{}`}, nil
	})
	_, err := runtime.runLLMMemoryProvider(context.Background(), func(client LLMProvider) (string, error) {
		resp, err := client.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "extract"}}})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "gpt-chat" {
		t.Fatalf("memory selected %q, want gpt-chat", selected)
	}
}

func groupFallbackRegistry(t *testing.T) *llm.ProviderRegistry {
	t.Helper()
	registry, err := llm.RegistryFromDocument(llm.ProviderRegistryDocument{
		Version: 1,
		Providers: []llm.ProviderDefinition{
			{ID: "chat-p", Name: "对话", Protocol: llm.ProtocolOpenAIResponses, BaseURL: "https://example.invalid/v1", APIKey: "sk-c", Enabled: true},
			{ID: "image-p", Name: "生图", Protocol: llm.ProtocolOpenAIResponses, BaseURL: "https://example.invalid/v1", APIKey: "sk-i", Enabled: true},
		},
		Models: []llm.ModelDefinition{
			{ID: "chat-p:gpt-chat", ProviderID: "chat-p", ModelID: "gpt-chat", Name: "gpt-chat"},
			{ID: "image-p:gpt-image", ProviderID: "image-p", ModelID: "gpt-image", Name: "gpt-image"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

// 对话配置在默认分组、生图配置在 image 组，激活的是生图那套。
func groupFallbackSet() llm.ProfileSet {
	return llm.ProfileSet{
		ActiveID: "image-p",
		Profiles: []llm.Profile{
			{ID: "chat-p", Name: "对话", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-c", Model: "gpt-chat"}},
			{ID: "image-p", Name: "生图", Group: llm.GroupImage, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-i", Model: "gpt-image"}},
		},
	}
}

// 激活的是生图配置时，聊天调用不能拿生图模型去发文本请求。
// 这是原来的实际行为：选到 image-p / gpt-image，而日志里 provider 和 model 都「正常」。
func TestChatSelectionNeverFallsBackToCrossGroupActiveProfile(t *testing.T) {
	selection, ok, err := registrySelectionForGroup(groupFallbackRegistry(t), groupFallbackSet(), nil, llm.GroupChat, "")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if selection.ProviderID == "image-p" {
		t.Fatalf("聊天调用选到了生图配置：%+v", selection)
	}
	if selection.ProviderID != "chat-p" {
		t.Fatalf("selection = %+v，应当退回默认分组里的对话配置", selection)
	}
}

// 同组的激活配置照旧生效：默认分组里并排放两套时，选哪套仍然由「激活配置」决定，
// 不能因为这次修复变成永远取第一个。
func TestChatSelectionStillHonoursActiveProfileWithinGroup(t *testing.T) {
	set := llm.ProfileSet{
		ActiveID: "chat-b",
		Profiles: []llm.Profile{
			{ID: "chat-a", Name: "对话A", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-a", Model: "gpt-chat"}},
			{ID: "chat-b", Name: "对话B", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-b", Model: "gpt-chat"}},
		},
	}
	registry, err := llm.RegistryFromDocument(llm.ProviderRegistryDocument{
		Version: 1,
		Providers: []llm.ProviderDefinition{
			{ID: "chat-a", Name: "对话A", Protocol: llm.ProtocolOpenAIResponses, BaseURL: "https://example.invalid/v1", APIKey: "sk-a", Enabled: true},
			{ID: "chat-b", Name: "对话B", Protocol: llm.ProtocolOpenAIResponses, BaseURL: "https://example.invalid/v1", APIKey: "sk-b", Enabled: true},
		},
		Models: []llm.ModelDefinition{
			{ID: "chat-a:gpt-chat", ProviderID: "chat-a", ModelID: "gpt-chat", Name: "gpt-chat"},
			{ID: "chat-b:gpt-chat", ProviderID: "chat-b", ModelID: "gpt-chat", Name: "gpt-chat"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection, ok, err := registrySelectionForGroup(registry, set, nil, llm.GroupChat, "")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if selection.ProviderID != "chat-b" {
		t.Fatalf("selection = %+v，同组时应当尊重激活配置", selection)
	}
}

// 非聊天用途仍然先按分组找，不受这次改动影响。
func TestNonChatSelectionPrefersItsOwnGroup(t *testing.T) {
	selection, ok, err := registrySelectionForGroup(groupFallbackRegistry(t), groupFallbackSet(), nil, llm.GroupImage, "")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if selection.ProviderID != "image-p" {
		t.Fatalf("selection = %+v，生图调用应当用生图配置", selection)
	}
}

// 本分组一套配置都没有时宁可报「没有可用配置」，也不要跨组硬接。
func TestSelectionReportsNoProfileRatherThanCrossingGroups(t *testing.T) {
	set := llm.ProfileSet{
		ActiveID: "image-p",
		Profiles: []llm.Profile{
			{ID: "image-p", Name: "生图", Group: llm.GroupImage, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-i", Model: "gpt-image"}},
		},
	}
	selection, ok, err := registrySelectionForGroup(groupFallbackRegistry(t), set, nil, llm.GroupChat, "")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("没有对话配置时不该给出选择：%+v", selection)
	}
}

// 分组名和角色名是两套命名空间：聊天配置的分组是 "default"，而角色键是 "chat"。
// 原先同一个变量串着用，导致「先在同组里找」查的是不存在的 "chat" 分组。
func TestChatProfilesLiveInDefaultGroupNotChatGroup(t *testing.T) {
	set := groupFallbackSet()
	if got := llmProfilesInGroup(set, "chat"); len(got) != 0 {
		t.Fatalf("居然存在 chat 分组：%+v", got)
	}
	if got := llmProfilesInGroup(set, llm.GroupChat); len(got) != 1 || got[0].ID != "chat-p" {
		t.Fatalf("默认分组里应当有那套对话配置：%+v", got)
	}
	if llm.GroupChat != "default" {
		t.Fatalf("llm.GroupChat = %q，这条测试的前提变了", llm.GroupChat)
	}
}

// 拦的是「干不了这活」，不是「分组不一样」。视觉、意图没单独配置时回落到对话配置
// 是正常且有用的（大多数对话模型本来就能看图），一刀切按分组拦会把它一起拦掉——
// 这条测试就是防止以后又收紧成那样。
func TestVisionStillFallsBackToChatActiveProfile(t *testing.T) {
	registry, err := llm.RegistryFromDocument(llm.ProviderRegistryDocument{
		Version: 1,
		Providers: []llm.ProviderDefinition{
			{ID: "chat-p", Name: "对话", Protocol: llm.ProtocolOpenAIResponses, BaseURL: "https://example.invalid/v1", APIKey: "sk-c", Enabled: true},
		},
		Models: []llm.ModelDefinition{
			{ID: "chat-p:gpt-chat", ProviderID: "chat-p", ModelID: "gpt-chat", Name: "gpt-chat"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	set := llm.ProfileSet{
		ActiveID: "chat-p",
		Profiles: []llm.Profile{
			{ID: "chat-p", Name: "对话", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-c", Model: "gpt-chat"}},
		},
	}
	for _, group := range []string{llm.GroupVision, llm.GroupIntent} {
		selection, ok, err := registrySelectionForGroup(registry, set, nil, group, "")
		if err != nil || !ok {
			t.Fatalf("group %s: ok=%v err=%v", group, ok, err)
		}
		if selection.ProviderID != "chat-p" {
			t.Fatalf("group %s selected %+v，应当回落到对话配置", group, selection)
		}
	}
}

func TestProfileGroupServesOnlyBlocksSinglePurposeGroups(t *testing.T) {
	// 对话模型之间随便顶替。
	for _, pair := range [][2]string{
		{llm.GroupChat, llm.GroupVision},
		{llm.GroupVision, llm.GroupChat},
		{llm.GroupIntent, llm.GroupChat},
		{llm.GroupChat, llm.GroupIntent},
	} {
		if !profileGroupServes(pair[0], pair[1]) {
			t.Fatalf("%s 应当能接 %s 的调用", pair[0], pair[1])
		}
	}
	// 生图和嵌入是单一用途：既不能拿去干别的，别的也不能拿来干它们的活。
	for _, pair := range [][2]string{
		{llm.GroupImage, llm.GroupChat},
		{llm.GroupChat, llm.GroupImage},
		{llm.GroupEmbedding, llm.GroupChat},
		{llm.GroupChat, llm.GroupEmbedding},
		{llm.GroupImage, llm.GroupVision},
	} {
		if profileGroupServes(pair[0], pair[1]) {
			t.Fatalf("%s 不该拿去接 %s 的调用", pair[0], pair[1])
		}
	}
	// 同组永远可以，哪怕是单一用途的。
	for _, group := range []string{llm.GroupImage, llm.GroupEmbedding, llm.GroupChat} {
		if !profileGroupServes(group, group) {
			t.Fatalf("%s 同组应当可用", group)
		}
	}
}
