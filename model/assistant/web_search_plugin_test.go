package assistant

import (
	"errors"
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
