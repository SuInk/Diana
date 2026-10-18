// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
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
	ID               string                  `json:"id"`
	Name             string                  `json:"name"`
	Type             string                  `json:"type"`
	URL              string                  `json:"url"`
	Tool             string                  `json:"tool,omitempty"`
	QueryParam       string                  `json:"query_param,omitempty"`
	ResultsParam     string                  `json:"results_param,omitempty"`
	Disabled         bool                    `json:"disabled,omitempty"`
	APIKey           string                  `json:"api_key,omitempty"`
	APIKeyConfigured bool                    `json:"api_key_configured,omitempty"`
	ClearAPIKey      bool                    `json:"clear_api_key,omitempty"`
	HTTPConfig       *agent.HTTPSearchConfig `json:"http_config,omitempty"`
}

// WebSearchAssignment stores an ordered primary/fallback route, either the
// global one or a robot's own override.
// A nil ProviderIDs preserves the legacy search_mode defaults.
type WebSearchAssignment struct {
	ProviderIDs            []string `json:"provider_ids"`
	Disabled               bool     `json:"disabled,omitempty"`
	MaxResults             int      `json:"max_results,omitempty"`
	ProviderTimeoutSeconds int      `json:"provider_timeout_seconds,omitempty"`
	TotalTimeoutSeconds    int      `json:"total_timeout_seconds,omitempty"`
	ReplyLinkPolicy        string   `json:"reply_link_policy,omitempty"`
	// SourcePriority 为 api 或 browser 时先试该类来源，留空按列表顺序。
	SourcePriority string `json:"source_priority,omitempty"`
}

const (
	searchSourcePriorityAPI     = "api"
	searchSourcePriorityBrowser = "browser"
)

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
	out.ProviderIDs = slices.Clone(value.ProviderIDs)
	for i := range out.ProviderIDs {
		out.ProviderIDs[i] = strings.TrimSpace(out.ProviderIDs[i])
	}
	return &out
}

