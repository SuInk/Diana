// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSearchProviderMigrationRedactionAndRestart(t *testing.T) {
	manager := NewDefaultPluginManager()
	if _, err := manager.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingMode: webSearchModeAPI, webSearchSettingExaAPIKey: "legacy-exa", webSearchSettingTavilyAPIKey: "legacy-tavily", webSearchSettingTavilyURL: "https://legacy.example/search"}); err != nil {
		t.Fatal(err)
	}
	before, err := manager.SearchConfiguration("")
	if err != nil || !before.Providers[0].APIKeyConfigured || before.Providers[1].URL != "https://legacy.example/search" {
		t.Fatalf("before=%#v err=%v", before, err)
	}
	custom := SearchProvider{Name: "Private MCP", Type: "search_mcp", URL: "https://private.example/mcp", Tool: "lookup", QueryParam: "text", APIKey: "custom-secret"}
	id, err := manager.SaveSearchProvider(custom)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := manager.SearchConfiguration("")
	raw, _ := json.Marshal(config)
	state, _ := manager.Get(webSearchPluginID)
	redacted, _ := json.Marshal(state.Redacted())
	for _, secret := range []string{"legacy-exa", "legacy-tavily", "custom-secret"} {
		if strings.Contains(string(raw), secret) || strings.Contains(string(redacted), secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
	exa := config.Providers[0]
	exa.ClearAPIKey = true
	if _, err := manager.SaveSearchProvider(exa); err != nil {
		t.Fatal(err)
	}
	// A legacy caller editing a normal option must not erase the new catalog.
	if _, err := manager.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingMaxResults: 7}); err != nil {
		t.Fatal(err)
	}
	restored := NewDefaultPluginManager()
	restored.Restore(manager.Snapshot())
	config, err = restored.SearchConfiguration("")
	if err != nil || len(config.Providers) != 4 || config.Providers[0].APIKeyConfigured || !config.Providers[1].APIKeyConfigured || !config.Providers[3].APIKeyConfigured || config.Providers[3].ID != id {
		t.Fatalf("after restart=%#v err=%v", config, err)
	}
	keys := restored.Snapshot()[webSearchPluginID].Settings[searchProviderKeysSetting].(string)
	if strings.Contains(keys, "legacy-exa") || !strings.Contains(keys, "legacy-tavily") || !strings.Contains(keys, "custom-secret") {
		t.Fatalf("migration lost or resurrected a key")
	}
	if _, err := manager.UpdateSettings(webSearchPluginID, map[string]any{searchProvidersSetting: `[{"api_key":"injected"}]`}); err == nil {
		t.Fatal("raw catalog write accepted")
	}
}

func TestSearchProviderValidationAndClearKey(t *testing.T) {
	manager := NewDefaultPluginManager()
	for _, provider := range []SearchProvider{{Name: "", Type: "tavily"}, {Name: "Bad URL", Type: "tavily", URL: "http://remote.example/search"}, {Name: "Unknown", Type: "json"}, {Name: "Missing tool", Type: "search_mcp", URL: "https://example.org/mcp"}} {
		if _, err := manager.SaveSearchProvider(provider); err == nil {
			t.Fatalf("invalid provider accepted: %#v", provider)
		}
	}
	id, err := manager.SaveSearchProvider(SearchProvider{Name: "Custom API", Type: "tavily", URL: "https://example.org/search", APIKey: "first-key"})
	if err != nil {
		t.Fatal(err)
	}
	config, _ := manager.SearchConfiguration("")
	provider := config.Providers[len(config.Providers)-1]
	provider.Name = "Renamed"
	if _, err := manager.SaveSearchProvider(provider); err != nil {
		t.Fatal(err)
	}
	config, _ = manager.SearchConfiguration("")
	provider = config.Providers[len(config.Providers)-1]
	if !provider.APIKeyConfigured {
		t.Fatal("blank draft removed existing key")
	}
	provider.ClearAPIKey = true
	if _, err := manager.SaveSearchProvider(provider); err != nil {
		t.Fatal(err)
	}
	config, _ = manager.SearchConfiguration("")
	if config.Providers[len(config.Providers)-1].APIKeyConfigured {
		t.Fatal("clear key ignored")
	}
	if err := manager.DeleteSearchProvider(id); err != nil {
		t.Fatal(err)
	}
}

