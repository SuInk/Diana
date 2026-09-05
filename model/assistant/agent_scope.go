// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
)

const agentScopeContextRadius = 1

type agentReplyScope struct {
	Routed                bool
	ToolNames             []string
	ContextMessageIDs     []string
	KeepContextSummary    bool
	KeepContextSummarySet bool
}

func (r *Runtime) newAgentRegistry(ctx context.Context, cfg BotConfig, event MessageEvent, relationship RelationshipPolicy, extraTools ...agent.Tool) (*agent.ToolRegistry, error) {
	agentCfg := r.agentRegistryConfig(cfg, event, relationship.Owner)
	var registry *agent.ToolRegistry
	var err error
	if relationship.Owner {
		base, baseErr := r.sharedAgentRegistry(ctx, agentCfg)
		if baseErr != nil {
			return nil, baseErr
		}
		registry, err = base.NewView(agentCfg)
	} else {
		registry, err = agent.NewDefaultToolRegistry(agentCfg)
	}
	if err != nil {
		return nil, err
	}
	if relationship.Owner {
		registry.Register(newDianaConfigTool(r))
	}
	for _, tool := range extraTools {
		registry.Register(tool)
	}
	registry.Retain(r.allowedAgentToolNamesForEvent(event, relationship))
	return registry, nil
}

func (r *Runtime) allowedAgentToolNamesForEvent(event MessageEvent, relationship RelationshipPolicy) map[string]bool {
	allowed := relationship.allowedAgentToolNames()
	if allowed == nil || r == nil || r.plugins == nil {
		return allowed
	}
	_, settings, enabled := r.pluginWithSettingsForEvent(repositoryPublishPluginID, event)
	if enabled && repositoryPublishEventHasAccess(event, settings) {
		allowed[dianaRepositoryIssuesToolName] = true
	}
	// 管的是仓库订阅，不往仓库里写，所以只认管理人员名单，不看 Issue 写入白名单。
	if enabled && len(repositoryWatchManagedRepositories(event, settings)) > 0 {
		allowed[dianaRepositoryWatchToolName] = true
	}
	return allowed
}

func (r *Runtime) agentRegistryConfig(cfg BotConfig, event MessageEvent, extensionManagement bool) agent.Config {
	return agent.Config{
		WorkDir:             AgentWorkspaceDir(),
		MaxSteps:            cfg.AgentMaxSteps,
		SkillRoots:          cfg.AgentSkillRoots,
		MCPConfigPath:       cfg.AgentMCPConfigPath,
		ExtensionManagement: extensionManagement,
		BuiltinExtensions:   r.agentBuiltinExtensions(event),
		BuiltinSkills:       r.oneBotV11BuiltinSkills(event),
		ReservedSkillNames:  []string{"onebot-v11"},
		CommandAllowlist:    cfg.AgentCommandAllowlist,
		CommandTimeoutMS:    cfg.AgentCommandTimeoutMS,
		// 这两项以前在 agent.Config 里存在但没人赋值，于是永远是 auto，
		// require 模式接不上。现在由机器人配置说了算。
		CommandSandbox:             cfg.AgentCommandSandbox,
		CommandSandboxAllowNetwork: cfg.AgentCommandSandboxAllowNetwork,
		FileWriteEnabled:           cfg.AgentFileWriteEnabled,
		BrowserCDPURL:              cfg.AgentBrowserCDPURL,
		BrowserTimeoutMS:           cfg.AgentBrowserTimeoutMS,
	}
}

