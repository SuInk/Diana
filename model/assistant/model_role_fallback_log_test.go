// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// roleFallbackLogSet 把对话和意图分别放进两条配置，并且都不在名为 intent 的分组里：
// 线上就是这个形状——分组只有 default 和自定义名，意图靠角色绑定指过去。
func roleFallbackLogSet() llm.ProfileSet {
	return llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "chat-p", Name: "对话", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-c", Model: "gpt-chat"}},
			{ID: "intent-p", Name: "意图", Group: "custom", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "sk-i", Model: "gpt-intent"}},
		},
	}
}

func roleFallbackLogRegistry(t *testing.T) *llm.ProviderRegistry {
	t.Helper()
	registry, err := llm.RegistryFromDocument(llm.ProviderRegistryDocument{
		Version: 1,
		Providers: []llm.ProviderDefinition{
			{ID: "chat-p", Name: "对话", Protocol: llm.ProtocolOpenAIResponses, BaseURL: "https://example.invalid/v1", APIKey: "sk-c", Enabled: true},
			{ID: "intent-p", Name: "意图", Protocol: llm.ProtocolOpenAIResponses, BaseURL: "https://example.invalid/v1", APIKey: "sk-i", Enabled: true},
		},
		Models: []llm.ModelDefinition{
			{ID: "chat-p:gpt-chat", ProviderID: "chat-p", ModelID: "gpt-chat", Name: "gpt-chat"},
			{ID: "intent-p:gpt-intent", ProviderID: "intent-p", ModelID: "gpt-intent", Name: "gpt-intent"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func captureRuntimeLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Writer()
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(previous)
		log.SetFlags(flags)
	}()
	fn()
	return buf.String()
}

// 绑定解析成功就不该报「没绑」。以前 registrySelectionForGroup 无条件打这行回落日志，
// 于是每次按 intent 绑定正常选中都刷一句 has no bound provider，而它指名的那条
// provider 恰恰就是绑定里写的那条。意图路由每条群消息跑一次，线上是几秒一条。
func TestBoundRoleSelectionDoesNotLogUnboundFallback(t *testing.T) {
	roles := map[string]ModelRole{
		"chat":   {ProfileID: "chat-p", Model: "gpt-chat"},
		"intent": {ProfileID: "intent-p", Model: "gpt-intent"},
	}
	var selection llm.AgentModelConfig
	var ok bool
	var err error
	output := captureRuntimeLog(t, func() {
		selection, ok, err = registrySelectionForGroup(roleFallbackLogRegistry(t), roleFallbackLogSet(), roles, "proactive_reply_router", llm.GroupIntent, "")
	})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if selection.ProviderID != "intent-p" || selection.ModelID != "intent-p:gpt-intent" {
		t.Fatalf("selection = %+v，应当命中 intent 角色绑定", selection)
	}
	if strings.Contains(output, "has no bound provider") {
		t.Fatalf("绑定解析成功却打了回落日志：%s", output)
	}
}

// 真的没绑上时这行日志必须还在：它是「绑定本身有问题」的唯一信号，
// 修的是误报，不是把告警一起删掉。
func TestUnboundGroupStillLogsFallback(t *testing.T) {
	// 只绑了生图，意图既没有角色绑定，也没有同名分组，只能跨组回落到对话配置。
	roles := map[string]ModelRole{
		"image": {ProfileID: "intent-p", Model: "gpt-intent"},
	}
	var ok bool
	var err error
	output := captureRuntimeLog(t, func() {
		_, ok, err = registrySelectionForGroup(roleFallbackLogRegistry(t), roleFallbackLogSet(), roles, "", llm.GroupIntent, "")
	})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !strings.Contains(output, "has no bound provider") {
		t.Fatalf("意图没有任何绑定时应当留下回落日志，实际输出：%q", output)
	}
}