func (value *WebSearchAssignment) Validate() error {
	if value == nil {
		return nil
	}
	if !value.Disabled && value.ProviderIDs != nil && len(value.ProviderIDs) == 0 {
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
	if value.SourcePriority != "" && value.SourcePriority != searchSourcePriorityAPI && value.SourcePriority != searchSourcePriorityBrowser {
		return fmt.Errorf("搜索来源优先级无效")
	}
	if value.ReplyLinkPolicy != "" && !slices.Contains([]string{replyLinkPolicyOnRequest, replyLinkPolicyAlways, replyLinkPolicyNever}, value.ReplyLinkPolicy) {
		return fmt.Errorf("回复链接策略无效")
	}
	return nil
}

// 首次部署什么都没填时，默认先试三个浏览器引擎，最后用免密钥的 Exa 公共 MCP 兜底：
// 只靠浏览器的话，服务器没装 Chrome 就一次也搜不到。
var freshSearchProviderIDs = []string{"engine-google", "engine-bing", "engine-duckduckgo", "exa"}

// webSearchFreshKeys 只要存过其中任何一项，就说明不是全新部署，按已有配置走。
var webSearchFreshKeys = []string{searchProvidersSetting, searchProviderOrderSetting, webSearchSettingMode, webSearchSettingEngines, webSearchSettingExaEnabled, webSearchSettingExaURL, webSearchSettingExaAPIKey, webSearchSettingTavilyEnabled, webSearchSettingTavilyURL, webSearchSettingTavilyAPIKey, webSearchSettingSourcePriority}

const webSearchFreshInstallMarker = "__fresh_search_install__"

func freshSearchInstall(raw map[string]any) bool {
	for _, key := range webSearchFreshKeys {
		if _, ok := raw[key]; ok {
			return false
		}
	}
	return true
}

// webSearchStateSettings 读取联网搜索的有效设置，全新部署时补上首装默认路由。
func webSearchStateSettings(state PluginState) SettingValues {
	settings := effectivePluginSettingsForGroup(state.Manifest.Settings, state.Settings, nil)
	if freshSearchInstall(state.Settings) {
		ids, _ := json.Marshal(freshSearchProviderIDs)
		settings[searchProviderOrderSetting] = string(ids)
		settings[webSearchSettingSourcePriority] = searchSourcePriorityBrowser
		settings[webSearchFreshInstallMarker] = true
	}
	return settings
}

// materializeFreshSearchDefaults 在第一次保存任何搜索配置前把首装默认值写进存档，
// 否则存下提供商目录后就不再算全新部署，默认路由会悄悄退回只有浏览器。
func materializeFreshSearchDefaults(state *PluginState) {
	if !freshSearchInstall(state.Settings) {
		return
	}
	state.Settings = clonePluginValues(state.Settings)
	if state.Settings == nil {
		state.Settings = map[string]any{}
	}
	ids, _ := json.Marshal(freshSearchProviderIDs)
	state.Settings[searchProviderOrderSetting] = string(ids)
	state.Settings[webSearchSettingSourcePriority] = searchSourcePriorityBrowser
}

// webSearchOverrideKeys 是机器人单独配置可以覆盖的全局搜索设置。
var webSearchOverrideKeys = []string{searchProviderOrderSetting, webSearchSettingMaxResults, webSearchSettingProviderTimeout, webSearchSettingTotalTimeout, webSearchSettingLinkPolicy, webSearchSettingSourcePriority}

func (value *WebSearchAssignment) settings() map[string]any {
	settings := map[string]any{}
	if value.ProviderIDs != nil {
		ids, _ := json.Marshal(value.ProviderIDs)
		settings[searchProviderOrderSetting] = string(ids)
	}
	if value.MaxResults != 0 {
		settings[webSearchSettingMaxResults] = value.MaxResults
	}
	if value.ProviderTimeoutSeconds != 0 {
		settings[webSearchSettingProviderTimeout] = value.ProviderTimeoutSeconds
	}
	if value.TotalTimeoutSeconds != 0 {
		settings[webSearchSettingTotalTimeout] = value.TotalTimeoutSeconds
	}
	if value.ReplyLinkPolicy != "" {
		settings[webSearchSettingLinkPolicy] = value.ReplyLinkPolicy
	}
	// 总是写入，机器人选「按列表顺序」时也要盖住旧值。
	settings[webSearchSettingSourcePriority] = value.SourcePriority
	return settings
}

func searchProviderCatalog(settings SettingValues) ([]SearchProvider, map[string]string, error) {
	providers := []SearchProvider{
		{ID: "exa", Name: "Exa", Type: "exa_mcp", URL: settings.String(webSearchSettingExaURL, defaultExaSearchURL), Tool: "web_search_exa", Disabled: !settings.Bool(webSearchSettingExaEnabled, true)},
		{ID: "tavily", Name: "Tavily", Type: "tavily", URL: settings.String(webSearchSettingTavilyURL, defaultTavilySearchURL), Disabled: !settings.Bool(webSearchSettingTavilyEnabled, true)},
	}
	// 通用「浏览器搜索」和下面的 Google 引擎是同一个地址，只为兼容旧存档里的引用保留；
	// 全新部署没有旧引用，不再列出，免得看起来重复。
	if settings[webSearchFreshInstallMarker] != true {
		providers = append(providers, SearchProvider{ID: "browser", Name: "浏览器搜索", Type: "browser", URL: "https://www.google.com/search", QueryParam: "q"})
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
	return agent.WebSearchProviderConfig{Name: provider.ID, Type: provider.Type, URL: provider.URL, Tool: provider.Tool, QueryParam: provider.QueryParam, ResultsParam: provider.ResultsParam, Disabled: provider.Disabled, TimeoutMS: timeout * 1000, MaxResults: results, NoEnvAPIKey: provider.ID != "tavily", HTTPConfig: provider.HTTPConfig}
}

func defaultSearchProviderIDs(settings SettingValues, providers []SearchProvider) []string {
	ids := []string{}
	if raw := strings.TrimSpace(settings.String(searchProviderOrderSetting, "")); raw != "" {
		if json.Unmarshal([]byte(raw), &ids) == nil {
			return ids
		}
	}
	engineMode := settings.String(webSearchSettingMode, webSearchModeEngine) == webSearchModeEngine
	// 搜索引擎方式也是 Exa 打头，和 SearchConfiguration 里实际的默认顺序一致。
	if engineMode {
		if index := slices.IndexFunc(providers, func(provider SearchProvider) bool { return provider.ID == "exa" }); index >= 0 && !providers[index].Disabled {
			ids = append(ids, "exa")
		}
	}
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
	if priority := settings.String(webSearchSettingSourcePriority, ""); priority != "" {
		first := priority == searchSourcePriorityBrowser
		slices.SortStableFunc(providers, func(a, b agent.WebSearchProviderConfig) int {
			rank := func(p agent.WebSearchProviderConfig) int {
				if (p.Type == "browser") == first {
					return 0
				}
				return 1
			}
			return rank(a) - rank(b)
		})
	}
	return providers, keys, nil
}

func (m *PluginManager) SearchConfiguration(_ string) (SearchConfiguration, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.states[webSearchPluginID]
	if !ok {
		return SearchConfiguration{}, ErrPluginNotFound
	}
	settings := webSearchStateSettings(state)
	providers, keys, err := searchProviderCatalog(settings)
	if err != nil {
		return SearchConfiguration{}, err
	}
	assignment := WebSearchAssignment{ProviderIDs: []string{}, Disabled: !state.Enabled, MaxResults: settings.Int(webSearchSettingMaxResults, 5), ProviderTimeoutSeconds: settings.Int(webSearchSettingProviderTimeout, 12), TotalTimeoutSeconds: settings.Int(webSearchSettingTotalTimeout, 35), ReplyLinkPolicy: settings.String(webSearchSettingLinkPolicy, replyLinkPolicyOnRequest), SourcePriority: settings.String(webSearchSettingSourcePriority, "")}
	for i := range providers {
		providers[i].APIKey = ""
		providers[i].ClearAPIKey = false
		providers[i].APIKeyConfigured = strings.TrimSpace(keys[providers[i].ID]) != "" || providers[i].ID == "tavily" && strings.TrimSpace(os.Getenv("TAVILY_API_KEY")) != ""

	}
	assignment.ProviderIDs = defaultSearchProviderIDs(settings, providers)
	return SearchConfiguration{Providers: providers, DefaultAssignment: assignment}, nil
}

func (m *PluginManager) SaveSearchProvider(provider SearchProvider) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.states[webSearchPluginID]
	if !ok {
		return "", ErrPluginNotFound
	}
	settings := webSearchStateSettings(state)
	providers, keys, err := searchProviderCatalog(settings)
	if err != nil {
		return "", err
	}
	// 停用正在被机器人使用的来源是允许的：运行时跳过停用来源，按后备继续。
	providers, id, err := upsertSearchProvider(providers, keys, provider)
	if err != nil {
		return "", err
	}
	materializeFreshSearchDefaults(&state)
	writeSearchProviderState(&state, providers, keys)
	m.states[webSearchPluginID] = state
	return id, nil
}

// ExportSearchProviders returns the catalog with stored API keys, for the
// WebUI config export file. Callers must treat the result as a secret.
func (m *PluginManager) ExportSearchProviders() ([]SearchProvider, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.states[webSearchPluginID]
	if !ok {
		return nil, ErrPluginNotFound
	}
	providers, keys, err := searchProviderCatalog(webSearchStateSettings(state))
	if err != nil {
		return nil, err
	}
	for i := range providers {
		providers[i].APIKey = keys[providers[i].ID]
		providers[i].APIKeyConfigured, providers[i].ClearAPIKey = false, false
	}
	return providers, nil
}

// ImportSearchProviders upserts exported providers by ID. Providers missing
// from the file are kept, because the global search route may still reference them.
// All entries are validated before anything is written.
func (m *PluginManager) ImportSearchProviders(imported []SearchProvider) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.states[webSearchPluginID]
	if !ok {
		return ErrPluginNotFound
	}
	providers, keys, err := searchProviderCatalog(webSearchStateSettings(state))
	if err != nil {
		return err
	}
	for _, provider := range imported {
		if strings.TrimSpace(provider.ID) == "" {
			return fmt.Errorf("导入的搜索提供商缺少 ID")
		}
		provider.ClearAPIKey = false
		if providers, _, err = upsertSearchProvider(providers, keys, provider); err != nil {
			return fmt.Errorf("搜索提供商「%s」：%w", provider.Name, err)
		}
	}
	materializeFreshSearchDefaults(&state)
	writeSearchProviderState(&state, providers, keys)
	m.states[webSearchPluginID] = state
	return nil
}

