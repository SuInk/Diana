// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
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

func TestEnsureWebSearchAgentToolAlwaysKeepsSearchVisible(t *testing.T) {
	fallback := ensureWebSearchAgentTool(nil)
	if len(fallback) != 1 || fallback[0].Name() != agent.WebSearchToolName {
		t.Fatalf("fallback tools = %#v", fallback)
	}
	_, err := fallback[0].Run(context.Background(), map[string]any{"query": "Diana"})
	if err == nil || !strings.Contains(err.Error(), "工具已注册") || !strings.Contains(err.Error(), "Provider") {
		t.Fatalf("fallback error = %v", err)
	}

	configured := &scopeTestTool{name: agent.WebSearchToolName}
	tools := ensureWebSearchAgentTool([]agent.Tool{configured})
	if len(tools) != 1 || tools[0] != configured {
		t.Fatalf("configured search tool was replaced: %#v", tools)
	}
}
