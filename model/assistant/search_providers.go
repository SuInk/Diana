// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

const (
	searchProvidersSetting     = "search_providers"
	searchProviderKeysSetting  = "search_provider_keys"
	searchProviderOrderSetting = "search_provider_order"
)

// SearchProvider is shared across robots. Read APIs return only the presence
// of a key; APIKey and ClearAPIKey are accepted exclusively on writes.
type SearchProvider struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	URL              string `json:"url"`
	Tool             string `json:"tool,omitempty"`
	QueryParam       string `json:"query_param,omitempty"`
	ResultsParam     string `json:"results_param,omitempty"`
	Disabled         bool   `json:"disabled,omitempty"`
	APIKey           string `json:"api_key,omitempty"`
	APIKeyConfigured bool   `json:"api_key_configured,omitempty"`
	ClearAPIKey      bool   `json:"clear_api_key,omitempty"`
}

// WebSearchAssignment stores this robot's ordered primary/fallback route.
// A nil assignment keeps the existing plugin configuration effective.
type WebSearchAssignment struct {
	ProviderIDs            []string `json:"provider_ids"`
	Disabled               bool     `json:"disabled,omitempty"`
	MaxResults             int      `json:"max_results,omitempty"`
	ProviderTimeoutSeconds int      `json:"provider_timeout_seconds,omitempty"`
	TotalTimeoutSeconds    int      `json:"total_timeout_seconds,omitempty"`
	SourceRecall           *bool    `json:"source_recall,omitempty"`
	ReplyLinkPolicy        string   `json:"reply_link_policy,omitempty"`
}

type SearchConfiguration struct {
	Providers         []SearchProvider    `json:"providers"`
	DefaultAssignment WebSearchAssignment `json:"default_assignment"`
}

var searchProviderIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func firstWebSearchAssignment(values ...*WebSearchAssignment) *WebSearchAssignment {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func normalizeWebSearchAssignment(value *WebSearchAssignment) *WebSearchAssignment {
	if value == nil {
		return nil
	}
	out := *value
	out.ProviderIDs = append([]string{}, value.ProviderIDs...)
	for i := range out.ProviderIDs {
		out.ProviderIDs[i] = strings.TrimSpace(out.ProviderIDs[i])
	}
	out.SourceRecall = copyBoolPointer(value.SourceRecall)
	return &out
}

func (value *WebSearchAssignment) Validate() error {
	if value == nil {
		return nil
	}
	if !value.Disabled && len(value.ProviderIDs) == 0 {
		return fmt.Errorf("联网搜索需要选择一个首选提供商")
	}
	if len(value.ProviderIDs) > 16 {
		return fmt.Errorf("搜索来源最多配置 16 个")
	}
	seen := map[string]bool{}
	for _, id := range value.ProviderIDs {
		if !searchProviderIDPattern.MatchString(id) || seen[id] {
			return fmt.Errorf("搜索提供商 ID 无效或重复")
		}
		seen[id] = true
	}
	for _, limit := range []struct {
		value, min, max int
		label           string
	}{
		{value.MaxResults, 1, 10, "搜索结果上限"}, {value.ProviderTimeoutSeconds, 2, 30, "单来源超时"}, {value.TotalTimeoutSeconds, 5, 90, "总搜索超时"},
	} {
		if limit.value != 0 && (limit.value < limit.min || limit.value > limit.max) {
			return fmt.Errorf("%s应在 %d–%d 之间", limit.label, limit.min, limit.max)
		}
	}
	if value.ReplyLinkPolicy != "" && !slices.Contains([]string{replyLinkPolicyOnRequest, replyLinkPolicyAlways, replyLinkPolicyNever}, value.ReplyLinkPolicy) {
		return fmt.Errorf("回复链接策略无效")
	}
	return nil
}

func (value *WebSearchAssignment) settings() map[string]any {
	idsToWrite := value.ProviderIDs
	if value.Disabled {
		idsToWrite = []string{}
	}
	ids, _ := json.Marshal(idsToWrite)
	settings := map[string]any{searchProviderOrderSetting: string(ids)}
	if value.MaxResults != 0 {
		settings[webSearchSettingMaxResults] = value.MaxResults
	}
	if value.ProviderTimeoutSeconds != 0 {
		settings[webSearchSettingProviderTimeout] = value.ProviderTimeoutSeconds
	}
	if value.TotalTimeoutSeconds != 0 {
		settings[webSearchSettingTotalTimeout] = value.TotalTimeoutSeconds
	}
	if value.SourceRecall != nil {
		settings[webSearchSettingSourceRecall] = *value.SourceRecall
	}
	if value.ReplyLinkPolicy != "" {
		settings[webSearchSettingLinkPolicy] = value.ReplyLinkPolicy
	}
	return settings
}

func searchProviderCatalog(settings SettingValues) ([]SearchProvider, map[string]string, error) {
	providers := []SearchProvider{
		{ID: "exa", Name: "Exa", Type: "exa_mcp", URL: settings.String(webSearchSettingExaURL, defaultExaSearchURL), Tool: "web_search_exa", Disabled: !settings.Bool(webSearchSettingExaEnabled, true)},
		{ID: "tavily", Name: "Tavily", Type: "tavily", URL: settings.String(webSearchSettingTavilyURL, defaultTavilySearchURL), Disabled: !settings.Bool(webSearchSettingTavilyEnabled, true)},
		{ID: "browser", Name: "浏览器搜索", Type: "browser", URL: "https://www.google.com/search", QueryParam: "q"},
	}
	if strings.TrimSpace(settings.String(searchProvidersSetting, "")) == "" && settings.String(webSearchSettingMode, webSearchModeEngine) == webSearchModeEngine {
		for _, engine := range webSearchEngineOrder(settings.String(webSearchSettingEngines, "")) {
			provider := SearchProvider{ID: "engine-" + engine, Name: engine, Type: "browser", Tool: engine}
			config := agent.WebSearchProviderConfig{Name: provider.ID, Type: agent.WebSearchProviderSearchEngine, Tool: engine}
			if agent.CustomSearchEngineURL(engine) {
				sum := sha256.Sum256([]byte(engine))
				provider.ID = "engine-" + hex.EncodeToString(sum[:8])
				provider.Name, provider.Tool, config.Tool, config.URL = "自定义搜索引擎", "", "", engine
			}
			normalized, err := agent.NormalizeWebSearchConfig(agent.WebSearchConfig{Providers: []agent.WebSearchProviderConfig{config}})
			if err != nil {
				return nil, nil, err
			}
			provider.URL = normalized.Providers[0].URL
			provider.QueryParam = "q"
			if engine == "baidu" {
				provider.QueryParam = "wd"
			}
			providers = append(providers, provider)
		}
	}
	if raw := strings.TrimSpace(settings.String(searchProvidersSetting, "")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &providers); err != nil {
			return nil, nil, fmt.Errorf("搜索提供商列表无效")
		}
	}
	keys := map[string]string{}
	if key := settings.String(webSearchSettingExaAPIKey, ""); key != "" {
		keys["exa"] = key
	}
	if key := settings.String(webSearchSettingTavilyAPIKey, ""); key != "" {
		keys["tavily"] = key
	}
	if raw := settings.String(searchProviderKeysSetting, ""); raw != "" {
		keys = map[string]string{}
		if err := json.Unmarshal([]byte(raw), &keys); err != nil {
			return nil, nil, fmt.Errorf("搜索凭据存档无效")
		}
	}
	return providers, keys, nil
}

