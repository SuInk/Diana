// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

type PluginManifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Official    bool   `json:"official"`
	BuiltIn     bool   `json:"built_in"`
	// CanAskAgent allows plugin-originated events or context to start a formal
	// Agent reply turn. It is opt-in so ordinary plugins cannot make the bot
	// speak merely by returning context.
	CanAskAgent bool `json:"can_ask_agent,omitempty"`
	// Internal 标记已经内化为产品能力的插件：始终启用，且不出现在插件页。
	// 这类能力是其它功能的前提，暴露成一个可关的开关只会制造「别的插件莫名
	// 其妙不工作」的排查成本。
	Internal    bool                `json:"internal,omitempty"`
	Permissions []string            `json:"permissions,omitempty"`
	Settings    []PluginSettingSpec `json:"settings,omitempty"`
}

type PluginState struct {
	Manifest  PluginManifest `json:"manifest"`
	Installed bool           `json:"installed"`
	Enabled   bool           `json:"enabled"`
	// Settings 只保存用户显式覆盖的值，默认值以 Manifest.Settings 声明为准。
	Settings map[string]any `json:"settings,omitempty"`
	// SecretsConfigured 只在脱敏后的响应里出现，标记哪些凭据已经配置过。
	// 明文永远不出现在读接口里。
	SecretsConfigured map[string]bool `json:"secrets_configured,omitempty"`
}

// Redacted 返回可以安全交给 WebUI 的副本：凭据类设置抹掉明文，
// 只保留「是否已配置」的标记。所有对外返回 PluginState 的接口都必须走这里。
func (s PluginState) Redacted() PluginState {
	secrets := secretSettingKeys(s.Manifest.Settings)
	if len(secrets) == 0 {
		return s
	}
	out := s
	out.Settings = make(map[string]any, len(s.Settings))
	out.SecretsConfigured = make(map[string]bool, len(secrets))
	for key := range secrets {
		out.SecretsConfigured[key] = false
	}
	for key, value := range s.Settings {
		if !secrets[key] {
			out.Settings[key] = value
			continue
		}
		text, _ := value.(string)
		out.SecretsConfigured[key] = strings.TrimSpace(text) != ""
	}
	if len(out.Settings) == 0 {
		out.Settings = nil
	}
	return out
}

// RedactStates 批量脱敏。
func RedactStates(states []PluginState) []PluginState {
	out := make([]PluginState, 0, len(states))
	for _, state := range states {
		out = append(out, state.Redacted())
	}
	return out
}

type PluginRequest struct {
	Event                   MessageEvent    `json:"event"`
	RecentEvents            []MessageEvent  `json:"recent_events,omitempty"`
	RecallEvents            []MessageEvent  `json:"recall_events,omitempty"`
	Text                    string          `json:"text"`
	OwnerID                 string          `json:"owner_id,omitempty"`
	SandboxedBrowserEnabled bool            `json:"sandboxed_browser_enabled,omitempty"`
	Channel                 Channel         `json:"-"`
	LLMStore                LLMProfileStore `json:"-"`
	LLMModelLister          LLMModelLister  `json:"-"`
	AppLogs                 applog.Writer   `json:"-"`
	// Settings 由 PluginManager 在调用前注入当前插件的生效设置，直接构造请求时可留空走默认值。
	Settings SettingValues `json:"-"`
}

type PluginResponse struct {
	Handled          bool              `json:"handled"`
	Context          string            `json:"context,omitempty"`
	Reply            string            `json:"reply,omitempty"`
	ImageURLs        []string          `json:"image_urls,omitempty"`
	ContextImageURLs []string          `json:"-"`
	VideoURLs        []string          `json:"video_urls,omitempty"`
	Forward          bool              `json:"forward,omitempty"`
	NestedForward    bool              `json:"-"`
	ForwardMessages  []OutgoingMessage `json:"-"`
	Tasks            []PluginTask      `json:"-"`
	// FollowUp 让插件在内容发出后，请机器人像真人那样再自然接一句。
	// 只是一个开关：刚发出的内容已经写进历史，模型从历史里就能看到自己发了什么。
	FollowUp            bool           `json:"-"`
	RecallDisclosure    bool           `json:"-"`
	RecallEvents        []MessageEvent `json:"-"`
	RecallReferenceTime int64          `json:"-"`
}

// PluginTask describes work that should outlive the incoming message request.
type PluginTask struct {
	Kind           string
	Name           string
	Key            string
	StartedMessage string
	Timeout        time.Duration
	Run            func(context.Context, PluginTaskServices) (PluginTaskResult, error)
}

type PluginTaskResult struct {
	Reply    string
	Messages []OutgoingMessage
}

type PluginTaskProgress struct {
	Phase     string
	Message   string
	Completed int
	Total     int
}

type PluginTaskServices struct {
	Generate      func(context.Context, llm.GenerateRequest) (string, error)
	GenerateReply func(context.Context, llm.GenerateRequest) (string, error)
	Report        func(PluginTaskProgress)
}

type Plugin interface {
	Manifest() PluginManifest
	Handle(ctx context.Context, req PluginRequest) (*PluginResponse, error)
}

type DirectTriggerPlugin interface {
	ShouldHandle(event MessageEvent, text string) bool
}

type EventObserverPlugin interface {
	Observe(ctx context.Context, event MessageEvent) MessageEvent
}

// AgentToolPlugin is implemented by plugins that add tools to the model's
// tool-calling loop. It is intentionally optional so ordinary context/reply
// plugins keep the smaller Plugin contract.
type AgentToolPlugin interface {
	AgentTools(settings SettingValues) ([]agent.Tool, error)
}

type AgentToolProviderPlugin interface {
	AgentTools() []agent.Tool
}

type LocalMediaSharerAwarePlugin interface {
	SetLocalMediaSharer(LocalMediaSharer)
}

// SecretSettingMerger lets a plugin update one entry inside a structured
// secret without requiring the WebUI to read the other secret values back.
type SecretSettingMerger interface {
	MergeSecretSetting(key, previous, submitted string) (string, error)
}

type PluginManager struct {
	mu      sync.RWMutex
	catalog map[string]Plugin
	states  map[string]PluginState
}

var (
	ErrPluginNotFound        = errors.New("qqbot: plugin not found")
	ErrBuiltInPluginAction   = errors.New("qqbot: built-in plugins do not support install or uninstall")
	ErrInternalPluginDisable = errors.New("qqbot: 该能力已内置，无法停用")
)

const (
	resolverPluginID         = "official.nonebot-plugin-resolver-go"
	messageHistoryPluginID   = "official.message-history"
	sandboxedBrowserPluginID = "official.sandboxed-browser-renderer"
	pluginSettingAskAgent    = "ask_agent"
)

