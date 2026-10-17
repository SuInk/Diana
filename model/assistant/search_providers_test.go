// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
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

func TestGlobalSearchRoutingIgnoresLegacyRobotAndFallsBack(t *testing.T) {
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
	if err := manager.SaveSearchAssignment(WebSearchAssignment{ProviderIDs: []string{a, b}, MaxResults: 3, SourceRecall: boolPointer(false)}, nil); err != nil {
		t.Fatal(err)
	}
	cfg.WebSearch = &WebSearchAssignment{ProviderIDs: []string{"missing"}, Disabled: true}
	runtime := NewRuntime(cfg, nil, manager, nil, nil, nil, nil)
	event := MessageEvent{ProfileID: cfg.ID, Kind: EventKindPrivate}
	overrides := runtime.pluginOverridesForEvent(event)
	if _, ok := overrides[webSearchPluginID]; ok {
		t.Fatal("legacy robot switch still applied")
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
	if _, _, enabled := runtime.pluginWithSettingsForEvent(webSearchPluginID, event); !enabled {
		t.Fatal("legacy disabled assignment affected global search")
	}
	// Another robot without an explicit assignment retains the existing plugin switch.
	other := DefaultBotConfig()
	other.ID = "other"
	other.Enabled = true
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg, other}})
	if _, settings, enabled := runtime.pluginWithSettingsForEvent(webSearchPluginID, MessageEvent{ProfileID: other.ID, Kind: EventKindPrivate}); !enabled || settings.Int(webSearchSettingMaxResults, 0) != 3 {
		t.Fatal("global route not shared")
	}
}

func TestHTTPSearchProviderRestartsWithMappingsAndRobotUsesSavedKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.Header.Get("X-Search-Key") != "saved-http-key" || body["q"] != "Diana" || body["limit"] != float64(2) || body["language"] != "zh" {
			t.Errorf("saved auth or request settings were lost: body=%v", body)
		}
		w.Write([]byte(`{"data":{"items":[{"href":"https://example.org/http-source","name":"Saved HTTP source","summary":"Saved mapping works"}]}}`))
	}))
	defer server.Close()
	manager := NewDefaultPluginManager()
	provider := SearchProvider{Name: "Custom HTTP", Type: "http", URL: server.URL, QueryParam: "q", ResultsParam: "limit", APIKey: "saved-http-key", HTTPConfig: &agent.HTTPSearchConfig{AuthType: "header", AuthHeader: "X-Search-Key", Params: map[string]any{"language": "zh"}, ResultsPath: "data.items", URLPath: "href", TitlePath: "name", SnippetPath: "summary"}}
	id, err := manager.SaveSearchProvider(provider)
	if err != nil {
		t.Fatal(err)
	}
	restored := NewDefaultPluginManager()
	restored.Restore(manager.Snapshot())
	config, err := restored.SearchConfiguration("")
	if err != nil {
		t.Fatal(err)
	}
	saved := config.Providers[len(config.Providers)-1]
	if saved.ID != id || saved.HTTPConfig == nil || saved.HTTPConfig.ResultsPath != "data.items" || !saved.APIKeyConfigured || saved.APIKey != "" {
		t.Fatalf("restored provider=%#v", saved)
	}
	cfg := DefaultBotConfig()
	cfg.ID = "http-search-bot"
	if err := restored.SaveSearchAssignment(WebSearchAssignment{ProviderIDs: []string{id}, MaxResults: 2, SourceRecall: boolPointer(false)}, nil); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(cfg, nil, restored, nil, nil, nil, nil)
	_, settings, enabled := runtime.pluginWithSettingsForEvent(webSearchPluginID, MessageEvent{ProfileID: cfg.ID, Kind: EventKindPrivate})
	if !enabled {
		t.Fatal("explicit HTTP assignment did not enable search")
	}
	tools, err := NewWebSearchPlugin(nil).AgentTools(settings)
	if err != nil {
		t.Fatal(err)
	}
	output, err := tools[0].Run(context.Background(), map[string]any{"query": "Diana"})
	if err != nil || !strings.Contains(output, "Saved mapping works") || !strings.Contains(output, "example.org/http-source") || strings.Contains(output, "saved-http-key") {
		t.Fatalf("output=%s err=%v", output, err)
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
	for _, assignment := range []*WebSearchAssignment{{ProviderIDs: []string{}}, {ProviderIDs: []string{"exa", "exa"}}, {ProviderIDs: []string{"exa"}, MaxResults: 11}} {
		if assignment.Validate() == nil {
			t.Fatal("invalid search assignment accepted")
		}
	}
}