func (provider SearchProvider) agentConfig(timeout, results int) agent.WebSearchProviderConfig {
	return agent.WebSearchProviderConfig{Name: provider.ID, Type: provider.Type, URL: provider.URL, Tool: provider.Tool, QueryParam: provider.QueryParam, ResultsParam: provider.ResultsParam, Disabled: provider.Disabled, TimeoutMS: timeout * 1000, MaxResults: results, NoEnvAPIKey: provider.ID != "tavily"}
}

func defaultSearchProviderIDs(settings SettingValues, providers []SearchProvider) []string {
	ids := []string{}
	if raw := strings.TrimSpace(settings.String(searchProviderOrderSetting, "")); raw != "" {
		if json.Unmarshal([]byte(raw), &ids) == nil {
			return ids
		}
	}
	engineMode := settings.String(webSearchSettingMode, webSearchModeEngine) == webSearchModeEngine
	for _, provider := range providers {
		if !provider.Disabled && ((engineMode && strings.HasPrefix(provider.ID, "engine-")) || (!engineMode && (provider.ID == "exa" || provider.ID == "tavily"))) {
			ids = append(ids, provider.ID)
		}
	}
	return ids
}

func orderedSearchProviders(settings SettingValues) ([]agent.WebSearchProviderConfig, map[string]string, error) {
	catalog, keys, err := searchProviderCatalog(settings)
	if err != nil {
		return nil, nil, err
	}
	ids := defaultSearchProviderIDs(settings, catalog)
	if raw := strings.TrimSpace(settings.String(searchProviderOrderSetting, "")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return nil, nil, fmt.Errorf("搜索路由配置无效")
		}
	}
	providers := []agent.WebSearchProviderConfig{}
	for _, id := range ids {
		for _, provider := range catalog {
			if provider.ID == id && !provider.Disabled {
				providers = append(providers, provider.agentConfig(settings.Int(webSearchSettingProviderTimeout, 12), settings.Int(webSearchSettingMaxResults, 5)))
				break
			}
		}
	}
	return providers, keys, nil
}

