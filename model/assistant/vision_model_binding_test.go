// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// diana.history_images 会把历史原图作为真实附件补进下一轮，于是这一轮的用途从
// chat 变成 vision。说话的还是同一台机器人，没单独绑视觉模型时就该继续用它绑定的
// 聊天模型——滑到全局激活配置上是静默换模型，日志里两轮看着都「正常」。
func visionBindingProfileSet() llm.ProfileSet {
	return llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "bot-chat", Name: "机器人绑定", Group: "default", Config: llm.ProviderConfig{
				Provider: llm.ProviderOpenAICompatible, APIKey: "sk-bot", Model: "bound-chat-model",
			}},
			{ID: "global-active", Name: "全局激活", Group: "default", Config: llm.ProviderConfig{
				Provider: llm.ProviderOpenAICompatible, APIKey: "sk-active", Model: "active-model",
			}},
		},
	}
}

func TestVisionTurnKeepsBoundChatProfile(t *testing.T) {
	set := visionBindingProfileSet()
	runtime := NewRuntime(BotConfig{
		ModelRoles: map[string]ModelRole{
			"chat": {ProfileID: "bot-chat", Model: "bound-chat-model"},
		},
	}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: set}, nil, nil, nil)

	for _, group := range []string{llm.GroupChat, llm.GroupVision} {
		profiles, err := runtime.roleBoundProfiles(set, group)
		if err != nil {
			t.Fatalf("group %s: %v", group, err)
		}
		if len(profiles) != 1 || profiles[0].ID != "bot-chat" {
			t.Fatalf("group %s resolved to %+v, want the bound chat profile", group, profiles)
		}
		if profiles[0].Config.Model != "bound-chat-model" {
			t.Fatalf("group %s model = %q", group, profiles[0].Config.Model)
		}
	}
}

// 绑了视觉模型的机器人当然还是走视觉绑定：这条修的是「没绑」的回落方向，
// 不是把视觉绑定也一并吞掉。
func TestVisionTurnUsesItsOwnBindingWhenPresent(t *testing.T) {
	set := visionBindingProfileSet()
	set.Profiles = append(set.Profiles, llm.Profile{
		ID: "bot-vision", Name: "视觉绑定", Group: "vision", Config: llm.ProviderConfig{
			Provider: llm.ProviderOpenAICompatible, APIKey: "sk-vision", Model: "bound-vision-model",
		},
	})
	runtime := NewRuntime(BotConfig{
		ModelRoles: map[string]ModelRole{
			"chat":   {ProfileID: "bot-chat", Model: "bound-chat-model"},
			"vision": {ProfileID: "bot-vision", Model: "bound-vision-model"},
		},
	}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: set}, nil, nil, nil)

	profiles, err := runtime.roleBoundProfiles(set, llm.GroupVision)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].ID != "bot-vision" || profiles[0].Config.Model != "bound-vision-model" {
		t.Fatalf("vision resolved to %+v", profiles)
	}
}

// 新的 provider 注册表走另一条选路代码。它以前不做 chat 回落，视觉轮次直接落到
// 全局激活配置——这正是「用完 diana.history_images 之后换了个模型答话」的由来。
func TestRegistrySelectionFallsBackToBoundChatRole(t *testing.T) {
	registry, err := llm.RegistryFromDocument(llm.ProviderRegistryDocument{
		Version: 1,
		Providers: []llm.ProviderDefinition{
			{ID: "bot-provider", Name: "机器人绑定", Protocol: llm.ProtocolOpenAIResponses, BaseURL: "https://example.invalid/v1", APIKey: "sk-bot", Enabled: true},
			{ID: "global-active", Name: "全局激活", Protocol: llm.ProtocolOpenAIResponses, BaseURL: "https://example.invalid/v1", APIKey: "sk-active", Enabled: true},
		},
		Models: []llm.ModelDefinition{
			{ID: "bot-provider:bound-chat-model", ProviderID: "bot-provider", ModelID: "bound-chat-model", Name: "bound-chat-model"},
			{ID: "global-active:active-model", ProviderID: "global-active", ModelID: "active-model", Name: "active-model"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	roles := normalizeModelRoles(map[string]ModelRole{
		"chat": {ProviderID: "bot-provider", ModelID: "bound-chat-model", Model: "bound-chat-model"},
	})
	set := visionBindingProfileSet()

	for _, group := range []string{llm.GroupChat, llm.GroupVision} {
		selection, ok, err := registrySelectionForGroup(registry, set, roles, group, "")
		if err != nil || !ok {
			t.Fatalf("group %s: ok=%v err=%v", group, ok, err)
		}
		if selection.ProviderID != "bot-provider" || selection.ModelID != "bot-provider:bound-chat-model" {
			t.Fatalf("group %s selected %+v, want the bound chat provider", group, selection)
		}
	}
}

// 没有任何绑定时：先按分组找，本组没有就跨组回落到对话分组，取列表第一条。
//
// 这里原来断言的是「回落到全局激活配置」。「激活中」去掉之后，同组里选谁只由列表
// 顺序决定——界面上写的「组内顺序即降级优先级」这才是真的。
func TestRegistrySelectionWithoutRolesFallsBackToFirstChatProfile(t *testing.T) {
	registry, err := llm.RegistryFromDocument(llm.ProviderRegistryDocument{
		Version: 1,
		Providers: []llm.ProviderDefinition{
			{ID: "bot-chat", Name: "机器人绑定", Protocol: llm.ProtocolOpenAIResponses, BaseURL: "https://example.invalid/v1", APIKey: "sk-bot", Enabled: true},
			{ID: "global-active", Name: "第二条", Protocol: llm.ProtocolOpenAIResponses, BaseURL: "https://example.invalid/v1", APIKey: "sk-active", Enabled: true},
		},
		Models: []llm.ModelDefinition{
			{ID: "bot-chat:bound-chat-model", ProviderID: "bot-chat", ModelID: "bound-chat-model", Name: "bound-chat-model"},
			{ID: "global-active:active-model", ProviderID: "global-active", ModelID: "active-model", Name: "active-model"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection, ok, err := registrySelectionForGroup(registry, visionBindingProfileSet(), nil, llm.GroupVision, "")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if selection.ProviderID != "bot-chat" {
		t.Fatalf("selection = %+v，应当取对话分组列表里的第一条", selection)
	}
}