// upsertSearchProvider validates provider and applies it to providers/keys in place.
func upsertSearchProvider(providers []SearchProvider, keys map[string]string, provider SearchProvider) ([]SearchProvider, string, error) {
	if provider.ID == "" {
		var id [8]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil, "", err
		}
		provider.ID = "search-" + hex.EncodeToString(id[:])
	}
	if !searchProviderIDPattern.MatchString(provider.ID) {
		return nil, "", fmt.Errorf("搜索提供商 ID 无效")
	}
	provider.Name = strings.TrimSpace(provider.Name)
	if provider.Name == "" || len([]rune(provider.Name)) > 100 {
		return nil, "", fmt.Errorf("请填写 1–100 字的搜索提供商名称")
	}
	normalized, err := agent.NormalizeWebSearchConfig(agent.WebSearchConfig{Providers: []agent.WebSearchProviderConfig{provider.agentConfig(12, 5)}})
	if err != nil {
		return nil, "", err
	}
	next := normalized.Providers[0]
	provider.Type, provider.URL, provider.Tool, provider.QueryParam, provider.ResultsParam = next.Type, next.URL, next.Tool, next.QueryParam, next.ResultsParam
	provider.HTTPConfig = next.HTTPConfig
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
			return nil, "", fmt.Errorf("搜索提供商最多配置 100 个")
		}
		providers = append(providers, provider)
	}
	return providers, provider.ID, nil
}