// NewPluginManager 创建插件管理器并登记插件目录。
func NewPluginManager(plugins ...Plugin) *PluginManager {
	manager := &PluginManager{
		catalog: map[string]Plugin{},
		states:  map[string]PluginState{},
	}
	for _, plugin := range plugins {
		manifest := plugin.Manifest()
		manager.catalog[manifest.ID] = plugin
		// 内置插件默认安装并启用，普通插件后续可以通过安装接口改变状态。
		manager.states[manifest.ID] = PluginState{
			Manifest:  manifest,
			Installed: manifest.BuiltIn,
			Enabled:   manifest.BuiltIn,
		}
	}
	return manager
}

// NewDefaultPluginManager 创建包含官方内置插件的默认插件管理器。
func NewDefaultPluginManager() *PluginManager {
	capabilities := NewCapabilityKnowledgePlugin()
	manager := NewPluginManager(
		NewMessageHistoryPlugin(),
		NewResolverPlugin(nil),
		NewFileParserPlugin(nil),
		NewLLMConfigPlugin(),
		NewOneBotV11SkillPlugin(),
		NewSandboxedBrowserRenderPlugin(),
		NewVoiceTTSPlugin(nil),
		NewVoiceSTTPlugin(nil),
		NewWebSearchPlugin(nil),
		NewRepositoryWatchPlugin(nil),
		NewRepositoryPublishPlugin(nil),
		NewRSSWatchPlugin(nil),
		capabilities,
	)
	capabilities.setPluginStateProvider(manager.List)
	return manager
}

func (m *PluginManager) ShouldHandleWithOverrides(event MessageEvent, text string, overrides map[string]bool) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	plugins := make([]DirectTriggerPlugin, 0)
	for id, plugin := range m.catalog {
		state := m.states[id]
		enabled := state.Enabled
		if override, ok := overrides[id]; ok {
			enabled = override
		}
		trigger, ok := plugin.(DirectTriggerPlugin)
		if state.Installed && enabled && ok {
			plugins = append(plugins, trigger)
		}
	}
	m.mu.RUnlock()
	for _, plugin := range plugins {
		if plugin.ShouldHandle(event, text) {
			return true
		}
	}
	return false
}

// List 返回所有插件状态。
func (m *PluginManager) List() []PluginState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]PluginState, 0, len(m.states))
	for _, state := range m.states {
		out = append(out, state)
	}
	slices.SortFunc(out, func(a, b PluginState) int {
		if a.Manifest.BuiltIn != b.Manifest.BuiltIn {
			if a.Manifest.BuiltIn {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Manifest.ID, b.Manifest.ID)
	})
	return out
}

// ListVisible 返回面向用户的插件列表，跳过已内化为产品能力的插件。
// 它们始终启用、没有可调的开关，摆在插件页上只会让人以为可以关掉。
// 内部消费方（能力索引、Agent 能力清单）仍然用 List 看到全部。
func (m *PluginManager) ListVisible() []PluginState {
	states := m.List()
	out := make([]PluginState, 0, len(states))
	for _, state := range states {
		if state.Manifest.Internal {
			continue
		}
		out = append(out, state)
	}
	return out
}

// Get 按 ID 返回单个插件状态。
func (m *PluginManager) Get(id string) (PluginState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.states[id]
	return state, ok
}

// EnabledWithOverrides reports the effective plugin switch for one event.
func (m *PluginManager) EnabledWithOverrides(id string, overrides map[string]bool) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.states[id]
	if !ok || !state.Installed {
		return false
	}
	// 已内化的能力不接受任何层级的关闭，群级覆盖也一样。
	if state.Manifest.Internal {
		return true
	}
	if enabled, overridden := overrides[id]; overridden {
		return enabled
	}
	return state.Enabled
}

func (m *PluginManager) Enabled(id string) bool {
	return m.EnabledWithOverrides(id, nil)
}

// PluginWithSettings returns one installed plugin and an immutable snapshot of
// its effective settings. Event-bound tools use this without exposing secrets
// to the model or requiring the plugin tool interface to know the chat target.
func (m *PluginManager) PluginWithSettings(id string, overrides map[string]bool) (Plugin, SettingValues, bool) {
	return m.PluginWithSettingsForGroup(id, overrides, nil)
}

