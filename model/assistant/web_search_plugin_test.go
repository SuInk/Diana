// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func TestWebSearchPluginIsBuiltInAndHonorsOverrides(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := manager.Get(webSearchPluginID)
	if !ok {
		t.Fatal("web search plugin missing")
	}
	if !state.Installed || !state.Enabled || !state.Manifest.Official || !state.Manifest.BuiltIn {
		t.Fatalf("initial state = %#v", state)
	}
	tools, err := manager.AgentToolsWithOverrides(nil)
	if err != nil || !hasAgentToolNamed(tools, agent.WebSearchToolName) {
		t.Fatalf("built-in tools=%#v err=%v", tools, err)
	}

	if _, err := manager.Install(webSearchPluginID); !errors.Is(err, ErrBuiltInPluginAction) {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := manager.Uninstall(webSearchPluginID); !errors.Is(err, ErrBuiltInPluginAction) {
		t.Fatalf("Uninstall() error = %v", err)
	}
	state, _ = manager.Get(webSearchPluginID)
	if !state.Installed || !state.Enabled {
		t.Fatalf("built-in state changed after lifecycle request: %#v", state)
	}
	tools, err = manager.AgentToolsWithOverrides(map[string]bool{webSearchPluginID: false})
	if err != nil || hasAgentToolNamed(tools, agent.WebSearchToolName) {
		t.Fatalf("disabled override tools=%#v err=%v", tools, err)
	}
}

func hasAgentToolNamed(tools []agent.Tool, name string) bool {
	for _, tool := range tools {
		if tool != nil && tool.Name() == name {
			return true
		}
	}
	return false
}

func TestWebSearchPluginSecretsAreRedacted(t *testing.T) {
	manager := NewDefaultPluginManager()
	if _, err := manager.UpdateSettings(webSearchPluginID, map[string]any{
		webSearchSettingExaAPIKey:    "exa-secret",
		webSearchSettingTavilyAPIKey: "tavily-secret",
	}); err != nil {
		t.Fatal(err)
	}
	state, _ := manager.Get(webSearchPluginID)
	redacted := state.Redacted()
	if redacted.Settings != nil {
		t.Fatalf("redacted settings = %#v", redacted.Settings)
	}
	if !redacted.SecretsConfigured[webSearchSettingExaAPIKey] || !redacted.SecretsConfigured[webSearchSettingTavilyAPIKey] {
		t.Fatalf("configured flags = %#v", redacted.SecretsConfigured)
	}
}

func TestWebSearchPluginCanDisableAllProviders(t *testing.T) {
	plugin := NewWebSearchPlugin(nil)
	tools, err := plugin.AgentTools(SettingValues{
		webSearchSettingExaEnabled:    false,
		webSearchSettingTavilyEnabled: false,
	})
	if err != nil || len(tools) != 0 {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
}

// 关掉的搜索插件不再塞一个同名占位工具：以前它每轮都带着一整段搜索规则，模型照
// 规则去搜，必然失败，还会把「搜索没配置」说给用户听。
func TestRuntimeWithoutSearchPluginRegistersNoSearchTool(t *testing.T) {
	provider := &privacyAwareTestProvider{}
	sawAgentTools := false
	provider.generate = func(call int, req llm.GenerateRequest) (string, error) {
		for _, tool := range req.Tools {
			sawAgentTools = true
			if tool.Name == agent.WebSearchToolName {
				return "", fmt.Errorf("web_search 仍然登记在工具表里")
			}
		}
		if call == 1 {
			return `{"action":"none","tools":[],"context_message_ids":[],"keep_older_summary":false}`, nil
		}
		return `{"action":"final","content":"好"}`, nil
	}
	runtime := NewRuntime(BotConfig{BotAccount: "10000", AgentEnabled: true, AgentMaxSteps: 3}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{Kind: EventKindPrivate, UserID: "user", MessageID: "no-search", RawMessage: "今天有什么新闻"}
	if _, err := runtime.replyAndRecord(context.Background(), event, "今天有什么新闻", "replied"); err != nil {
		t.Fatal(err)
	}
	if !sawAgentTools {
		t.Fatal("没有走到 Agent，测不出工具表")
	}
	if last := runtime.Status().LastError; last != "" {
		t.Fatalf("reply failed: %s", last)
	}
}