func (m *PluginManager) TestSearchProvider(ctx context.Context, provider SearchProvider, query string) agent.SearchProviderTestResult {
	m.mu.RLock()
	state, ok := m.states[webSearchPluginID]
	if !ok {
		m.mu.RUnlock()
		return agent.SearchProviderTestResult{Error: "搜索插件不存在"}
	}
	_, keys, err := searchProviderCatalog(webSearchStateSettings(state))
	m.mu.RUnlock()
	if err != nil {
		return agent.SearchProviderTestResult{Error: err.Error()}
	}
	key := strings.TrimSpace(provider.APIKey)
	if provider.ClearAPIKey {
		key = ""
	} else if key == "" {
		key = keys[provider.ID]
	}
	if provider.ID == "" {
		provider.ID = "draft"
	}
	// An explicit clear must not fall back to the canonical Tavily env key.
	config := provider.agentConfig(30, 3)
	if provider.ClearAPIKey {
		config.NoEnvAPIKey = true
	}
	return agent.ProbeWebSearchProvider(ctx, config, query, agent.WebSearchToolOptions{APIKeys: map[string]string{provider.ID: key}})
}

func (m *PluginManager) DeleteSearchProvider(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.states[webSearchPluginID]
	if !ok {
		return ErrPluginNotFound
	}
	providers, keys, err := searchProviderCatalog(webSearchStateSettings(state))
	if err != nil {
		return err
	}
	index := slices.IndexFunc(providers, func(provider SearchProvider) bool { return provider.ID == id })
	if index < 0 {
		return fmt.Errorf("搜索提供商不存在")
	}
	// 哪台机器人在用由调用方按机器人配置检查；这里只维护目录。
	providers = slices.Delete(providers, index, index+1)
	delete(keys, id)
	materializeFreshSearchDefaults(&state)
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

// SaveSearchAssignment persists one complete state before publishing it to runtime.
// persist runs under the manager lock and must not call back into the manager.
func (m *PluginManager) SaveSearchAssignment(value WebSearchAssignment, persist func(map[string]PersistedPluginState) error) error {
	value = *normalizeWebSearchAssignment(&value)
	if err := value.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.states[webSearchPluginID]
	if !ok {
		return ErrPluginNotFound
	}
	state.Settings = clonePluginValues(state.Settings)
	if state.Settings == nil {
		state.Settings = map[string]any{}
	}
	delete(state.Settings, searchProviderOrderSetting)
	for key, setting := range value.settings() {
		state.Settings[key] = setting
	}
	state.Enabled = !value.Disabled
	settings := webSearchStateSettings(state)
	providers, _, err := searchProviderCatalog(settings)
	if err != nil {
		return err
	}
	if state.Enabled {
		if err := validateGlobalSearchReferences(state, settings, providers, true); err != nil {
			return err
		}
	}
	if persist != nil {
		snapshot := m.snapshotLocked()
		saved := snapshot[webSearchPluginID]
		saved.Enabled, saved.Settings = boolPointer(state.Enabled), clonePluginValues(state.Settings)
		snapshot[webSearchPluginID] = saved
		if err := persist(snapshot); err != nil {
			return err
		}
	}
	m.states[webSearchPluginID] = state
	return nil
}

func validateGlobalSearchReferences(state PluginState, settings SettingValues, providers []SearchProvider, requireNonempty bool) error {
	ids := defaultSearchProviderIDs(settings, providers)
	if state.Enabled && requireNonempty && len(ids) == 0 {
		return fmt.Errorf("联网搜索需要选择一个首选提供商")
	}
	for _, id := range ids {
		index := slices.IndexFunc(providers, func(provider SearchProvider) bool { return provider.ID == id })
		if index < 0 || state.Enabled && providers[index].Disabled {
			return fmt.Errorf("全局搜索提供商 %s 不存在或已停用，请先更换搜索来源", id)
		}
	}
	return nil
}