// PluginWithSettingsForGroup returns one plugin with settings resolved in the
// order defaults -> global overrides -> group overrides.
func (m *PluginManager) PluginWithSettingsForGroup(id string, enabledOverrides map[string]bool, settingOverrides PluginSettingOverrides) (Plugin, SettingValues, bool) {
	if m == nil {
		return nil, nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	plugin, ok := m.catalog[id]
	state := m.states[id]
	enabled := state.Enabled
	if override, present := enabledOverrides[id]; present {
		enabled = override
	}
	if !ok || !state.Installed || !enabled {
		return nil, nil, false
	}
	return plugin, effectivePluginSettingsForGroup(state.Manifest.Settings, state.Settings, settingOverrides[id]), true
}

// ValidateGroupSettingOverrides validates and normalizes explicit per-group
// values. Secret fields are intentionally rejected and continue to inherit
// the global plugin configuration.
func (m *PluginManager) ValidateGroupSettingOverrides(overrides PluginSettingOverrides) (PluginSettingOverrides, error) {
	if len(overrides) == 0 {
		return nil, nil
	}
	if m == nil {
		return nil, fmt.Errorf("qqbot: plugin manager is not configured")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := PluginSettingOverrides{}
	for id, values := range overrides {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("qqbot: plugin ID cannot be empty")
		}
		plugin, ok := m.catalog[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrPluginNotFound, id)
		}
		normalized, err := normalizeGroupPluginSettings(plugin.Manifest().Settings, values)
		if err != nil {
			return nil, fmt.Errorf("qqbot: plugin %q group settings: %w", id, err)
		}
		if len(normalized) > 0 {
			out[id] = normalized
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// SanitizeGroupSettingOverrides returns a safe snapshot for runtime use or API
// responses. Unknown plugins, stale keys, invalid values, and secrets vanish.
func (m *PluginManager) SanitizeGroupSettingOverrides(overrides PluginSettingOverrides) PluginSettingOverrides {
	if m == nil || len(overrides) == 0 {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := PluginSettingOverrides{}
	for id, values := range overrides {
		id = strings.TrimSpace(id)
		plugin, ok := m.catalog[id]
		if !ok {
			continue
		}
		sanitized := sanitizeGroupPluginSettings(plugin.Manifest().Settings, values)
		if len(sanitized) > 0 {
			out[id] = sanitized
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Snapshot 返回插件状态快照用于持久化。
func (m *PluginManager) Snapshot() map[string]PluginState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// 返回副本，避免外部持久化逻辑反向修改 manager 内部状态。
	out := make(map[string]PluginState, len(m.states))
	for id, state := range m.states {
		out[id] = state
	}
	return out
}

// Restore 从持久化状态恢复插件开关。
func (m *PluginManager) Restore(states map[string]PluginState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, plugin := range m.catalog {
		current := m.states[id]
		current.Manifest = plugin.Manifest()
		if saved, ok := states[id]; ok {
			current.Installed = saved.Installed
			current.Enabled = saved.Enabled
			// 历史数据可能包含已下线的设置键或非法值，恢复时按当前声明清洗。
			current.Settings = sanitizePluginSettings(current.Manifest.Settings, saved.Settings)
		}
		if current.Manifest.BuiltIn {
			// 内置插件不能被彻底卸载，但允许用户在 WebUI 里关闭启用状态。
			current.Installed = true
			if !savedStateDisabled(states, id) {
				current.Enabled = true
			}
		}
		m.states[id] = current
	}
}

// Install 安装并启用指定插件。
func (m *PluginManager) Install(id string) (PluginState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plugin, ok := m.catalog[id]
	if !ok {
		return PluginState{}, ErrPluginNotFound
	}
	state := m.states[id]
	state.Manifest = plugin.Manifest()
	if state.Manifest.BuiltIn {
		return state, ErrBuiltInPluginAction
	}
	state.Installed = true
	state.Enabled = true
	m.states[id] = state
	return state, nil
}

// Uninstall 卸载并关闭指定插件。
func (m *PluginManager) Uninstall(id string) (PluginState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plugin, ok := m.catalog[id]
	if !ok {
		return PluginState{}, ErrPluginNotFound
	}
	state := m.states[id]
	state.Manifest = plugin.Manifest()
	if state.Manifest.BuiltIn {
		return state, ErrBuiltInPluginAction
	}
	state.Installed = false
	state.Enabled = false
	m.states[id] = state
	return state, nil
}

// UpdateSettings 校验并整体替换插件的设置覆盖值，传入空 map 表示恢复全部默认。
// 凭据类设置不会被这个接口清空，要清除请用 UpdateSettingsWithClears。
func (m *PluginManager) UpdateSettings(id string, values map[string]any) (PluginState, error) {
	return m.UpdateSettingsWithClears(id, values, nil)
}

// UpdateSettingsWithClears 在保存设置的同时显式清除指定的凭据。
//
// 凭据的保留规则：读接口只回传「是否已配置」，前端既可能提交空串、也可能
// 整个键都不提交（值等于默认值时会被过滤掉）。这两种都必须视为「没改动」，
// 否则用户改一下超时时间就会把 Cookie 弄丢；真要清除只能走 clear 参数。
func (m *PluginManager) UpdateSettingsWithClears(id string, values map[string]any, clear []string) (PluginState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plugin, ok := m.catalog[id]
	if !ok {
		return PluginState{}, ErrPluginNotFound
	}
	manifest := plugin.Manifest()
	if len(manifest.Settings) == 0 {
		return PluginState{}, fmt.Errorf("qqbot: plugin %q has no configurable settings", id)
	}
	normalized, err := normalizePluginSettings(manifest.Settings, values)
	if err != nil {
		return PluginState{}, err
	}
	state := m.states[id]
	state.Manifest = manifest
	cleared := map[string]bool{}
	for _, key := range clear {
		cleared[strings.TrimSpace(key)] = true
	}
	for key := range secretSettingKeys(manifest.Settings) {
		if cleared[key] {
			delete(normalized, key)
			continue
		}
		// 空串和「整个键没提交」都表示没改动，沿用已存的值。
		if text, _ := normalized[key].(string); strings.TrimSpace(text) != "" {
			if merger, ok := plugin.(SecretSettingMerger); ok {
				previous, _ := state.Settings[key].(string)
				merged, err := merger.MergeSecretSetting(key, previous, text)
				if err != nil {
					return PluginState{}, err
				}
				normalized[key] = merged
			}
			continue
		}
		if previous, ok := state.Settings[key]; ok {
			if normalized == nil {
				normalized = map[string]any{}
			}
			normalized[key] = previous
		} else {
			delete(normalized, key)
		}
	}
	state.Settings = normalized
	m.states[id] = state
	return state, nil
}

// SetEnabled 更新指定插件启用状态。
func (m *PluginManager) SetEnabled(id string, enabled bool) (PluginState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plugin, ok := m.catalog[id]
	if !ok {
		return PluginState{}, ErrPluginNotFound
	}
	state := m.states[id]
	state.Manifest = plugin.Manifest()
	if !state.Installed {
		return state, fmt.Errorf("qqbot: plugin %q is not installed", id)
	}
	if state.Manifest.Internal && !enabled {
		return state, ErrInternalPluginDisable
	}
	state.Enabled = enabled
	m.states[id] = state
	return state, nil
}

// CanAskAgent reports whether an installed, enabled plugin may turn a
// plugin-originated background event into a formal Agent reply turn.
func (m *PluginManager) CanAskAgent(id string, enabledOverrides map[string]bool, settingOverrides PluginSettingOverrides) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	plugin, ok := m.catalog[id]
	if !ok {
		return false
	}
	state := m.states[id]
	enabled := state.Enabled
	if override, overridden := enabledOverrides[id]; overridden {
		enabled = override
	}
	manifest := plugin.Manifest()
	settings := effectivePluginSettingsForGroup(manifest.Settings, state.Settings, settingOverrides[id])
	return state.Installed && enabled && manifest.CanAskAgent && settings.Bool(pluginSettingAskAgent, true)
}

// Run 依次执行已安装且启用的插件。
func (m *PluginManager) Run(ctx context.Context, req PluginRequest) []PluginResponse {
	return m.RunWithOverrides(ctx, req, nil)
}

// RunWithOverrides 依次执行插件，并允许调用方按会话覆盖已安装插件的启用状态。
func (m *PluginManager) RunWithOverrides(ctx context.Context, req PluginRequest, overrides map[string]bool) []PluginResponse {
	return m.RunWithGroupOverrides(ctx, req, overrides, nil)
}

// RunWithGroupOverrides runs plugins with per-conversation enable and setting
// overrides.
func (m *PluginManager) RunWithGroupOverrides(ctx context.Context, req PluginRequest, enabledOverrides map[string]bool, settingOverrides PluginSettingOverrides) []PluginResponse {
	type runnable struct {
		id       string
		plugin   Plugin
		settings SettingValues
	}
	m.mu.RLock()
	plugins := make([]runnable, 0, len(m.catalog))
	for id, plugin := range m.catalog {
		state := m.states[id]
		enabled := state.Enabled
		if override, ok := enabledOverrides[id]; ok {
			enabled = override
		}
		if state.Installed && enabled {
			plugins = append(plugins, runnable{
				id:     id,
				plugin: plugin,
				// 生效设置在锁内合并成快照，插件执行期间的设置变更不影响本次请求。
				settings: effectivePluginSettingsForGroup(state.Manifest.Settings, state.Settings, settingOverrides[id]),
			})
		}
	}
	m.mu.RUnlock()

	responses := make([]PluginResponse, 0, len(plugins))
	for _, item := range plugins {
		pluginReq := req
		pluginReq.Settings = item.settings
		resp, err := safeHandlePlugin(ctx, item.id, item.plugin, pluginReq)
		if err != nil {
			recordPluginFailure(ctx, req, item.id, err)
			continue
		}
		if resp == nil || !resp.Handled {
			// 插件失败或未处理不打断主回复链路，运行时会继续调用其它插件/LLM。
			continue
		}
		responses = append(responses, *resp)
	}
	return responses
}

func (m *PluginManager) RunOneWithOverrides(ctx context.Context, id string, req PluginRequest, overrides map[string]bool) (*PluginResponse, error) {
	return m.RunOneWithGroupOverrides(ctx, id, req, overrides, nil)
}

func (m *PluginManager) RunOneWithGroupOverrides(ctx context.Context, id string, req PluginRequest, enabledOverrides map[string]bool, settingOverrides PluginSettingOverrides) (*PluginResponse, error) {
	if m == nil {
		return nil, nil
	}
	m.mu.RLock()
	plugin, ok := m.catalog[id]
	state := m.states[id]
	enabled := state.Enabled
	if override, overridden := enabledOverrides[id]; overridden {
		enabled = override
	}
	settings := effectivePluginSettingsForGroup(state.Manifest.Settings, state.Settings, settingOverrides[id])
	m.mu.RUnlock()
	if !ok || !state.Installed || !enabled {
		return nil, nil
	}
	req.Settings = settings
	return safeHandlePlugin(ctx, id, plugin, req)
}

// AgentToolsWithOverrides returns a stable snapshot of tools contributed by
// installed and enabled plugins for one conversation.
func (m *PluginManager) AgentToolsWithOverrides(overrides map[string]bool) ([]agent.Tool, error) {
	return m.AgentToolsWithGroupOverrides(overrides, nil)
}

// AgentToolsWithGroupOverrides resolves plugin settings for the current
// conversation before constructing event-bound tools.
func (m *PluginManager) AgentToolsWithGroupOverrides(enabledOverrides map[string]bool, settingOverrides PluginSettingOverrides) ([]agent.Tool, error) {
	if m == nil {
		return nil, nil
	}
	type provider struct {
		id       string
		plugin   AgentToolPlugin
		settings SettingValues
	}

	m.mu.RLock()
	providers := make([]provider, 0, len(m.catalog))
	for id, plugin := range m.catalog {
		toolPlugin, ok := plugin.(AgentToolPlugin)
		if !ok {
			continue
		}
		state := m.states[id]
		enabled := state.Enabled
		if override, ok := enabledOverrides[id]; ok {
			enabled = override
		}
		if !state.Installed || !enabled {
			continue
		}
		providers = append(providers, provider{
			id:       id,
			plugin:   toolPlugin,
			settings: effectivePluginSettingsForGroup(state.Manifest.Settings, state.Settings, settingOverrides[id]),
		})
	}
	m.mu.RUnlock()
	slices.SortFunc(providers, func(a, b provider) int { return strings.Compare(a.id, b.id) })

	var tools []agent.Tool
	for _, item := range providers {
		provided, err := item.plugin.AgentTools(item.settings)
		if err != nil {
			return nil, fmt.Errorf("qqbot: plugin %q agent tools: %w", item.id, err)
		}
		tools = append(tools, provided...)
	}

	m.mu.RLock()
	legacyProviders := make([]AgentToolProviderPlugin, 0)
	for id, plugin := range m.catalog {
		provider, ok := plugin.(AgentToolProviderPlugin)
		if !ok {
			continue
		}
		state := m.states[id]
		enabled := state.Enabled
		if override, overridden := enabledOverrides[id]; overridden {
			enabled = override
		}
		if state.Installed && enabled {
			legacyProviders = append(legacyProviders, provider)
		}
	}
	m.mu.RUnlock()
	for _, provider := range legacyProviders {
		tools = append(tools, provider.AgentTools()...)
	}
	return tools, nil
}

func (m *PluginManager) SetLocalMediaSharer(sharer LocalMediaSharer) {
	if m == nil {
		return
	}
	m.mu.RLock()
	plugins := make([]LocalMediaSharerAwarePlugin, 0)
	for _, plugin := range m.catalog {
		if aware, ok := plugin.(LocalMediaSharerAwarePlugin); ok {
			plugins = append(plugins, aware)
		}
	}
	m.mu.RUnlock()
	for _, plugin := range plugins {
		plugin.SetLocalMediaSharer(sharer)
	}
}

func (m *PluginManager) ObserveEvent(ctx context.Context, event MessageEvent) MessageEvent {
	if m == nil {
		return event
	}
	m.mu.RLock()
	type observerEntry struct {
		id       string
		observer EventObserverPlugin
	}
	observers := make([]observerEntry, 0)
	for id, plugin := range m.catalog {
		state := m.states[id]
		observer, ok := plugin.(EventObserverPlugin)
		if state.Installed && state.Enabled && ok {
			observers = append(observers, observerEntry{id: id, observer: observer})
		}
	}
	m.mu.RUnlock()
	for _, entry := range observers {
		event = safeObservePlugin(ctx, entry.id, entry.observer, event)
	}
	return event
}

func safeObservePlugin(ctx context.Context, id string, observer EventObserverPlugin, event MessageEvent) (result MessageEvent) {
	result = event
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("plugin %s observer failed: %v", id, recovered)
			result = event
		}
	}()
	return observer.Observe(ctx, event)
}

func safeHandlePlugin(ctx context.Context, id string, plugin Plugin, req PluginRequest) (resp *PluginResponse, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			resp = nil
			err = fmt.Errorf("qqbot: plugin %q panicked: %v", id, recovered)
		}
	}()
	return plugin.Handle(ctx, req)
}

func recordPluginFailure(ctx context.Context, req PluginRequest, id string, err error) {
	if err == nil {
		return
	}
	log.Printf("plugin %s failed: %v", id, err)
	if req.AppLogs == nil {
		return
	}
	_ = req.AppLogs.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindError,
		Level:   applog.LevelError,
		Action:  "assistant.plugin.execute",
		Message: "插件执行失败",
		Detail:  err.Error(),
		Actor:   strings.TrimSpace(req.Event.UserID),
		Target:  id,
		Metadata: map[string]any{
			"group_id": req.Event.GroupID,
		},
	})
}