func TestWebSearchIgnoresGroupOverrides(t *testing.T) {
	manager := NewDefaultPluginManager()
	overrides := PluginSettingOverrides{webSearchPluginID: {searchProvidersSetting: `[{"id":"injected","api_key":"secret"}]`, searchProviderOrderSetting: `["injected"]`, webSearchSettingMaxResults: 3}}
	validated, err := manager.ValidateGroupSettingOverrides(overrides)
	if err != nil || validated[webSearchPluginID] != nil {
		t.Fatalf("group search override kept: %v %v", validated, err)
	}
	if manager.SanitizeGroupSettingOverrides(overrides)[webSearchPluginID] != nil {
		t.Fatal("group search override exposed")
	}
	cfg := DefaultBotConfig()
	cfg.ID = "bot"
	cfg.WebSearch = &WebSearchAssignment{ProviderIDs: []string{"exa"}}
	runtime := NewRuntime(cfg, nil, manager, nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"10001": {GroupID: "10001", PluginOverrides: map[string]bool{webSearchPluginID: false}, PluginSettingOverrides: overrides}}})
	event := MessageEvent{ProfileID: cfg.ID, Kind: EventKindGroup, GroupID: "10001"}
	if !manager.EnabledWithOverrides(webSearchPluginID, runtime.pluginOverridesForEvent(event)) {
		t.Fatal("legacy group switch still disables search")
	}
	if _, ok := runtime.pluginSettingOverridesForEvent(event)[webSearchPluginID][webSearchSettingMaxResults]; ok {
		t.Fatal("legacy group settings still applied")
	}
}

func TestSearchProviderCatalogPreservesBrowserEngineDefaults(t *testing.T) {
	manager := NewDefaultPluginManager()
	config, err := manager.SearchConfiguration("")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exa", "engine-google", "engine-bing", "engine-duckduckgo", "engine-baidu"}
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
	if err != nil || len(providers) != 5 || providers[0].Name != "exa" || providers[4].QueryParam != "wd" {
		t.Fatalf("providers=%v err=%v", providers, err)
	}
	runtime := NewRuntime(BotConfig{ID: "bot", WebSearch: &WebSearchAssignment{Disabled: true, ProviderIDs: []string{"engine-google"}}}, nil, manager, nil, nil, nil, nil)
	event := MessageEvent{ProfileID: "bot", Kind: EventKindPrivate}
	overrides := runtime.pluginSettingOverridesForEvent(event)
	if _, ok := overrides[webSearchPluginID]; ok {
		t.Fatal("legacy search assignment applied")
	}
}

func TestSearchProviderExportImportRoundTrip(t *testing.T) {
	source := NewDefaultPluginManager()
	id, err := source.SaveSearchProvider(SearchProvider{Name: "Private MCP", Type: "search_mcp", URL: "https://private.example/mcp", Tool: "lookup", APIKey: "custom-secret"})
	if err != nil {
		t.Fatal(err)
	}
	exported, err := source.ExportSearchProviders()
	if err != nil || !slices.ContainsFunc(exported, func(p SearchProvider) bool { return p.ID == id && p.APIKey == "custom-secret" }) {
		t.Fatalf("export=%#v err=%v", exported, err)
	}

	target := NewDefaultPluginManager()
	keep, _ := target.SaveSearchProvider(SearchProvider{Name: "Local only", Type: "tavily", URL: "https://api.tavily.com/search", APIKey: "keep-secret"})
	bad := append(slices.Clone(exported), SearchProvider{ID: "broken", Name: "", Type: "tavily"})
	if err := target.ImportSearchProviders(bad); err == nil {
		t.Fatal("invalid import accepted")
	}
	if config, _ := target.SearchConfiguration(""); slices.ContainsFunc(config.Providers, func(p SearchProvider) bool { return p.ID == id }) {
		t.Fatal("failed import wrote partial providers")
	}
	if err := target.ImportSearchProviders(exported); err != nil {
		t.Fatal(err)
	}
	after, _ := target.ExportSearchProviders()
	if !slices.ContainsFunc(after, func(p SearchProvider) bool { return p.ID == id && p.APIKey == "custom-secret" }) || !slices.ContainsFunc(after, func(p SearchProvider) bool { return p.ID == keep && p.APIKey == "keep-secret" }) {
		t.Fatalf("after import=%#v", after)
	}
}

