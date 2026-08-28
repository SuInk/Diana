// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type dianaConfigTool struct {
	runtime *Runtime
}

type dianaConfigSnapshot struct {
	Note        string                   `json:"note"`
	Runtime     dianaRuntimeSnapshot     `json:"runtime,omitempty"`
	Bot         dianaBotConfigSnapshot   `json:"bot,omitempty"`
	LLM         dianaLLMSnapshot         `json:"llm,omitempty"`
	Skills      []dianaPluginSkillState  `json:"installed_skills,omitempty"`
	RuntimePath dianaRuntimePathSnapshot `json:"runtime_paths,omitempty"`
}

type dianaRuntimeSnapshot struct {
	Running       bool                `json:"running"`
	Channel       ChannelStatus       `json:"channel"`
	NoneBotBridge NoneBotBridgeStatus `json:"nonebot_bridge"`
	ActiveWorkers int                 `json:"active_workers"`
	LastError     string              `json:"last_error,omitempty"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

type dianaBotConfigSnapshot struct {
	ID                              string                   `json:"id,omitempty"`
	Name                            string                   `json:"name,omitempty"`
	Platform                        string                   `json:"platform,omitempty"`
	AvatarURL                       string                   `json:"avatar_url,omitempty"`
	Enabled                         bool                     `json:"enabled"`
	OneBotReverseWSEndpoint         string                   `json:"onebot_reverse_ws_endpoint,omitempty"`
	OneBotAccessTokenConfigured     bool                     `json:"onebot_access_token_configured"`
	NoneBotBridgeEnabled            bool                     `json:"nonebot_bridge_enabled"`
	NoneBotBridgeEndpoint           string                   `json:"nonebot_bridge_endpoint,omitempty"`
	NoneBotBridgeTokenConfigured    bool                     `json:"nonebot_bridge_token_configured"`
	BotAccount                      string                   `json:"bot_account,omitempty"`
	OwnerID                         string                   `json:"owner_id,omitempty"`
	OwnerLoginEnabled               bool                     `json:"owner_login_enabled"`
	GroupTriggers                   []string                 `json:"group_triggers,omitempty"`
	GroupTriggerMode                AliasTriggerMode         `json:"group_trigger_mode,omitempty"`
	DisabledGroups                  []string                 `json:"disabled_groups,omitempty"`
	DisabledUsers                   []string                 `json:"disabled_users,omitempty"`
	GroupAdmission                  GroupAdmission           `json:"group_admission"`
	ReplyGate                       *ReplyGate               `json:"reply_gate,omitempty"`
	WelcomeEnabled                  bool                     `json:"welcome_enabled"`
	WelcomeMessage                  string                   `json:"welcome_message,omitempty"`
	SystemPromptConfigured          bool                     `json:"system_prompt_configured"`
	SystemPromptChars               int                      `json:"system_prompt_chars,omitempty"`
	ReplyReferenceMode              ReplyDecorationMode      `json:"reply_reference_mode"`
	MentionUserMode                 ReplyDecorationMode      `json:"mention_user_mode"`
	MarkdownToPlain                 bool                     `json:"markdown_to_plain"`
	ErrorNotifyEnabled              bool                     `json:"error_notify_enabled"`
	ErrorReplyPrefix                string                   `json:"error_reply_prefix,omitempty"`
	SendRetryAttempts               int                      `json:"send_retry_attempts"`
	SendChunkIntervalMS             int                      `json:"send_chunk_interval_ms"`
	PromptInjectTime                bool                     `json:"prompt_inject_time"`
	PromptInjectPlaintextRules      bool                     `json:"prompt_inject_plaintext_rules"`
	PromptInjectGroupSender         bool                     `json:"prompt_inject_group_sender"`
	PromptChineseSlangHint          bool                     `json:"prompt_chinese_slang_hint"`
	PromptChineseSlangChars         int                      `json:"prompt_chinese_slang_chars,omitempty"`
	PromptPlaintextRulesChars       int                      `json:"prompt_plaintext_rules_chars,omitempty"`
	PromptTimeTemplateConfigured    bool                     `json:"prompt_time_template_configured"`
	PromptGroupSenderConfigured     bool                     `json:"prompt_group_sender_template_configured"`
	PromptImageOnlyConfigured       bool                     `json:"prompt_image_only_text_configured"`
	PromptWakeOnlyConfigured        bool                     `json:"prompt_wake_only_text_configured"`
	ModelRoles                      map[string]ModelRole     `json:"model_roles,omitempty"`
	BotReplyLoopDetectionEnabled    bool                     `json:"bot_reply_loop_detection_enabled"`
	ProactiveReplyRouterPromptChars int                      `json:"proactive_reply_router_prompt_chars,omitempty"`
	ProactiveReplyPromptChars       int                      `json:"proactive_reply_prompt_chars,omitempty"`
	MaxInputChars                   int                      `json:"max_input_chars"`
	MaxReplyChars                   int                      `json:"max_reply_chars"`
	DirectReplyChunkSize            int                      `json:"direct_reply_chunk_size"`
	ForwardReplyThreshold           int                      `json:"forward_reply_threshold"`
	RecallReplyMode                 RecallReplyMode          `json:"recall_reply_mode"`
	RecallReplyAutoDeleteEnabled    bool                     `json:"recall_reply_auto_delete_enabled"`
	RecallReplyTTLSeconds           int                      `json:"recall_reply_auto_delete_delay_seconds"`
	OwnerLLMConfigEnabled           bool                     `json:"owner_llm_config_enabled"`
	LLMIdentityMaskingEnabled       bool                     `json:"llm_identity_masking_enabled"`
	RecentContextLimit              int                      `json:"recent_context_limit"`
	ContextSummaryThreshold         int                      `json:"context_summary_threshold"`
	LongTermMemoryEnabled           bool                     `json:"long_term_memory_enabled"`
	CrossGroupMemoryEnabled         bool                     `json:"cross_group_memory_enabled"`
	DictSegmentEnabled              bool                     `json:"dict_segment_enabled"`
	SemanticSearchEnabled           bool                     `json:"semantic_search_enabled"`
	ProactiveReplyChance            float64                  `json:"proactive_reply_chance"`
	ProactiveReplyThreshold         float64                  `json:"proactive_reply_threshold"`
	ChatInEnabled                   bool                     `json:"chat_in_enabled"`
	ChatInLevel                     string                   `json:"chat_in_level"`
	ChatInThreshold                 float64                  `json:"chat_in_threshold"`
	ChatInChance                    float64                  `json:"chat_in_chance"`
	ChatInCooldownSeconds           int                      `json:"chat_in_cooldown_seconds"`
	NaturalInterjectionEnabled      bool                     `json:"natural_interjection_enabled"`
	ReplyRules                      []ReplyRule              `json:"reply_rules,omitempty"`
	MaxBotConcurrency               int                      `json:"max_bot_concurrency"`
	RequestTimeoutMS                int64                    `json:"request_timeout_ms"`
	Agent                           dianaAgentConfigSnapshot `json:"agent"`
}

type dianaAgentConfigSnapshot struct {
	Enabled          bool     `json:"enabled"`
	WorkDir          string   `json:"work_dir,omitempty"`
	MaxSteps         int      `json:"max_steps"`
	SkillRoots       []string `json:"skill_roots,omitempty"`
	MCPConfigPath    string   `json:"mcp_config_path,omitempty"`
	CommandAllowlist []string `json:"command_allowlist,omitempty"`
	CommandTimeoutMS int      `json:"command_timeout_ms"`
	BrowserCDPURL    string   `json:"browser_cdp_url,omitempty"`
	BrowserTimeoutMS int      `json:"browser_timeout_ms"`
}

type dianaLLMSnapshot struct {
	Profiles []dianaLLMProfile `json:"profiles,omitempty"`
}

type dianaLLMProfile struct {
	ID                  string        `json:"id,omitempty"`
	Name                string        `json:"name,omitempty"`
	Group               string        `json:"group,omitempty"`
	Description         string        `json:"description,omitempty"`
	Provider            llm.Provider  `json:"provider,omitempty"`
	APIKeyConfigured    bool          `json:"api_key_configured"`
	BaseURL             string        `json:"base_url,omitempty"`
	APIFormat           llm.APIFormat `json:"api_format,omitempty"`
	Model               string        `json:"model,omitempty"`
	ImageModel          string        `json:"image_model,omitempty"`
	ImageBaseURL        string        `json:"image_base_url,omitempty"`
	ImageOrigin         string        `json:"image_origin,omitempty"`
	ImageTimeoutMS      int64         `json:"image_timeout_ms,omitempty"`
	UserAgent           string        `json:"user_agent,omitempty"`
	HeaderNames         []string      `json:"header_names,omitempty"`
	Temperature         *float64      `json:"temperature,omitempty"`
	ReasoningEffort     string        `json:"reasoning_effort,omitempty"`
	ContextWindowTokens int64         `json:"context_window_tokens,omitempty"`
	MaxContextTokens    int64         `json:"max_context_tokens,omitempty"`
	MaxOutputTokens     int64         `json:"max_output_tokens,omitempty"`
	TimeoutMS           int64         `json:"timeout_ms,omitempty"`
	UpdatedAt           time.Time     `json:"updated_at,omitempty"`
}

type dianaPluginSkillState struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Description string   `json:"description,omitempty"`
	Official    bool     `json:"official"`
	BuiltIn     bool     `json:"built_in"`
	Installed   bool     `json:"installed"`
	Enabled     bool     `json:"enabled"`
	Permissions []string `json:"permissions,omitempty"`
}

type dianaRuntimePathSnapshot struct {
	AppDBPath       string `json:"app_db_path,omitempty"`
	LogPath         string `json:"log_path,omitempty"`
	FrontendDist    string `json:"frontend_dist,omitempty"`
	AgentWorkDir    string `json:"agent_work_dir,omitempty"`
	AgentSkillRoots string `json:"agent_skill_roots,omitempty"`
	AgentMCPConfig  string `json:"agent_mcp_config,omitempty"`
}

// newDianaConfigTool exposes the bot's own redacted runtime configuration to Agent skills.
func newDianaConfigTool(runtime *Runtime) *dianaConfigTool {
	return &dianaConfigTool{runtime: runtime}
}

func (t *dianaConfigTool) Name() string {
	return "diana.config"
}

func (t *dianaConfigTool) Description() string {
	return `读取 Diana 自己的脱敏运行配置、当前 LLM profile、已安装 skills 与插件状态和运行路径。`
}

func (t *dianaConfigTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"section": toolEnumParam("要读取的部分，省略等同 all。",
			"all", "bot", "llm", "skills", "runtime", "paths"),
	})
}

func (t *dianaConfigTool) Run(_ context.Context, input map[string]any) (string, error) {
	section := strings.ToLower(strings.TrimSpace(configToolString(input, "section")))
	if section == "" {
		section = "all"
	}
	snapshot := t.runtime.dianaConfigSnapshot()
	filtered := map[string]any{"note": snapshot.Note}
	switch section {
	case "all":
		filtered["runtime"] = snapshot.Runtime
		filtered["bot"] = snapshot.Bot
		filtered["llm"] = snapshot.LLM
		filtered["installed_skills"] = snapshot.Skills
		filtered["runtime_paths"] = snapshot.RuntimePath
	case "bot":
		filtered["bot"] = snapshot.Bot
	case "llm":
		filtered["llm"] = snapshot.LLM
	case "skills", "plugins":
		filtered["installed_skills"] = snapshot.Skills
	case "runtime":
		filtered["runtime"] = snapshot.Runtime
	case "paths":
		filtered["runtime_paths"] = snapshot.RuntimePath
	default:
		filtered["runtime"] = snapshot.Runtime
		filtered["bot"] = snapshot.Bot
		filtered["llm"] = snapshot.LLM
		filtered["installed_skills"] = snapshot.Skills
		filtered["runtime_paths"] = snapshot.RuntimePath
	}
	body, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (r *Runtime) dianaConfigSnapshot() dianaConfigSnapshot {
	status := r.Status()
	cfg := r.Config().WithDefaults()
	return dianaConfigSnapshot{
		Note:        "配置已脱敏：不会返回 API key、OneBot token、自定义 header 值、config.yaml 原文或 secrets 文件内容。",
		Runtime:     dianaRuntimeFromStatus(status),
		Bot:         dianaBotConfigFromConfig(cfg),
		LLM:         r.dianaLLMSnapshot(),
		Skills:      dianaPluginSkillsFromStates(status.Plugins),
		RuntimePath: dianaRuntimePathsFromEnv(),
	}
}

func dianaRuntimeFromStatus(status RuntimeStatus) dianaRuntimeSnapshot {
	return dianaRuntimeSnapshot{
		Running:       status.Running,
		Channel:       status.Channel,
		NoneBotBridge: status.NoneBotBridge,
		ActiveWorkers: status.ActiveWorkers,
		LastError:     status.LastError,
		UpdatedAt:     status.UpdatedAt,
	}
}

func dianaBotConfigFromConfig(cfg BotConfig) dianaBotConfigSnapshot {
	chatIn := cfg.chatInSettings()
	return dianaBotConfigSnapshot{
		ID:                              cfg.ID,
		Name:                            cfg.Name,
		Platform:                        cfg.Platform,
		AvatarURL:                       cfg.AvatarURL,
		Enabled:                         cfg.Enabled,
		OneBotReverseWSEndpoint:         cfg.OneBotReverseWSEndpoint,
		OneBotAccessTokenConfigured:     strings.TrimSpace(cfg.OneBotAccessToken) != "",
		NoneBotBridgeEnabled:            cfg.NoneBotBridgeEnabled,
		NoneBotBridgeEndpoint:           cfg.NoneBotBridgeEndpoint,
		NoneBotBridgeTokenConfigured:    strings.TrimSpace(cfg.NoneBotBridgeToken) != "",
		BotAccount:                      cfg.BotAccount,
		OwnerID:                         cfg.OwnerID,
		OwnerLoginEnabled:               cfg.OwnerLoginEnabled,
		GroupTriggers:                   append([]string(nil), cfg.GroupTriggers...),
		GroupTriggerMode:                aliasTriggerMode(cfg),
		DisabledGroups:                  append([]string(nil), cfg.DisabledGroups...),
		DisabledUsers:                   append([]string(nil), cfg.DisabledUsers...),
		GroupAdmission:                  cfg.GroupAdmission.WithDefaults(),
		ReplyGate:                       cfg.ReplyGate.Clone(),
		WelcomeEnabled:                  cfg.WelcomeEnabled,
		WelcomeMessage:                  cfg.WelcomeMessage,
		SystemPromptConfigured:          strings.TrimSpace(cfg.SystemPrompt) != "",
		SystemPromptChars:               len([]rune(cfg.SystemPrompt)),
		ReplyReferenceMode:              replyReferenceMode(cfg),
		MentionUserMode:                 mentionUserMode(cfg),
		MarkdownToPlain:                 boolValue(cfg.MarkdownToPlain, true),
		ErrorNotifyEnabled:              boolValue(cfg.ErrorNotifyEnabled, true),
		ErrorReplyPrefix:                cfg.ErrorReplyPrefix,
		SendRetryAttempts:               cfg.SendRetryAttempts,
		SendChunkIntervalMS:             cfg.SendChunkIntervalMS,
		PromptInjectTime:                boolValue(cfg.PromptInjectTime, true),
		PromptInjectPlaintextRules:      boolValue(cfg.PromptInjectPlaintextRules, true),
		PromptInjectGroupSender:         boolValue(cfg.PromptInjectGroupSender, true),
		PromptChineseSlangHint:          boolValue(cfg.PromptChineseSlangHint, true),
		PromptChineseSlangChars:         len([]rune(cfg.PromptChineseSlangText)),
		PromptPlaintextRulesChars:       len([]rune(cfg.PromptPlaintextRulesText)),
		PromptTimeTemplateConfigured:    strings.TrimSpace(cfg.PromptTimeTemplate) != "",
		PromptGroupSenderConfigured:     strings.TrimSpace(cfg.PromptGroupSenderTemplate) != "",
		PromptImageOnlyConfigured:       strings.TrimSpace(cfg.PromptImageOnlyText) != "",
		PromptWakeOnlyConfigured:        strings.TrimSpace(cfg.PromptWakeOnlyText) != "",
		ModelRoles:                      normalizeModelRoles(cfg.ModelRoles),
		BotReplyLoopDetectionEnabled:    boolValue(cfg.BotReplyLoopDetectionEnabled, true),
		ProactiveReplyRouterPromptChars: len([]rune(cfg.ProactiveReplyRouterPrompt)),
		ProactiveReplyPromptChars:       len([]rune(cfg.ProactiveReplyPrompt)),
		MaxInputChars:                   cfg.MaxInputChars,
		MaxReplyChars:                   cfg.MaxReplyChars,
		DirectReplyChunkSize:            cfg.DirectReplyChunkSize,
		ForwardReplyThreshold:           cfg.ForwardReplyThreshold,
		RecallReplyMode:                 cfg.RecallReplyMode,
		RecallReplyAutoDeleteEnabled:    boolValue(cfg.RecallReplyAutoDeleteEnabled, false),
		RecallReplyTTLSeconds:           cfg.RecallReplyTTLSeconds,
		OwnerLLMConfigEnabled:           boolValue(cfg.OwnerLLMConfigEnabled, true),
		LLMIdentityMaskingEnabled:       llmIdentityMaskingEnabled(cfg),
		RecentContextLimit:              cfg.RecentContextLimit,
		ContextSummaryThreshold:         cfg.ContextSummaryThreshold,
		LongTermMemoryEnabled:           boolValue(cfg.LongTermMemoryEnabled, true),
		CrossGroupMemoryEnabled:         boolValue(cfg.CrossGroupMemoryEnabled, false),
		DictSegmentEnabled:              boolValue(cfg.DictSegmentEnabled, false),
		SemanticSearchEnabled:           boolValue(cfg.SemanticSearchEnabled, false),
		ProactiveReplyChance:            cfg.ProactiveReplyChance,
		ProactiveReplyThreshold:         cfg.ProactiveReplyThreshold,
		ChatInEnabled:                   chatIn.Enabled,
		ChatInLevel:                     string(chatIn.Level),
		ChatInThreshold:                 chatIn.Threshold,
		ChatInChance:                    chatIn.Chance,
		ChatInCooldownSeconds:           int(chatIn.Cooldown / time.Second),
		NaturalInterjectionEnabled:      chatIn.Natural,
		ReplyRules:                      append([]ReplyRule(nil), cfg.ReplyRules...),
		MaxBotConcurrency:               cfg.MaxBotConcurrency,
		RequestTimeoutMS:                cfg.RequestTimeout.Milliseconds(),
		Agent: dianaAgentConfigSnapshot{
			Enabled:          cfg.AgentEnabled,
			WorkDir:          AgentWorkspaceDir(),
			MaxSteps:         cfg.AgentMaxSteps,
			SkillRoots:       append([]string(nil), cfg.AgentSkillRoots...),
			MCPConfigPath:    cfg.AgentMCPConfigPath,
			CommandAllowlist: append([]string(nil), cfg.AgentCommandAllowlist...),
			CommandTimeoutMS: cfg.AgentCommandTimeoutMS,
			BrowserCDPURL:    cfg.AgentBrowserCDPURL,
			BrowserTimeoutMS: cfg.AgentBrowserTimeoutMS,
		},
	}
}

func (r *Runtime) dianaLLMSnapshot() dianaLLMSnapshot {
	if r.llmStore == nil {
		return dianaLLMSnapshot{}
	}
	set := r.llmStore.Profiles().WithDefaults()
	out := dianaLLMSnapshot{Profiles: make([]dianaLLMProfile, 0, len(set.Profiles))}
	for _, profile := range set.Profiles {
		cfg := profile.Config.WithDefaults()
		headers := cfg.NormalizedHeaders()
		headerNames := make([]string, 0, len(headers))
		for name := range headers {
			headerNames = append(headerNames, name)
		}
		sort.Strings(headerNames)
		out.Profiles = append(out.Profiles, dianaLLMProfile{
			ID:               profile.ID,
			Name:             profile.Name,
			Group:            llm.NormalizeProfileGroup(profile.Group),
			Description:      profile.Description,
			Provider:         cfg.Provider,
			APIKeyConfigured: strings.TrimSpace(cfg.APIKey) != "",
			BaseURL:          cfg.BaseURL,
			APIFormat:        cfg.APIFormatWithDefault(),
			Model:            cfg.Model,
			ImageModel:       cfg.ImageModel,
			ImageBaseURL:     cfg.ImageBaseURL,
			ImageOrigin:      cfg.ImageOrigin,
			ImageTimeoutMS:   cfg.ImageTimeout.Milliseconds(),
			UserAgent:        cfg.UserAgent,
			HeaderNames:      headerNames,
			Temperature:      cfg.Temperature,
			ReasoningEffort:  cfg.ReasoningEffort,
			// 报当前模型实际生效的窗口，而不是配置里存没存过一个数。
			ContextWindowTokens: cfg.ContextWindowTokensWithDefault(),
			MaxContextTokens:    cfg.MaxContextTokensWithDefault(),
			MaxOutputTokens:     cfg.MaxOutputTokens,
			TimeoutMS:           cfg.Timeout.Milliseconds(),
			UpdatedAt:           profile.UpdatedAt,
		})
	}
	return out
}

func dianaPluginSkillsFromStates(states []PluginState) []dianaPluginSkillState {
	out := make([]dianaPluginSkillState, 0, len(states))
	for _, state := range states {
		out = append(out, dianaPluginSkillState{
			ID:          state.Manifest.ID,
			Name:        state.Manifest.Name,
			Version:     state.Manifest.Version,
			Description: state.Manifest.Description,
			Official:    state.Manifest.Official,
			BuiltIn:     state.Manifest.BuiltIn,
			Installed:   state.Installed,
			Enabled:     state.Enabled,
			Permissions: append([]string(nil), state.Manifest.Permissions...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func dianaRuntimePathsFromEnv() dianaRuntimePathSnapshot {
	return dianaRuntimePathSnapshot{
		AppDBPath:       os.Getenv("APP_DB_PATH"),
		LogPath:         os.Getenv("LOG_PATH"),
		FrontendDist:    os.Getenv("FRONTEND_DIST"),
		AgentWorkDir:    AgentWorkspaceDir(),
		AgentSkillRoots: os.Getenv("DIANA_AGENT_SKILL_ROOTS"),
		AgentMCPConfig:  os.Getenv("DIANA_AGENT_MCP_CONFIG"),
	}
}

func configToolString(input map[string]any, key string) string {
	if input == nil {
		return ""
	}
	value, ok := input[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func configToolStringSlice(input map[string]any, key string) []string {
	if input == nil {
		return nil
	}
	value, ok := input[key]
	if !ok {
		return nil
	}
	var out []string
	switch typed := value.(type) {
	case []string:
		out = append(out, typed...)
	case []any:
		for _, item := range typed {
			if text, isText := item.(string); isText {
				out = append(out, text)
			}
		}
	case string:
		out = append(out, typed)
	}
	var cleaned []string
	for _, item := range out {
		if item = strings.TrimSpace(item); item != "" {
			cleaned = append(cleaned, item)
		}
	}
	return cleaned
}