// savedStateDisabled 判断保存状态里插件是否被显式关闭。
func savedStateDisabled(states map[string]PluginState, id string) bool {
	state, ok := states[id]
	return ok && !state.Enabled
}

const (
	resolverSettingFetchTitle       = "fetch_title"
	resolverSettingMaxLinks         = "max_links"
	resolverSettingTimeoutSeconds   = "timeout_seconds"
	resolverSettingBrowserRender    = "browser_render"
	resolverSettingBrowserCDPURL    = "browser_cdp_url"
	resolverSettingExcludePlatforms = "exclude_platforms"
	resolverSettingSummaryMaxRunes  = "summary_max_runes"
	resolverSettingCacheTTLMinutes  = "cache_ttl_minutes"
	resolverSettingUserAgent        = "user_agent"
	resolverSettingDownloadMedia    = "download_media"
	resolverSettingMaxVideoMB       = "max_video_mb"
	resolverSettingMaxDuration      = "max_video_duration_seconds"
	resolverSettingMaxImages        = "max_images"
	resolverSettingFollowUpComment  = "follow_up_comment"
	resolverSettingMaxVideoHeight   = "max_video_height"
	// 凭据类设置。这些值最容易过期、最需要频繁更换，只靠环境变量意味着
	// Docker 用户改一次 Cookie 就得重启容器。
	resolverSettingBiliSessdata = "bili_sessdata"
	resolverSettingDouyinCookie = "douyin_cookie"
	resolverSettingXHSCookie    = "xhs_cookie"
	resolverSettingYTDLPCookies = "ytdlp_cookies_path"
	resolverSettingProxyURL     = "proxy_url"

	defaultResolverMaxLinks        = 5
	defaultResolverTimeoutSeconds  = 8
	maxResolverTimeoutSeconds      = 30
	defaultResolverBrowserCDPURL   = "http://127.0.0.1:9222"
	defaultResolverSummaryMaxRunes = 140
	defaultResolverCacheTTLMinutes = 10

	// 浏览器渲染要等页面 JS 补齐内容，超时独立于普通抓取，宽松一些。
	resolverBrowserTimeout = 15 * time.Second
)

