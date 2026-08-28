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
		Profiles: []llm.Profile{
			{ID: "chat-p", Name: "对话", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-c", Model: "gpt-chat"}},
			{ID: "image-p", Name: "生图", Group: llm.GroupImage, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-i", Model: "gpt-image"}},
		},
	}
}

// 激活的是生图配置时，聊天调用不能拿生图模型去发文本请求。
// 这是原来的实际行为：选到 image-p / gpt-image，而日志里 provider 和 model 都「正常」。
func TestChatSelectionNeverFallsBackToCrossGroupActiveProfile(t *testing.T) {
	selection, ok, err := registrySelectionForGroup(groupFallbackRegistry(t), groupFallbackSet(), nil, "", llm.GroupChat, "")
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

// 同组里并排放两套时按列表顺序取第一条。这条原来断言的是「由激活配置决定」——
// 那个隐藏状态会让列表顺序和实跑顺序对不上，去掉之后所见即所得。
func TestChatSelectionTakesFirstProfileWithinGroup(t *testing.T) {
	set := llm.ProfileSet{
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
	selection, ok, err := registrySelectionForGroup(registry, set, nil, "", llm.GroupChat, "")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if selection.ProviderID != "chat-a" {
		t.Fatalf("selection = %+v，同组时应当取列表第一条", selection)
	}
}

// 非聊天用途仍然先按分组找，不受这次改动影响。
func TestNonChatSelectionPrefersItsOwnGroup(t *testing.T) {
	selection, ok, err := registrySelectionForGroup(groupFallbackRegistry(t), groupFallbackSet(), nil, "", llm.GroupImage, "")
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
		Profiles: []llm.Profile{
			{ID: "image-p", Name: "生图", Group: llm.GroupImage, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-i", Model: "gpt-image"}},
		},
	}
	selection, ok, err := registrySelectionForGroup(groupFallbackRegistry(t), set, nil, "", llm.GroupChat, "")
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
// 这条测试就是防止以后又收紧成那样。去掉「激活配置」时就差点又踩一次。
func TestVisionStillFallsBackToChatProfile(t *testing.T) {
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
		Profiles: []llm.Profile{
			{ID: "chat-p", Name: "对话", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-c", Model: "gpt-chat"}},
		},
	}
	for _, group := range []string{llm.GroupVision, llm.GroupIntent} {
		selection, ok, err := registrySelectionForGroup(registry, set, nil, "", group, "")
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

// 默认分组为空时会拿列表第一条兜底。第一条正好是生图配置的话，不加能力检查就会
// 拿 gpt-image 去发文本请求——「激活配置」时代踩过这个坑，概念去掉之后兜底这条
// 路径仍然存在，检查得跟着留下。
func TestChatFallbackRefusesImageOnlyFirstProfile(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "image-p", Group: llm.GroupImage, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "k", Model: "gpt-image-2"}},
		},
	}}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	used := ""
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		used = cfg.Model
		return &capturingLLMProvider{reply: "ok"}, nil
	})

	_, err := runtime.runLLMProviderWithFailover(context.Background(), store, runtime.llmCfgFactory, func(client LLMProvider) (string, error) {
		resp, genErr := client.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "在吗"}}})
		if genErr != nil {
			return "", genErr
		}
		return resp.Text, nil
	})
	if err == nil {
		t.Fatalf("只有生图配置时对话调用应当报错，却用上了 %q", used)
	}
	if used != "" {
		t.Fatalf("对话调用用了生图模型 %q", used)
	}
}

// 组内候选严格按列表顺序，不受任何隐藏状态影响。
func TestChatGroupCandidatesFollowListOrder(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "first", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "k", Model: "model-first"}},
			{ID: "second", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "k", Model: "model-second"}},
		},
	}}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	var attempts []string
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		attempts = append(attempts, cfg.Model)
		return &capturingLLMProvider{reply: "ok"}, nil
	})

	if _, err := runtime.runLLMProviderWithFailover(context.Background(), store, runtime.llmCfgFactory, func(client LLMProvider) (string, error) {
		resp, genErr := client.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "在吗"}}})
		if genErr != nil {
			return "", genErr
		}
		return resp.Text, nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(attempts) == 0 || attempts[0] != "model-first" {
		t.Fatalf("首选应当是列表第一条，实际 %#v", attempts)
	}
	// 降级不再把「谁成功了」写回配置集：配置集是用户编排的，不该被运行时改。
	if store.saves != 0 {
		t.Fatalf("降级不该写回配置集，却保存了 %d 次", store.saves)
	}
}