func TestRobotSearchRoutingOverridesLegacyPluginAndFallsBack(t *testing.T) {
	var called []string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = append(called, "primary")
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = append(called, "fallback")
		if r.Header.Get("Authorization") != "Bearer fallback-key" {
			t.Error("missing saved provider key")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"title":"Source","url":"https://example.org/release","content":"Found"}]}`))
	}))
	defer fallback.Close()
	manager := NewDefaultPluginManager()
	manager.SetEnabledForProfile(webSearchPluginID, "search-bot", false)
	a, err := manager.SaveSearchProvider(SearchProvider{Name: "Primary", Type: "tavily", URL: primary.URL, APIKey: "primary-key"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := manager.SaveSearchProvider(SearchProvider{Name: "Fallback", Type: "tavily", URL: fallback.URL, APIKey: "fallback-key"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultBotConfig()
	cfg.ID = "search-bot"
	cfg.Enabled = true
	cfg.WebSearch = &WebSearchAssignment{ProviderIDs: []string{a, b}, MaxResults: 3, SourceRecall: boolPointer(false)}
	runtime := NewRuntime(cfg, nil, manager, nil, nil, nil, nil)
	event := MessageEvent{ProfileID: cfg.ID, Kind: EventKindPrivate}
	overrides := runtime.pluginOverridesForEvent(event)
	if !overrides[webSearchPluginID] {
		t.Fatal("explicit assignment failed to enable legacy-disabled plugin")
	}
	_, settings, enabled := runtime.pluginWithSettingsForEvent(webSearchPluginID, event)
	if !enabled || settings.Bool(webSearchSettingSourceRecall, true) || settings.Int(webSearchSettingMaxResults, 0) != 3 {
		t.Fatalf("runtime settings=%#v enabled=%v", settings, enabled)
	}
	tools, err := NewWebSearchPlugin(nil).AgentTools(settings)
	if err != nil {
		t.Fatal(err)
	}
	output, err := tools[0].Run(context.Background(), map[string]any{"query": "Diana release"})
	if err != nil || !reflect.DeepEqual(called, []string{"primary", "fallback"}) || !strings.Contains(output, b) {
		t.Fatalf("called=%v output=%s err=%v", called, output, err)
	}
	cfg.WebSearch.Disabled = true
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	if runtime.pluginOverridesForEvent(event)[webSearchPluginID] {
		t.Fatal("disabled assignment did not disable search")
	}
	// Another robot without an explicit assignment retains the existing plugin switch.
	other := DefaultBotConfig()
	other.ID = "other"
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg, other}})
	if runtime.pluginOverridesForEvent(MessageEvent{ProfileID: other.ID, Kind: EventKindPrivate})[webSearchPluginID] {
		t.Fatal("route leaked to another robot")
	}
}

func TestWebSearchAssignmentRoundTripAndOldClients(t *testing.T) {
	original := DefaultBotConfig()
	original.WebSearch = &WebSearchAssignment{ProviderIDs: []string{"custom", "browser"}, SourceRecall: boolPointer(false), ReplyLinkPolicy: "always"}
	payload := PayloadFromConfig(original)
	roundTrip := ConfigFromPayload(payload, DefaultBotConfig())
	if !reflect.DeepEqual(original.WebSearch, roundTrip.WebSearch) {
		t.Fatalf("round trip=%#v", roundTrip.WebSearch)
	}
	roundTrip.WebSearch.ProviderIDs[0] = "changed"
	*roundTrip.WebSearch.SourceRecall = true
	if original.WebSearch.ProviderIDs[0] != "custom" || *original.WebSearch.SourceRecall {
		t.Fatal("normalization did not clone assignment")
	}
	legacy := ConfigFromPayload(ConfigPayload{}, original)
	if !reflect.DeepEqual(legacy.WebSearch, original.WebSearch) {
		t.Fatal("omitted assignment reset old client config")
	}
	for _, assignment := range []*WebSearchAssignment{{ProviderIDs: nil}, {ProviderIDs: []string{"exa", "exa"}}, {ProviderIDs: []string{"exa"}, MaxResults: 11}} {
		if assignment.Validate() == nil {
			t.Fatal("invalid search assignment accepted")
		}
	}
}

func TestSearchCatalogCannotBeOverriddenByGroupSettings(t *testing.T) {
	manager := NewDefaultPluginManager()
	overrides := PluginSettingOverrides{webSearchPluginID: {searchProvidersSetting: `[{"id":"injected","api_key":"secret"}]`, searchProviderOrderSetting: `["injected"]`, webSearchSettingMaxResults: 3}}
	if _, err := manager.ValidateGroupSettingOverrides(overrides); err == nil {
		t.Fatal("group catalog override accepted")
	}
	sanitized := manager.SanitizeGroupSettingOverrides(overrides)
	if _, ok := sanitized[webSearchPluginID][searchProvidersSetting]; ok {
		t.Fatal("catalog exposed in group settings")
	}
	if _, ok := sanitized[webSearchPluginID][searchProviderOrderSetting]; ok {
		t.Fatal("route exposed in group settings")
	}
	if SettingValues(sanitized[webSearchPluginID]).Int(webSearchSettingMaxResults, 0) != 3 {
		t.Fatal("legitimate group option lost")
	}
}

func TestSearchProviderCatalogPreservesBrowserEngineDefaults(t *testing.T) {
	manager := NewDefaultPluginManager()
	config, err := manager.SearchConfiguration("")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"engine-google", "engine-bing", "engine-duckduckgo", "engine-baidu"}
	if !reflect.DeepEqual(config.DefaultAssignment.ProviderIDs, want) {
		t.Fatalf("default=%v", config.DefaultAssignment.ProviderIDs)
	}
	if _, err := manager.SaveSearchProvider(SearchProvider{Name: "Extra API", Type: "tavily", URL: "https://example.org/search"}); err != nil {
		t.Fatal(err)
	}
	config, err = manager.SearchConfiguration("")
	if err != nil || !reflect.DeepEqual(config.DefaultAssignment.ProviderIDs, want) {
		t.Fatalf("after save=%v err=%v", config.DefaultAssignment.ProviderIDs, err)
	}
	state, _ := manager.Get(webSearchPluginID)
	providers, _, err := orderedSearchProviders(effectivePluginSettingsForGroup(state.Manifest.Settings, state.Settings, nil))
	if err != nil || len(providers) != 4 || providers[3].QueryParam != "wd" {
		t.Fatalf("providers=%v err=%v", providers, err)
	}
	runtime := NewRuntime(BotConfig{ID: "bot", WebSearch: &WebSearchAssignment{Disabled: true, ProviderIDs: []string{"engine-google"}}}, nil, manager, nil, nil, nil, nil)
	event := MessageEvent{ProfileID: "bot", Kind: EventKindPrivate}
	overrides := runtime.pluginSettingOverridesForEvent(event)
	if !searchDisabledByOverride(overrides) {
		t.Fatal("disabled search could be re-enabled by browser fallback")
	}
}