// browserFetchFunc 通过 CDP 渲染页面并提取元数据，测试里可注入桩实现。
type browserFetchFunc func(ctx context.Context, cdpURL string, pageURL string) (agent.RenderedPage, error)

type ResolverPlugin struct {
	client          *http.Client
	cache           resolverCache
	browserFetch    browserFetchFunc
	mediaDownloader func(context.Context, string) string
	// videoDownloader is the legacy injection point retained for the complete
	// resolver implementation and its integrations.
	videoDownloader        func(context.Context, string) string
	twitterPostFetcher     func(context.Context, string) (twitterPost, bool)
	twitterMediaDownloader func(context.Context, twitterMedia) string
}

// NewResolverPlugin 创建官方内置链接解析插件。
func NewResolverPlugin(client *http.Client) *ResolverPlugin {
	if client == nil {
		// 单个链接的超时由 resolveURL 按设置生成的 context 控制，client 只兜住设置上限。
		client = &http.Client{Timeout: maxResolverTimeoutSeconds * time.Second}
	}
	return &ResolverPlugin{
		client:          client,
		mediaDownloader: downloadPlatformVideoFile,
		browserFetch: func(ctx context.Context, cdpURL string, pageURL string) (agent.RenderedPage, error) {
			return agent.FetchRenderedPage(ctx, cdpURL, pageURL, resolverBrowserTimeout, 4000)
		},
	}
}