func (r *Runtime) sharedAgentRegistry(ctx context.Context, cfg agent.Config) (*agent.ToolRegistry, error) {
	cfg = cfg.WithDefaults()
	// Built-in plugin state can vary per group override, but it does not change
	// the underlying Skills/MCP processes. Request views overlay that state.
	baseCfg := cfg
	baseCfg.BuiltinExtensions = nil
	// 证据账本开关按群覆盖，只影响 Runner 的校验行为，不改变工具集合；
	// 参与缓存键会把同一套 Skills/MCP 进程按开关拆成两份。
	baseCfg.EvidenceLedgerAdvisory = false
	keyBody, err := json.Marshal(baseCfg)
	if err != nil {
		return nil, err
	}
	key := string(keyBody)
	r.agentRegistryMu.Lock()
	defer r.agentRegistryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.agentRegistryCache == nil {
		r.agentRegistryCache = map[string]*agent.ToolRegistry{}
	}
	if registry := r.agentRegistryCache[key]; registry != nil {
		return registry, nil
	}
	r.mu.RLock()
	lifecycleCtx := r.runCtx
	r.mu.RUnlock()
	if lifecycleCtx == nil {
		lifecycleCtx = context.WithoutCancel(ctx)
	}
	registry, err := agent.NewAgentToolRegistry(lifecycleCtx, baseCfg)
	if err != nil {
		return nil, err
	}
	r.agentRegistryCache[key] = registry
	return registry, nil
}

// logCommandExecutionPosture 在启动时把「命令执行这条路现在是什么状态」写进日志。
//
// 这两件事以前都不可见，而它们决定了一台机器上命令执行的全部风险：
// 白名单为空时 run_command 根本不注册（于是「让机器人执行指令」表现为静默无反应），
// 沙盒不可用时命令直接以主进程身份裸跑（而白名单只管「跑哪个程序」，不管它能碰什么）。
// 部署方有权在启动时就知道自己处在哪一种。
func logCommandExecutionPosture(configs []BotConfig) {
	logged := map[string]bool{}
	for _, cfg := range configs {
		cfg = cfg.WithDefaults()
		if !cfg.Enabled || !cfg.AgentEnabled {
			continue
		}
		if len(cfg.AgentCommandAllowlist) == 0 {
			log.Printf("diana agent: 配置 %q 未设置命令白名单，run_command 不会注册（机器人无法执行任何本地命令）", cfg.ID)
			continue
		}
		status := agent.DescribeCommandSandbox(cfg.AgentCommandSandbox)
		key := status.Mode + "\x00" + status.Effective() + "\x00" + status.Reason
		switch status.Effective() {
		case "sandboxed":
			log.Printf("diana agent: 配置 %q 的命令执行已沙盒化（%s，网络 %s）", cfg.ID, status.Kind, allowedOrBlocked(cfg.AgentCommandSandboxAllowNetwork))
		case "blocked":
			log.Printf("diana agent: 配置 %q 要求沙盒但本机不可用，命令执行会被拒绝：%s", cfg.ID, status.Reason)
		default:
			if logged[key] {
				continue
			}
			logged[key] = true
			log.Printf("diana agent: 命令执行未被沙盒隔离，将以本进程权限直接运行（%s）。白名单只限制能跑哪个程序，不限制它能读写什么；生产环境建议安装 bubblewrap 并把沙盒模式设为 require", status.Reason)
		}
	}
}

func allowedOrBlocked(allowed bool) string {
	if allowed {
		return "放行"
	}
	return "切断"
}

func (r *Runtime) prewarmAgentRegistries(ctx context.Context, configs []BotConfig) {
	logCommandExecutionPosture(configs)
	for _, cfg := range configs {
		cfg = cfg.WithDefaults()
		if !cfg.Enabled || !cfg.AgentEnabled || strings.TrimSpace(cfg.OwnerID) == "" {
			continue
		}
		event := MessageEvent{Kind: EventKindPrivate, ProfileID: cfg.ID, UserID: cfg.OwnerID}
		if _, err := r.sharedAgentRegistry(ctx, r.agentRegistryConfig(cfg, event, true)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("diana agent extension prewarm failed for profile %q: %v", cfg.ID, err)
		}
	}
}