func (m *PluginManager) SearchConfiguration(profileID string) (SearchConfiguration, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.states[webSearchPluginID]
	if !ok {
		return SearchConfiguration{}, ErrPluginNotFound
	}
	settings := effectivePluginSettingsForGroup(state.Manifest.Settings, state.Settings, nil)
	providers, keys, err := searchProviderCatalog(settings)
	if err != nil {
		return SearchConfiguration{}, err
	}
	assignment := WebSearchAssignment{ProviderIDs: []string{}, Disabled: !state.ForProfile(profileID).Enabled, MaxResults: settings.Int(webSearchSettingMaxResults, 5), ProviderTimeoutSeconds: settings.Int(webSearchSettingProviderTimeout, 12), TotalTimeoutSeconds: settings.Int(webSearchSettingTotalTimeout, 35), SourceRecall: boolPointer(settings.Bool(webSearchSettingSourceRecall, true)), ReplyLinkPolicy: settings.String(webSearchSettingLinkPolicy, replyLinkPolicyOnRequest)}
	for i := range providers {
		providers[i].APIKey = ""
		providers[i].ClearAPIKey = false
		providers[i].APIKeyConfigured = strings.TrimSpace(keys[providers[i].ID]) != "" || providers[i].ID == "tavily" && strings.TrimSpace(os.Getenv("TAVILY_API_KEY")) != ""

	}
	assignment.ProviderIDs = defaultSearchProviderIDs(settings, providers)
	if len(assignment.ProviderIDs) == 0 {
		assignment.Disabled = true
	}
	return SearchConfiguration{Providers: providers, DefaultAssignment: assignment}, nil
}

func (m *PluginManager) SaveSearchProvider(provider SearchProvider) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.states[webSearchPluginID]
	if !ok {
		return "", ErrPluginNotFound
	}
	settings := effectivePluginSettingsForGroup(state.Manifest.Settings, state.Settings, nil)
	providers, keys, err := searchProviderCatalog(settings)
	if err != nil {
		return "", err
	}
	if provider.ID == "" {
		var id [8]byte
		if _, err := rand.Read(id[:]); err != nil {
			return "", err
		}
		provider.ID = "search-" + hex.EncodeToString(id[:])
	}
	if !searchProviderIDPattern.MatchString(provider.ID) {
		return "", fmt.Errorf("搜索提供商 ID 无效")
	}
	provider.Name = strings.TrimSpace(provider.Name)
	if provider.Name == "" || len([]rune(provider.Name)) > 100 {
		return "", fmt.Errorf("请填写 1–100 字的搜索提供商名称")
	}
	normalized, err := agent.NormalizeWebSearchConfig(agent.WebSearchConfig{Providers: []agent.WebSearchProviderConfig{provider.agentConfig(12, 5)}})
	if err != nil {
		return "", err
	}
	next := normalized.Providers[0]
	provider.Type, provider.URL, provider.Tool, provider.QueryParam, provider.ResultsParam = next.Type, next.URL, next.Tool, next.QueryParam, next.ResultsParam
	if provider.ClearAPIKey || provider.Type == "browser" {
		delete(keys, provider.ID)
	} else if value := strings.TrimSpace(provider.APIKey); value != "" {
		keys[provider.ID] = value
	}
	provider.APIKey, provider.APIKeyConfigured, provider.ClearAPIKey = "", false, false
	index := slices.IndexFunc(providers, func(existing SearchProvider) bool { return existing.ID == provider.ID })
	if index >= 0 {
		providers[index] = provider
	} else {
		if len(providers) >= 100 {
			return "", fmt.Errorf("搜索提供商最多配置 100 个")
		}
		providers = append(providers, provider)
	}
	writeSearchProviderState(&state, providers, keys)
	m.states[webSearchPluginID] = state
	return provider.ID, nil
}

func (m *PluginManager) DeleteSearchProvider(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.states[webSearchPluginID]
	if !ok {
		return ErrPluginNotFound
	}
	providers, keys, err := searchProviderCatalog(effectivePluginSettingsForGroup(state.Manifest.Settings, state.Settings, nil))
	if err != nil {
		return err
	}
	index := slices.IndexFunc(providers, func(provider SearchProvider) bool { return provider.ID == id })
	if index < 0 {
		return fmt.Errorf("搜索提供商不存在")
	}
	providers = slices.Delete(providers, index, index+1)
	delete(keys, id)
	writeSearchProviderState(&state, providers, keys)
	m.states[webSearchPluginID] = state
	return nil
}

func writeSearchProviderState(state *PluginState, providers []SearchProvider, keys map[string]string) {
	state.Settings = clonePluginValues(state.Settings)
	if state.Settings == nil {
		state.Settings = map[string]any{}
	}
	raw, _ := json.Marshal(providers)
	state.Settings[searchProvidersSetting] = string(raw)
	raw, _ = json.Marshal(keys)
	state.Settings[searchProviderKeysSetting] = string(raw)
	// The catalog has now consumed legacy credentials; clearing a migrated key
	// must never resurrect it from the old plugin fields on the next save.
	delete(state.Settings, webSearchSettingExaAPIKey)
	delete(state.Settings, webSearchSettingTavilyAPIKey)
}

func searchDisabledByOverride(overrides map[string]map[string]any) bool {
	settings := overrides[webSearchPluginID]
	raw, ok := settings[searchProviderOrderSetting].(string)
	if !ok {
		return false
	}
	var ids []string
	return json.Unmarshal([]byte(raw), &ids) == nil && len(ids) == 0
}