// Manifest 返回链接解析插件清单。
func (p *ResolverPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          resolverPluginID,
		Name:        "链接解析",
		Version:     "0.2.0",
		Description: "官方内置 Go 社交媒体解析器，可提取并发送 B 站、YouTube、X、小红书和抖音的图片或视频。",
		Official:    true,
		BuiltIn:     true,
		CanAskAgent: true,
		Permissions: []string{"network:http", "message:read", "message:write", "filesystem:temp", "process:media"},
		Settings: []PluginSettingSpec{
			{
				Key:         pluginSettingAskAgent,
				Label:       "允许 Agent 回复",
				Description: "允许链接解析结果主动进入正式 Agent 回复流程；关闭后仍会解析和发送确定性结果。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         resolverSettingFollowUpComment,
				Label:       "发送后自然点评",
				Description: "解析结果发出后，让机器人再用一句话说说自己的反应，像群友那样；关闭后只发解析结果。",
				Type:        PluginSettingTypeBool,
				Default:     false,
			},
			{
				Key:         resolverSettingDownloadMedia,
				Label:       "下载并发送媒体",
				Description: "识别支持的平台后下载视频或提取图集，并通过当前机器人发送；关闭后只提供网页上下文。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         resolverSettingMaxVideoMB,
				Label:       "视频大小上限",
				Description: "下载和发送的单个视频最大体积。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultVideoMaxMB,
				Min:         settingRange(5),
				Max:         settingRange(500),
				Step:        5,
				Unit:        "MB",
			},
			{
				Key:         resolverSettingMaxDuration,
				Label:       "视频时长上限",
				Description: "超过该时长的视频只返回元数据，不执行下载。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultVideoMaxDuration,
				Min:         settingRange(30),
				Max:         settingRange(3600),
				Step:        30,
				Unit:        "秒",
			},
			{
				Key:         resolverSettingMaxImages,
				Label:       "图集发送上限",
				Description: "单条社交链接最多发送的图片数量。",
				Type:        PluginSettingTypeNumber,
				Default:     9,
				Min:         settingRange(1),
				Max:         settingRange(20),
				Step:        1,
				Unit:        "张",
			},
			{
				Key:   resolverSettingMaxVideoHeight,
				Label: "视频清晰度上限",
				// 注意和大小上限的联动：调高清晰度但不放大 max_video_mb，
				// 结果是下完才发现超限被丢弃，白费带宽和时间。
				Description: "下载时选择的最高分辨率。调高后建议同步放宽「视频大小上限」，否则容易下完才因超限被丢弃。",
				Type:        PluginSettingTypeSelect,
				Default:     "720",
				Options: []PluginSettingOption{
					{Value: "480", Label: "480p"},
					{Value: "720", Label: "720p（默认）"},
					{Value: "1080", Label: "1080p"},
					{Value: "0", Label: "不限制（可能很大）"},
				},
			},
			{
				Key:         resolverSettingFetchTitle,
				Label:       "抓取网页标题",
				Description: "关闭后只识别链接平台，不再请求网页获取标题。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         resolverSettingMaxLinks,
				Label:       "单条消息解析上限",
				Description: "一条消息里最多解析多少个链接，超出部分忽略。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultResolverMaxLinks,
				Min:         settingRange(1),
				Max:         settingRange(20),
				Step:        1,
				Unit:        "个",
			},
			{
				Key:         resolverSettingTimeoutSeconds,
				Label:       "单链接抓取超时",
				Description: "抓取单个网页标题的最长等待时间。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultResolverTimeoutSeconds,
				Min:         settingRange(2),
				Max:         settingRange(maxResolverTimeoutSeconds),
				Step:        1,
				Unit:        "秒",
			},
			{
				Key:         resolverSettingBrowserRender,
				Label:       "浏览器渲染兜底",
				Description: "直接抓取拿不到内容时，用本机 Chrome（CDP）渲染页面提取标题和摘要，可利用浏览器里的登录态；需要 Chrome 以 --remote-debugging-port 启动。",
				Type:        PluginSettingTypeBool,
				Default:     false,
			},
			{
				Key:         resolverSettingBrowserCDPURL,
				Label:       "Chrome CDP 地址",
				Description: "浏览器渲染兜底连接的 DevTools 地址。",
				Type:        PluginSettingTypeString,
				Default:     defaultResolverBrowserCDPURL,
			},
			{
				Key:         resolverSettingExcludePlatforms,
				Label:       "排除平台",
				Description: "勾选的平台不解析链接，也不进入模型上下文。",
				Type:        PluginSettingTypeMultiSelect,
				Default:     []string{},
				Options:     resolverPlatformOptions(),
			},
			{
				Key:         resolverSettingSummaryMaxRunes,
				Label:       "摘要长度上限",
				Description: "喂给模型的单条摘要最大字数。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultResolverSummaryMaxRunes,
				Min:         settingRange(60),
				Max:         settingRange(400),
				Step:        10,
				Unit:        "字",
			},
			{
				Key:         resolverSettingCacheTTLMinutes,
				Label:       "解析缓存时长",
				Description: "同一链接在缓存有效期内不重复抓取；0 表示关闭缓存。",
				Type:        PluginSettingTypeNumber,
				Default:     defaultResolverCacheTTLMinutes,
				Min:         settingRange(0),
				Max:         settingRange(120),
				Step:        1,
				Unit:        "分钟",
			},
			{
				Key:         resolverSettingUserAgent,
				Label:       "抓取 User-Agent",
				Description: "直接抓取网页时使用的 UA，留空使用内置 Chrome UA。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         resolverSettingBiliSessdata,
				Label:       "B 站 SESSDATA",
				Description: "B 站登录 Cookie 中的 SESSDATA，用于需要登录态的内容。留空则沿用 DIANA_BILI_SESSDATA 环境变量。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         resolverSettingDouyinCookie,
				Label:       "抖音 Cookie",
				Description: "抖音解析必需，不配置无法解析。留空则沿用 DIANA_DOUYIN_CK 环境变量。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         resolverSettingXHSCookie,
				Label:       "小红书 Cookie",
				Description: "小红书解析必需，不配置无法解析。留空则沿用 DIANA_XHS_CK 环境变量。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         resolverSettingYTDLPCookies,
				Label:       "yt-dlp Cookie 文件路径",
				Description: "Netscape 格式 Cookie 文件路径，供 YouTube/X 等需要登录的内容使用。留空则沿用 DIANA_YTDLP_COOKIES 环境变量。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         resolverSettingProxyURL,
				Label:       "解析代理",
				Description: "社交媒体解析与 yt-dlp 使用的代理地址，例如 http://127.0.0.1:7890。留空则沿用 DIANA_RESOLVER_PROXY 环境变量。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
		},
	}
}

// resolverMaxHeightFromSetting 把清晰度选项解析成像素高度，"0" 表示不限。
func resolverMaxHeightFromSetting(settings SettingValues) int {
	raw := strings.TrimSpace(settings.String(resolverSettingMaxVideoHeight, ""))
	if raw == "" {
		return defaultVideoMaxHeight
	}
	height, err := strconv.Atoi(raw)
	if err != nil || height < 0 {
		return defaultVideoMaxHeight
	}
	return height
}

// resolverCredentials 是一次解析用到的凭据与代理，由插件设置注入、
// 缺省时回落环境变量，这样现有部署不改任何东西也不会坏。
type resolverCredentials struct {
	BiliSessdata string
	DouyinCookie string
	XHSCookie    string
	YTDLPCookies string
	ProxyURL     string
}

// resolverCredentialsFromSettings 读取插件设置，未配置的项留空交给环境变量兜底。
func resolverCredentialsFromSettings(settings SettingValues) resolverCredentials {
	return resolverCredentials{
		BiliSessdata: strings.TrimSpace(settings.String(resolverSettingBiliSessdata, "")),
		DouyinCookie: strings.TrimSpace(settings.String(resolverSettingDouyinCookie, "")),
		XHSCookie:    strings.TrimSpace(settings.String(resolverSettingXHSCookie, "")),
		YTDLPCookies: strings.TrimSpace(settings.String(resolverSettingYTDLPCookies, "")),
		ProxyURL:     strings.TrimSpace(settings.String(resolverSettingProxyURL, "")),
	}
}

// resolveOptions 汇总一次消息解析用到的生效设置。
type resolveOptions struct {
	fetchTitle       bool
	httpTimeout      time.Duration
	browserRender    bool
	browserCDPURL    string
	excludePlatforms []string
	summaryMaxRunes  int
	cacheTTL         time.Duration
	userAgent        string
}