func (r *Runtime) closeAgentRegistryCache() {
	r.agentRegistryMu.Lock()
	registries := make([]*agent.ToolRegistry, 0, len(r.agentRegistryCache))
	for _, registry := range r.agentRegistryCache {
		registries = append(registries, registry)
	}
	r.agentRegistryCache = map[string]*agent.ToolRegistry{}
	r.agentRegistryMu.Unlock()
	for _, registry := range registries {
		_ = registry.Close()
	}
}

func (r *Runtime) agentBuiltinExtensions(event MessageEvent) []agent.BuiltinExtension {
	if r == nil || r.plugins == nil {
		return nil
	}
	overrides := r.pluginOverridesForEvent(event)
	states := r.plugins.List()
	extensions := make([]agent.BuiltinExtension, 0, len(states))
	for _, state := range states {
		extensions = append(extensions, agent.BuiltinExtension{
			ID:          state.Manifest.ID,
			Name:        state.Manifest.Name,
			Version:     state.Manifest.Version,
			Description: state.Manifest.Description,
			Official:    state.Manifest.Official,
			BuiltIn:     state.Manifest.BuiltIn,
			Installed:   state.Installed,
			Enabled:     r.plugins.EnabledWithOverrides(state.Manifest.ID, overrides),
			Permissions: append([]string(nil), state.Manifest.Permissions...),
		})
	}
	return extensions
}

func (scope agentReplyScope) toolSet() map[string]bool {
	if !scope.Routed {
		return nil
	}
	selected := make(map[string]bool, len(scope.ToolNames))
	for _, name := range scope.ToolNames {
		if name = strings.TrimSpace(name); name != "" {
			selected[name] = true
		}
	}
	return selected
}

func withoutAgentTool(names []string, excluded string) []string {
	excluded = strings.TrimSpace(excluded)
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) != excluded {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

func filterAgentReplyHistory(history []MessageEvent, event MessageEvent, scope agentReplyScope) []MessageEvent {
	if !scope.Routed {
		return history
	}
	wanted := map[string]bool{}
	add := func(messageID string) {
		if messageID = strings.TrimSpace(messageID); messageID != "" {
			wanted[messageID] = true
		}
	}
	for _, messageID := range scope.ContextMessageIDs {
		add(messageID)
	}
	for _, messageID := range eventSemanticSourceMessageIDs(event) {
		add(messageID)
	}
	for _, messageID := range replyReferenceIDs(event.Segments) {
		add(messageID)
	}
	if event.Quoted != nil {
		add(event.Quoted.MessageID)
		for _, messageID := range quotedSemanticSourceMessageIDs(event.Quoted) {
			add(messageID)
		}
		for _, messageID := range replyReferenceIDs(event.Quoted.Segments) {
			add(messageID)
		}
	}
	if len(wanted) == 0 {
		return nil
	}

	include := map[int]bool{}
	for index, item := range history {
		if !wanted[strings.TrimSpace(item.MessageID)] {
			continue
		}
		left := index - agentScopeContextRadius
		if left < 0 {
			left = 0
		}
		right := index + agentScopeContextRadius
		if right >= len(history) {
			right = len(history) - 1
		}
		for nearby := left; nearby <= right; nearby++ {
			include[nearby] = true
		}
	}
	filtered := make([]MessageEvent, 0, len(include))
	for index, item := range history {
		if include[index] {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (r *Runtime) recordAgentScope(ctx context.Context, event MessageEvent, scope agentReplyScope, toolsBefore, contextBefore, contextAfter int) {
	writer := r.appLogWriter()
	if writer == nil || !scope.Routed {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "diana.planner",
		Message: "planner 已完成回复判断，工具和上下文建议仅供 Agent 参考",
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id":           event.GroupID,
			"user_id":            event.UserID,
			"selected_tools":     append([]string(nil), scope.ToolNames...),
			"tools_before":       toolsBefore,
			"tools_after":        toolsBefore,
			"context_before":     contextBefore,
			"context_after":      contextAfter,
			"keep_older_summary": scope.KeepContextSummary,
		},
	})
}