func TestGlobalSearchAssignmentAtomicPersistenceAndReferences(t *testing.T) {
	m := NewDefaultPluginManager()
	before := m.Snapshot()
	failure := errors.New("disk unavailable")
	err := m.SaveSearchAssignment(WebSearchAssignment{Disabled: true, ProviderIDs: []string{"browser"}, MaxResults: 8}, func(snapshot map[string]PersistedPluginState) error {
		state := snapshot[webSearchPluginID]
		if state.Enabled == nil || *state.Enabled || state.Settings[webSearchSettingMaxResults] != 8 {
			t.Fatal("non-atomic persisted assignment")
		}
		return failure
	})
	if !errors.Is(err, failure) || !reflect.DeepEqual(before, m.Snapshot()) {
		t.Fatal("failed save mutated runtime")
	}
	if err := m.SaveSearchAssignment(WebSearchAssignment{ProviderIDs: []string{"browser"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteSearchProvider("browser"); err == nil {
		t.Fatal("deleted referenced provider")
	}
	if _, err := m.SaveSearchProvider(SearchProvider{ID: "browser", Name: "Browser", Type: "browser", URL: "https://www.google.com/search", Disabled: true}); err == nil {
		t.Fatal("disabled active referenced provider")
	}
	if err := m.ImportSearchProviders([]SearchProvider{{ID: "browser", Name: "Browser", Type: "browser", URL: "https://www.google.com/search", Disabled: true}}); err == nil {
		t.Fatal("import disabled active provider")
	}
	if err := m.SaveSearchAssignment(WebSearchAssignment{Disabled: true, ProviderIDs: []string{"browser"}}, nil); err != nil {
		t.Fatal(err)
	}
	restored := NewDefaultPluginManager()
	restored.Restore(m.Snapshot())
	restored.MigrateProfileConfigurations([]BotConfig{{ID: "bot"}})
	config, _ := restored.SearchConfiguration("bot")
	if !config.DefaultAssignment.Disabled || !reflect.DeepEqual(config.DefaultAssignment.ProviderIDs, []string{"browser"}) {
		t.Fatalf("lost disabled route: %+v", config)
	}
	if err := restored.SaveSearchAssignment(WebSearchAssignment{Disabled: true, ProviderIDs: []string{"stale-provider"}}, nil); err != nil {
		t.Fatalf("cannot disable stale route: %v", err)
	}
	if err := restored.SaveSearchAssignment(WebSearchAssignment{ProviderIDs: []string{"stale-provider"}}, nil); err == nil {
		t.Fatal("enabled stale route")
	}
}

func TestGlobalSearchMigrationPreservesSettingsAndExplicitEmptyRoute(t *testing.T) {
	for _, migrated := range []bool{false, true} {
		m := NewDefaultPluginManager()
		m.Restore(map[string]PersistedPluginState{webSearchPluginID: {Installed: true, Enabled: boolPointer(true), ProfileConfigMigrated: migrated, Settings: map[string]any{searchProviderOrderSetting: "[]", webSearchSettingMaxResults: 7}, ProfileEnabled: map[string]bool{"bot": false}, ProfileSettings: map[string]map[string]any{"bot": {searchProviderOrderSetting: `["browser"]`, webSearchSettingMaxResults: 2}}}})
		m.MigrateProfileConfigurations([]BotConfig{{ID: "bot"}})
		state := m.Snapshot()[webSearchPluginID]
		if state.Settings[searchProviderOrderSetting] != "[]" || SettingValues(state.Settings).Int(webSearchSettingMaxResults, 0) != 7 || len(state.ProfileSettings) != 1 {
			t.Fatalf("global or backup overwritten: %+v", state)
		}
		tools, err := m.AgentToolsWithOverrides(m.ProfileOverrides("bot"))
		if err != nil || hasAgentToolNamed(tools, agent.WebSearchToolName) {
			t.Fatal("empty route bypassed")
		}
		if err := m.SaveSearchAssignment(WebSearchAssignment{ProviderIDs: []string{}}, nil); err == nil {
			t.Fatal("enabled empty assignment accepted")
		}
		if err := m.SaveSearchAssignment(WebSearchAssignment{}, nil); err != nil {
			t.Fatal(err)
		}
		tools, err = m.AgentToolsWithOverrides(m.ProfileOverrides("bot"))
		if err != nil || !hasAgentToolNamed(tools, agent.WebSearchToolName) {
			t.Fatal("missing route lost legacy defaults")
		}
	}
}