// Handle 解析消息中的链接并生成上下文。
func (p *ResolverPlugin) Handle(ctx context.Context, req PluginRequest) (*PluginResponse, error) {
	urls := extractResolverRequestURLs(req)
	if len(urls) == 0 {
		return nil, nil
	}
	if maxLinks := req.Settings.Int(resolverSettingMaxLinks, defaultResolverMaxLinks); len(urls) > maxLinks {
		urls = urls[:maxLinks]
	}
	opts := resolveOptions{
		fetchTitle:       req.Settings.Bool(resolverSettingFetchTitle, true),
		httpTimeout:      time.Duration(req.Settings.Int(resolverSettingTimeoutSeconds, defaultResolverTimeoutSeconds)) * time.Second,
		browserRender:    req.Settings.Bool(resolverSettingBrowserRender, false),
		browserCDPURL:    req.Settings.String(resolverSettingBrowserCDPURL, defaultResolverBrowserCDPURL),
		excludePlatforms: req.Settings.StringSlice(resolverSettingExcludePlatforms),
		summaryMaxRunes:  req.Settings.Int(resolverSettingSummaryMaxRunes, defaultResolverSummaryMaxRunes),
		cacheTTL:         time.Duration(req.Settings.Int(resolverSettingCacheTTLMinutes, defaultResolverCacheTTLMinutes)) * time.Minute,
		userAgent:        strings.TrimSpace(req.Settings.String(resolverSettingUserAgent, "")),
	}

	parts := make([]string, 0, len(urls))
	directParts := make([]string, 0, len(urls))
	imageURLs := make([]string, 0)
	videoURLs := make([]string, 0)
	forwardMessages := make([]OutgoingMessage, 0)
	deferredToBrowser := false
	// PluginManager always injects effective settings. Direct unit callers that
	// omit Settings keep the legacy metadata-only behavior and never perform a
	// real media download unexpectedly.
	downloadMedia := len(req.Settings) > 0 && req.Settings.Bool(resolverSettingDownloadMedia, true)
	if p.videoDownloader != nil || p.twitterPostFetcher != nil || p.twitterMediaDownloader != nil {
		downloadMedia = true
	}
	// 凭据挂在 ctx 上一路带到底层下载函数；没在设置里配的项会在取值时
	// 回落到对应环境变量，现有部署不受影响。
	ctx = withResolverCredentials(ctx, resolverCredentialsFromSettings(req.Settings))
	mediaCtx := withResolverMediaLimits(
		ctx,
		req.Settings.Int(resolverSettingMaxVideoMB, defaultVideoMaxMB),
		req.Settings.Int(resolverSettingMaxDuration, defaultVideoMaxDuration),
		resolverMaxHeightFromSetting(req.Settings),
	)
	maxImages := req.Settings.Int(resolverSettingMaxImages, 9)
	legacyResolver := p.videoDownloader != nil || p.twitterPostFetcher != nil || p.twitterMediaDownloader != nil
	for _, raw := range urls {
		if len(opts.excludePlatforms) > 0 {
			if parsed, err := url.Parse(raw); err == nil {
				key, _ := platformKeyAndLabel(parsed.Hostname())
				if key != "" && slices.Contains(opts.excludePlatforms, key) {
					// 勾选排除的平台整条跳过，不进上下文。
					continue
				}
			}
		}
		if legacyResolver && isKnownResolverPlatformURL(raw) {
			if isTwitterURL(raw) && !twitterResolverRequestAllowed(mediaCtx, req) {
				continue
			}
			media := p.resolveKnownPlatform(mediaCtx, req, raw)
			if strings.TrimSpace(media.Context) != "" {
				parts = append(parts, media.Context)
				directParts = append(directParts, media.Context)
			}
			imageURLs = append(imageURLs, media.ImageURLs...)
			videoURLs = append(videoURLs, media.VideoURLs...)
			forwardMessages = append(forwardMessages, media.ForwardMessages...)
			continue
		}
		if downloadMedia {
			if media := p.resolveSocialMedia(mediaCtx, req, raw, maxImages); media.Suppressed {
				continue
			} else if media.Handled {
				media.ForwardMessages = resolverSocialForwardMessages(media)
				if strings.TrimSpace(media.Context) != "" {
					parts = append(parts, media.Context)
					directParts = append(directParts, media.Context)
				}
				imageURLs = append(imageURLs, media.ImageURLs...)
				videoURLs = append(videoURLs, media.VideoURLs...)
				forwardMessages = append(forwardMessages, media.ForwardMessages...)
				continue
			}
		}
		if req.SandboxedBrowserEnabled && !isKnownResolverPlatformURL(raw) {
			deferredToBrowser = true
			continue
		}
		parts = append(parts, p.resolveURL(ctx, raw, opts))
	}
	if len(parts) == 0 {
		if deferredToBrowser {
			return &PluginResponse{Handled: true}, nil
		}
		return nil, nil
	}
	response := &PluginResponse{
		Handled:         true,
		Context:         "链接解析结果：\n" + strings.Join(parts, "\n"),
		ImageURLs:       dedupeMediaURLs(imageURLs),
		VideoURLs:       dedupeMediaURLs(videoURLs),
		Forward:         len(forwardMessages) > 0,
		ForwardMessages: forwardMessages,
	}
	if len(forwardMessages) > 0 && len(parts) == 1 {
		response.Context = strings.TrimSpace(parts[0])
	}
	if len(directParts) > 0 {
		response.Reply = strings.Join(directParts, "\n\n")
	} else {
		// Context 前缀是给模型的来源标签，发到群里不该出现「链接解析结果：」这种机器腔。
		response.Reply = strings.Join(parts, "\n")
	}
	response.FollowUp = req.Settings.Bool(resolverSettingFollowUpComment, false)
	return response, nil
}

var (
	urlPattern                = regexp.MustCompile(`https?://[^\s<>"'，。！？、]+`)
	bareXiaohongshuURLPattern = regexp.MustCompile(`(?i)(^|[^a-z0-9._@-])((?:www\.)?(?:xhslink\.com|xiaohongshu\.com)/[^\s<>"'，。！？、]+)`)
	bilibiliVideoIDPattern    = regexp.MustCompile(`(?i)\b(BV[a-z0-9]{10})\b`)
)

func extractResolverRequestURLs(req PluginRequest) []string {
	return dedupeURLs(append(append(
		extractURLs(req.Text),
		extractURLs(req.Event.RawMessage)...,
	), extractURLs(PlainText(req.Event.Segments))...))
}

// extractURLs 从消息文本中提取并去重 URL。
func extractURLs(text string) []string {
	var out []string
	for _, candidate := range resolverURLTextVariants(text) {
		for _, match := range urlPattern.FindAllString(candidate, -1) {
			match = normalizeResolverURL(match)
			if match != "" {
				out = append(out, match)
			}
		}
		withoutURLs := urlPattern.ReplaceAllString(candidate, " ")
		for _, match := range bareXiaohongshuURLPattern.FindAllStringSubmatch(withoutURLs, -1) {
			if len(match) < 3 {
				continue
			}
			if normalized := normalizeResolverURL("https://" + match[2]); normalized != "" {
				out = append(out, normalized)
			}
		}
		for _, match := range bilibiliVideoIDPattern.FindAllStringSubmatch(withoutURLs, -1) {
			if len(match) < 2 {
				continue
			}
			bvid := "BV" + match[1][2:]
			out = append(out, "https://www.bilibili.com/video/"+bvid)
		}
	}
	return dedupeURLs(out)
}

func resolverURLTextVariants(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	variants := []string{text}
	current := text
	for i := 0; i < 3; i++ {
		next := html.UnescapeString(current)
		if next == current {
			break
		}
		variants = append(variants, next)
		current = next
	}
	baseLen := len(variants)
	for i := 0; i < baseLen; i++ {
		decoded := decodeResolverEscapedURLText(variants[i])
		if decoded != variants[i] {
			variants = append(variants, decoded)
		}
	}
	return variants
}

func decodeResolverEscapedURLText(text string) string {
	return strings.NewReplacer(
		`\/`, `/`,
		`\u002f`, `/`,
		`\u002F`, `/`,
		`\\u002f`, `/`,
		`\\u002F`, `/`,
	).Replace(text)
}

func dedupeURLs(urls []string) []string {
	out := make([]string, 0, len(urls))
	seen := map[string]struct{}{}
	for _, raw := range urls {
		raw = normalizeResolverURL(raw)
		if raw == "" {
			continue
		}
		key := resolverURLDedupeKey(raw)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, raw)
	}
	return out
}

func normalizeResolverURL(raw string) string {
	raw = strings.TrimSpace(raw)
	for i := 0; i < 3; i++ {
		next := html.UnescapeString(raw)
		if next == raw {
			break
		}
		raw = next
	}
	raw = decodeResolverEscapedURLText(raw)
	return strings.TrimRight(raw, ".,;:!?)]}\\")
}

func resolverURLDedupeKey(raw string) string {
	parsed, err := url.Parse(normalizeResolverURL(raw))
	if err != nil {
		return raw
	}
	parsed.Fragment = ""
	return parsed.String()
}

func nonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func firstNonEmptyString(values []string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func knownResolverPlatformURLs(text string) []string {
	urls := extractURLs(text)
	out := make([]string, 0, len(urls))
	for _, raw := range urls {
		if isKnownResolverPlatformURL(raw) {
			out = append(out, raw)
		}
	}
	return out
}

func isKnownResolverPlatformURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return isKnownResolverPlatformHost(parsed.Hostname())
}

func isKnownResolverPlatformHost(host string) bool {
	return hostMatchesDomain(host, "bilibili.com", "b23.tv", "bili2233.cn") ||
		hostMatchesDomain(host, "youtube.com", "youtu.be") ||
		hostMatchesDomain(host, "x.com", "twitter.com") ||
		hostMatchesDomain(host, "xiaohongshu.com", "xhslink.com") ||
		hostMatchesDomain(host, "douyin.com")
}

// resolveURL 获取链接平台、标题和摘要。
func (p *ResolverPlugin) resolveURL(ctx context.Context, raw string, opts resolveOptions) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "- " + raw
	}
	platform := platformName(parsed.Hostname())
	if !opts.fetchTitle {
		return fmt.Sprintf("- [%s] %s", platform, raw)
	}

	meta, ok := p.fetchPageMeta(ctx, raw, opts)
	// 短链跳转后按最终地址重新识别平台，b23.tv/xhslink 才能显示成 B 站/小红书。
	if meta.FinalURL != "" && meta.FinalURL != raw {
		if finalParsed, err := url.Parse(meta.FinalURL); err == nil {
			platform = platformName(finalParsed.Hostname())
		}
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "- [%s] %s", platform, raw)
	if meta.Title != "" {
		fmt.Fprintf(&builder, "\n  标题：%s", meta.Title)
	}
	if meta.Description != "" {
		description := meta.Description
		// 输出时按设置截断；缓存里始终存硬上限内的近全量摘要。
		if limit := opts.summaryMaxRunes; limit > 0 {
			if runes := []rune(description); len(runes) > limit {
				description = string(runes[:limit]) + "…"
			}
		}
		fmt.Fprintf(&builder, "\n  摘要：%s", description)
	}
	if !ok {
		// 明确告诉模型抓取失败，回复时不至于凭空编造网页内容。
		builder.WriteString("\n  备注：未能获取网页内容（可能需要登录或被站点风控），不要编造页面内容。")
	}
	return builder.String()
}

// platformName 根据域名识别常见平台名称。
// resolverPlatforms 是链接解析认识的平台清单；排除平台勾选项与平台识别共用同一张表。
//
// media 表示这个平台有真正的图片/视频提取分支（见 resolveSocialMedia 的分发）。
// 为 false 的平台只能抓标题和描述——界面上必须区分标注，否则用户看到「微博」
// 会以为支持解析视频。
var resolverPlatforms = []struct {
	key   string
	label string
	hosts []string
	media bool
}{
	{"bilibili", "Bilibili", []string{"bilibili.com", "b23.tv"}, true},
	{"youtube", "YouTube", []string{"youtube.com", "youtu.be"}, true},
	{"x", "X / Twitter", []string{"x.com", "twitter.com"}, true},
	{"xiaohongshu", "小红书", []string{"xiaohongshu.com", "xhslink.com"}, true},
	{"zhihu", "知乎", []string{"zhihu.com"}, false},
	{"weibo", "微博", []string{"weibo.com", "weibo.cn"}, false},
	{"douyin", "抖音", []string{"douyin.com"}, true},
	{"github", "GitHub", []string{"github.com"}, false},
}

// platformSupportsMedia 判断平台是否有真正的媒体提取分支。
// resolveSocialMedia 的分发和排除平台的标注都以这里为准，避免三处清单各自漂移。
func platformSupportsMedia(key string) bool {
	for _, platform := range resolverPlatforms {
		if platform.key == key {
			return platform.media
		}
	}
	return false
}

// platformKeyAndLabel 识别域名对应的平台键与展示名；未知平台键为空、展示名回退域名。
func platformKeyAndLabel(host string) (string, string) {
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	for _, platform := range resolverPlatforms {
		for _, candidate := range platform.hosts {
			if host == candidate || strings.HasSuffix(host, "."+candidate) {
				return platform.key, platform.label
			}
		}
	}
	return "", host
}

// platformName 根据域名识别常见平台名称。
func platformName(host string) string {
	_, label := platformKeyAndLabel(host)
	return label
}

// resolverPlatformOptions 生成排除平台设置的勾选项。
func resolverPlatformOptions() []PluginSettingOption {
	options := make([]PluginSettingOption, 0, len(resolverPlatforms))
	for _, platform := range resolverPlatforms {
		label := platform.label
		if platform.media {
			label += "（可下载媒体）"
		} else {
			label += "（仅标题）"
		}
		options = append(options, PluginSettingOption{Value: platform.key, Label: label})
	}
	return options
}

var titlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// extractHTMLTitle 从 HTML 片段中提取 title。
func extractHTMLTitle(html string) string {
	match := titlePattern.FindStringSubmatch(html)
	if len(match) < 2 {
		return ""
	}
	return unescapeHTMLText(match[1])
}

// compactWhitespace 压缩文本中的连续空白。
func compactWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
