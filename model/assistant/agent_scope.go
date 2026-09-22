// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
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
	overrides, err := agent.LoadExtensionOverrides(AgentWorkspaceDir(), event.ProfileID)
	if err != nil {
		return nil, err
	}
	audiences, err := agent.LoadExtensionAudiences(AgentWorkspaceDir(), event.ProfileID)
	if err != nil {
		return nil, err
	}
	// 本群单独设过的扩展走群里的说法，其余跟随机器人。
	groupAccess := r.groupExtensionAccessForEvent(event)
	access := extensionAccessInput{overrides: overrides, audiences: audiences, groupAccess: groupAccess, userID: event.UserID, groupID: event.GroupID}
	// 起不起共享底座只看「有没有可能对非主人开」，这一步不碰扩展目录，也就不会
	// 为一句群闲聊把 MCP 进程拉起来。
	mayOpen := relationship.Owner || len(agent.MemberAllowedExtensionIDs(overrides)) > 0 || groupOpensExtensions(groupAccess)
	base, err := r.agentExtensionBase(ctx, agentCfg, mayOpen)
	if err != nil {
		return nil, err
	}
	var registry *agent.ToolRegistry
	if base != nil {
		registry, err = base.NewView(agentCfg)
		if err != nil && relationship.Owner {
			return nil, err
		}
	}
	if registry == nil {
		base = nil
		registry, err = agent.NewDefaultToolRegistry(agentCfg)
		if err != nil {
			return nil, err
		}
	}
	if relationship.Owner {
		registry.Register(newDianaConfigTool(r, event))
		registry.Register(&dianaUsageTool{runtime: r, event: event})
		registry.Register(&dianaBotMarkersTool{runtime: r, event: event})
		registry.Register(newDianaExtensionAccessTool(r, event))
	}
	// 所有人都要能用：它的用途就是核实「我是主人」这类声称，只给主人用等于没用。
	// 它只读运行时判定、不改任何状态，对非主人开放没有额外风险。
	registry.Register(&dianaIdentityCheckTool{runtime: r, event: event})
	for _, tool := range extraTools {
		registry.Register(tool)
	}
	allowed := r.allowedAgentToolNamesForEvent(event, relationship)
	memberExtensions := []string{}
	if allowed != nil && base != nil {
		// 非主人能用哪些扩展，按「停用 > 黑名单 > 白名单 > 档位」逐项算出来。
		var pending []string
		memberExtensions, pending = resolveMemberExtensions(extensionIDsOf(base), access)
		if len(pending) > 0 {
			role := r.senderGroupRole(ctx, event)
			if role == agent.MemberRoleAdmin || role == "owner" {
				memberExtensions = append(memberExtensions, pending...)
			}
		}
		sort.Strings(memberExtensions)
		for _, name := range memberMCPToolNames(base, memberExtensions) {
			allowed[name] = true
		}
	}
	if !relationship.Owner {
		// 群成员的 skill 面固定成「内置协议 + 放行的那几份」：视图挂在共享底座下
		// 之后，不显式设置就会把底座上的自定义 Skill 全部继承过来。
		registry.RegisterScopedSkills(agentCfg.BuiltinSkills, memberSkills(base, memberExtensions), agentCfg.ReservedSkillNames)
	}
	registry.Retain(allowed)
	// 一次性交给注册表：ApplyExtensionOverrides 是整份替换，分两次调用后一次会
	// 把前一次的机器人级停用覆盖掉。
	registry.ApplyExtensionOverrides(mergeExtensionOverrides(overrides, groupExtensionOverrides(groupAccess)))
	return registry, nil
}

// agentCoreTools 按这台机器人配的档位算出本轮的常驻工具名单。没配过档位就是内置
// 默认名单；读不到覆盖文件时同样退回默认，不因为配置读失败就把工具列表抖一遍。
func (r *Runtime) agentCoreTools(event MessageEvent, registry *agent.ToolRegistry) []string {
	overrides, err := agent.LoadExtensionOverrides(AgentWorkspaceDir(), event.ProfileID)
	if err != nil || len(overrides) == 0 {
		return replyAgentCoreTools
	}
	return agent.ResolveCoreTools(replyAgentCoreTools, registry.ToolOwners(), overrides)
}

// groupExtensionAccessForEvent 取本群对扩展档位的覆盖，私聊没有群配置。
func (r *Runtime) groupExtensionAccessForEvent(event MessageEvent) map[string]GroupExtensionAccess {
	if event.Kind != EventKindGroup {
		return nil
	}
	groupCfg, ok := r.groupConfigForEvent(event)
	if !ok || len(groupCfg.ExtensionAccess) == 0 {
		return nil
	}
	access := make(map[string]GroupExtensionAccess, len(groupCfg.ExtensionAccess))
	for id, item := range groupCfg.ExtensionAccess {
		id = strings.TrimSpace(id)
		tier, err := agent.NormalizeExtensionTier(item.Tier)
		if err != nil || id == "" {
			continue
		}
		item.Tier = tier
		// 跟随档不带本群名单：界面上「跟随」就是这个群不干预，存储侧也按这条收。
		if tier == "" || item.Empty() {
			continue
		}
		access[id] = item
	}
	return access
}

// extensionAccessInput 是算「这个人在这个群能用哪些扩展」要用到的全部输入。
type extensionAccessInput struct {
	overrides   map[string]bool
	audiences   map[string]agent.ExtensionAudience
	groupAccess map[string]GroupExtensionAccess
	userID      string
	groupID     string
}

// resolveMemberExtensions 逐项判断非主人能不能用，顺序是「停用 > 黑名单 > 白名单 > 档位」。
// 第二个返回值是还要核验群身份才能定的项：核验可能要访问平台接口，能不查就不查。
func resolveMemberExtensions(candidates []string, in extensionAccessInput) (allowed, needsRole []string) {
	for _, id := range candidates {
		group := in.groupAccess[id]
		tier := group.Tier
		if tier == "" {
			tier = agent.BotExtensionTier(in.overrides, in.audiences, id)
		}
		// 停用是「这里没有这个能力」，白名单也放不出来。本群设过档位时只看本群这一档：
		// 机器人级停用是默认值，不该再回头否决群里的决定。
		if tier == agent.ExtensionTierOff {
			continue
		}
		if group.Denied(in.userID) {
			continue
		}
		if group.Allowed(in.userID) {
			allowed = append(allowed, id)
			continue
		}
		// 机器人那份对象名单继续管用，本群白名单才是它的例外。
		if !in.audiences[id].Allows(in.userID, in.groupID) {
			continue
		}
		switch tier {
		case agent.ExtensionTierMembers:
			allowed = append(allowed, id)
		case agent.ExtensionTierAdmins:
			needsRole = append(needsRole, id)
		}
	}
	return allowed, needsRole
}

// groupOpensExtensions 判断本群配置里有没有「可能放开给非主人」的条目。
func groupOpensExtensions(access map[string]GroupExtensionAccess) bool {
	for _, item := range access {
		if len(item.Allow) > 0 || item.Tier == agent.ExtensionTierAdmins || item.Tier == agent.ExtensionTierMembers {
			return true
		}
	}
	return false
}

// extensionIDsOf 列出底座上已装的 MCP 和 Skill。
func extensionIDsOf(base *agent.ToolRegistry) []string {
	if base == nil {
		return nil
	}
	ids := []string{}
	for _, state := range base.Extensions() {
		if state.Kind == agent.ExtensionKindMCP || state.Kind == agent.ExtensionKindSkill {
			ids = append(ids, state.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// mergeExtensionOverrides 合并机器人级和群级开关，群里设过的优先。
func mergeExtensionOverrides(botLevel, groupLevel map[string]bool) map[string]bool {
	if len(groupLevel) == 0 {
		return botLevel
	}
	merged := make(map[string]bool, len(botLevel)+len(groupLevel))
	for id, enabled := range botLevel {
		merged[id] = enabled
	}
	for id, enabled := range groupLevel {
		merged[id] = enabled
	}
	return merged
}

// groupExtensionOverrides 把本群的档位表达成注册表认识的启停开关。
//
// 机器人那一份是默认档，群级设过就直接盖上去——两个方向都盖。以前这里只翻译「停用」，
// 于是群管理页开了也没用：机器人级停用会在合并后继续赢，用户开完还被告知没启用，群级
// 那个开关等于摆设。跟随档（空档位）在读取时就被滤掉，不会进到这里。
func groupExtensionOverrides(access map[string]GroupExtensionAccess) map[string]bool {
	if len(access) == 0 {
		return nil
	}
	values := map[string]bool{}
	for id, item := range access {
		if item.Tier == "" {
			continue
		}
		values[id] = item.Tier != agent.ExtensionTierOff
	}
	return values
}

func sortedExtensionIDs(values map[string]bool) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// agentExtensionBase 取共享扩展底座。主人会话和「有 MCP 放给群成员」时按需拉起；
// 其余群消息只借用已经起来的底座，不为一句闲聊新起一堆 MCP 进程——借到了也有用：
// 白名单外的工具名这时能被认出来是没权限，而不是查无此工具。
func (r *Runtime) agentExtensionBase(ctx context.Context, agentCfg agent.Config, start bool) (*agent.ToolRegistry, error) {
	// 底座按 ExtensionScope 共享，而 ExtensionManagement 在里面。群成员视图关掉了
	// 扩展管理，取底座时仍按主人那份取，否则同一套 MCP 会被拉起第二份。
	baseCfg := agentCfg
	baseCfg.ExtensionManagement = true
	if start {
		return r.sharedAgentRegistry(ctx, baseCfg)
	}
	return r.cachedAgentRegistry(baseCfg), nil
}

// memberMCPToolNames 展开放给群成员的 MCP 服务当前发现到的工具名。
func memberMCPToolNames(base *agent.ToolRegistry, extensionIDs []string) []string {
	if base == nil || len(extensionIDs) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(extensionIDs))
	for _, id := range extensionIDs {
		allowed[id] = true
	}
	names := []string{}
	for _, state := range base.Extensions() {
		if state.Kind != agent.ExtensionKindMCP || !state.Enabled || !allowed[state.ID] {
			continue
		}
		names = append(names, state.Tools...)
	}
	return names
}

// senderGroupRole 核验发言人在本群的身份，只在确实有扩展设了群管门槛时才调用。
// 事件自带身份就不访问平台；平台给不出身份时返回空串，按普通成员处理。
// 返回的是 agent 包认的取值（owner/admin/member），不是本包的 group_ 前缀常量。
func (r *Runtime) senderGroupRole(ctx context.Context, event MessageEvent) string {
	if event.Kind != EventKindGroup {
		return ""
	}
	role := NormalizeGroupRole(event.SenderRole)
	if role == "" {
		member, err := r.getGroupMemberInfoForEvent(ctx, event, event.GroupID, event.UserID)
		if err != nil {
			log.Printf("diana agent: 无法核验 %s 在群 %s 的身份，按普通成员处理: %v", event.UserID, event.GroupID, err)
			return ""
		}
		role = NormalizeGroupRole(member.Role)
	}
	switch role {
	case GroupRoleOwner:
		return "owner"
	case GroupRoleAdmin:
		return agent.MemberRoleAdmin
	case GroupRoleMember:
		return "member"
	}
	return ""
}

// memberSkills 挑出放给群成员的 skill。正文之外的脚本资源不跟着开放：成员没有
// run_command 和 read_file，skill 里让跑脚本的段落在成员会话里执行不了。
func memberSkills(base *agent.ToolRegistry, extensionIDs []string) []agent.SkillMetadata {
	if base == nil || len(extensionIDs) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(extensionIDs))
	for _, id := range extensionIDs {
		allowed[id] = true
	}
	skills := []agent.SkillMetadata{}
	for _, skill := range base.Skills() {
		if allowed["skill:"+skill.Name] {
			skills = append(skills, skill)
		}
	}
	return skills
}

func (r *Runtime) allowedAgentToolNamesForEvent(event MessageEvent, relationship RelationshipPolicy) map[string]bool {
	allowed := relationship.allowedAgentToolNames()
	if allowed == nil || r == nil || r.plugins == nil {
		return allowed
	}
	_, settings, enabled := r.pluginWithSettingsForEvent(repositoryPublishPluginID, event)
	if enabled && repositoryPublishEventHasAccess(event, settings) {
		allowed[dianaGitHubToolName] = true
	}
	// 管的是仓库订阅，不往仓库里写，所以只认管理人员名单，不看 Issue 写入白名单。
	if enabled && len(repositoryWatchManagedRepositories(event, settings)) > 0 {
		allowed[dianaRepositoryWatchToolName] = true
	}
	return allowed
}

func (r *Runtime) agentRegistryConfig(cfg BotConfig, event MessageEvent, extensionManagement bool) agent.Config {
	// Skills 目录和 MCP 配置路径由 GlobalExtensionPaths 在首次使用时固定下来，
	// 机器人之间不会因为各自填得不同而切到另一套扩展。
	return agent.Config{
		WorkDir:             AgentWorkspaceDir(),
		MaxSteps:            cfg.AgentMaxSteps,
		SkillRoots:          cfg.AgentSkillRoots,
		MCPConfigPath:       cfg.AgentMCPConfigPath,
		ExtensionManagement: extensionManagement,
		BuiltinExtensions:   r.agentBuiltinExtensions(event),
		BuiltinSkills:       r.botProtocolBuiltinSkills(event),
		ReservedSkillNames:  []string{"platform", "bot-protocol"},
		CommandAllowlist:    cfg.AgentCommandAllowlist,
		CommandTimeoutMS:    cfg.AgentCommandTimeoutMS,
		// 这两项以前在 agent.Config 里存在但没人赋值，于是永远是 auto，
		// require 模式接不上。现在由机器人配置说了算。
		CommandSandbox:             cfg.AgentCommandSandbox,
		CommandSandboxAllowNetwork: cfg.AgentCommandSandboxAllowNetwork,
		FileWriteEnabled:           cfg.AgentFileWriteEnabled,
		BrowserCDPURL:              cfg.AgentBrowserCDPURL,
		BrowserTimeoutMS:           cfg.AgentBrowserTimeoutMS,
		BrowserControl:             r.browserControlFor(cfg),
		BuiltinBrowser:             r.browserBoxFor(cfg),
	}
}

// agentRegistryCacheKey 规范化共享底座的配置并给出缓存键。底座只放扩展，按
// ExtensionScope 共享：各机器人的步数、命令白名单、沙盒，以及随群变化的内置插件与
// 内置 Skill，都由请求视图叠加，不能拆出第二套 MCP 进程。
func agentRegistryCacheKey(cfg agent.Config) (agent.Config, string, error) {
	cfg = cfg.WithDefaults()
	cfg, err := agent.GlobalExtensionPaths(cfg)
	if err != nil {
		return agent.Config{}, "", err
	}
	baseCfg := cfg.ExtensionScope()
	keyBody, err := json.Marshal(baseCfg)
	if err != nil {
		return agent.Config{}, "", err
	}
	return baseCfg, string(keyBody), nil
}

// cachedAgentRegistry 只看缓存，不创建底座，也不启动任何 MCP 进程。
func (r *Runtime) cachedAgentRegistry(cfg agent.Config) *agent.ToolRegistry {
	_, key, err := agentRegistryCacheKey(cfg)
	if err != nil {
		return nil
	}
	r.agentRegistryMu.Lock()
	defer r.agentRegistryMu.Unlock()
	return r.agentRegistryCache[key]
}

func (r *Runtime) sharedAgentRegistry(ctx context.Context, cfg agent.Config) (*agent.ToolRegistry, error) {
	baseCfg, key, err := agentRegistryCacheKey(cfg)
	if err != nil {
		return nil, err
	}
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
	registry, err := agent.NewSharedExtensionRegistry(lifecycleCtx, baseCfg)
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
	// 插件带来的工具要能整条配档位，目录里就得记得谁带来了什么。这一步只构造工具
	// 对象读名字，不发请求、不起进程。
	toolOwners := r.plugins.AgentToolOwners(r.currentPlatform(event), overrides, r.pluginSettingOverridesForEvent(event))
	states := r.plugins.List()
	extensions := make([]agent.BuiltinExtension, 0, len(states))
	for _, state := range states {
		extensions = append(extensions, agent.BuiltinExtension{
			Tools:       append([]string(nil), toolOwners[state.Manifest.ID]...),
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
		Action:  "intent_recognition",
		Message: "Intent Recognition 已完成意图识别，工具和上下文建议仅供 Agent 参考",
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

// AgentResidencyEntry 是常驻档位界面里的一行：一个内置工具，或者一条 MCP 服务。
type AgentResidencyEntry struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Detail 是没被压过的描述。列表里显示的 Description 只有一行，而要不要常驻恰恰
	// 得看完整那段，所以全文一起发过去，由界面决定什么时候展开。
	Detail string   `json:"detail,omitempty"`
	Tools  []string `json:"tools,omitempty"`
	// Default 是不配档位时这一项的实际结果。
	Default bool `json:"default"`
	// Resident 是用户配的档位，nil 表示跟随默认。
	Resident *bool `json:"resident,omitempty"`
	// ResidentTokens 和 DeferredTokens 是这一项两档各占的 token：常驻是整份工具
	// 定义，按需只是目录里的一行。界面靠它把「常驻越多越贵」说成具体数字。
	ResidentTokens int64 `json:"resident_tokens,omitempty"`
	DeferredTokens int64 `json:"deferred_tokens,omitempty"`
	// Parent 是这个工具所属的插件或 MCP 服务的 ID。有 Parent 的工具不表态时跟着那一条
	// 走，界面据此把「跟随」算出来；Default 始终是内置名单的结果，不掺已配的档位。
	Parent string `json:"parent,omitempty"`
	// Stale 表示这一项只是从档位文件里反推出来的：进程重启后目录还没重新攒出来，
	// 但配过的档位仍然在生效，不能让界面显示成「没配过」。
	Stale bool `json:"stale,omitempty"`
}

// agentResidencyProtocolTools 是协议本身的工具，不接受档位：它们一定随请求下发，
// 配成按需等于把加载工具的那个工具也藏起来。
var agentResidencyProtocolTools = map[string]bool{
	agent.ToolsLoadToolName:    true,
	agent.ToolsExecuteToolName: true,
	"agent_finalize":           true,
}

// rememberAgentResidencyCatalog 记下这一轮实际注册的工具目录。
//
// 界面没法自己造一份这样的目录：内置工具是在组装回复时按平台、按权限、按插件开关
// 一个个挂上去的，不跑一轮就不知道这台机器人到底有哪些。所以档位界面显示的是最近
// 一轮真实用过的目录，而不是一份可能对不上的静态清单。
func (r *Runtime) rememberAgentResidencyCatalog(event MessageEvent, registry *agent.ToolRegistry) {
	if r == nil || registry == nil {
		return
	}
	// 一个工具可能同时属于两级：它所在的插件或 MCP 服务，和它自己。两级都摆出来，
	// 整条一档管大局，单个工具那一档用来破例。
	owner := map[string]string{}
	entries := []AgentResidencyEntry{}
	for _, state := range registry.Extensions() {
		kind := ""
		switch state.Kind {
		case agent.ExtensionKindMCP:
			kind = "mcp"
		case agent.ExtensionKindBuiltin:
			kind = "plugin"
		default:
			continue
		}
		tools := []string{}
		for _, name := range state.Tools {
			// 插件自报的工具名未必这一轮都注册上了（设置关掉了其中一个、平台不支持），
			// 以注册表为准，否则界面会列出一个根本不存在的工具。
			if _, ok := registry.Get(name); !ok || agentResidencyProtocolTools[name] {
				continue
			}
			tools = append(tools, name)
			owner[name] = state.ID
		}
		if len(tools) == 0 {
			continue
		}
		entry := AgentResidencyEntry{
			ID:          state.ID,
			Kind:        kind,
			Name:        state.Name,
			Description: agent.CompactToolDescription(state.Description, agent.SystemPromptToolDescriptionBudget),
			Detail:      strings.TrimSpace(state.Description),
			Tools:       tools,
		}
		// 整条的档位管它全部的工具，开销也按全部工具加起来算。
		for _, name := range tools {
			if tool, ok := registry.Get(name); ok {
				resident, deferred := agent.ResidencyCost(tool)
				entry.ResidentTokens += resident
				entry.DeferredTokens += deferred
			}
		}
		entries = append(entries, entry)
	}
	for _, name := range registry.Names() {
		if agentResidencyProtocolTools[name] {
			continue
		}
		entry := AgentResidencyEntry{ID: agent.ToolResidentID(name), Kind: "tool", Name: name, Parent: owner[name]}
		if tool, ok := registry.Get(name); ok {
			entry.Description = agent.CompactToolDescription(tool.Description(), agent.SystemPromptToolDescriptionBudget)
			entry.Detail = strings.Join(strings.Fields(tool.Description()), " ")
			entry.ResidentTokens, entry.DeferredTokens = agent.ResidencyCost(tool)
		}
		entries = append(entries, entry)
	}
	defaults := map[string]bool{}
	for _, name := range replyAgentCoreTools {
		defaults[name] = true
	}
	for index := range entries {
		for _, name := range append([]string{entries[index].Name}, entries[index].Tools...) {
			if defaults[name] {
				entries[index].Default = true
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].Name < entries[j].Name
	})
	r.agentResidencyMu.Lock()
	if r.agentResidencyCatalog == nil {
		r.agentResidencyCatalog = map[string][]AgentResidencyEntry{}
	}
	r.agentResidencyCatalog[event.ProfileID] = entries
	r.agentResidencyMu.Unlock()
}

// AgentResidency 返回这台机器人最近一轮的工具目录、以及名单里有哪些。
//
// 第二个返回值是「这台机器人有没有自己的名单」。界面必须分得清「不在名单里」和
// 「还没列过名单」：前者就是不常驻，后者跟着内置推荐走，两种情况下同一个工具的
// 显示结果可能正相反。
func (r *Runtime) AgentResidency(profileID string) ([]AgentResidencyEntry, bool) {
	if r == nil {
		return nil, false
	}
	r.agentResidencyMu.RLock()
	entries := append([]AgentResidencyEntry(nil), r.agentResidencyCatalog[profileID]...)
	r.agentResidencyMu.RUnlock()
	overrides, err := agent.LoadExtensionOverrides(AgentWorkspaceDir(), profileID)
	if err != nil {
		return entries, false
	}
	listed := agent.ResidencyListed(overrides)
	known := make(map[string]bool, len(entries))
	for index := range entries {
		entries[index].Resident = agent.ResidentOverride(overrides, entries[index].ID)
		known[entries[index].ID] = true
	}
	// 目录还没攒出来的时候，配过档位的项也要露面：它们仍然在生效，界面说「没配过」
	// 会让人以为配置丢了，于是又配一遍。
	for _, id := range agent.ResidentOverrideIDs(overrides) {
		if known[id] {
			continue
		}
		kind, name := "plugin", id
		switch prefix, rest, _ := strings.Cut(id, ":"); prefix {
		case "tool", "mcp":
			kind, name = prefix, rest
		case "skill":
			// skill: 的档位归 Skills 标签管，不在这一页里露面。
			continue
		}
		// 插件 ID 没有前缀（official.music 这种），剩下的都按插件算：与其因为认不出
		// 前缀而把它藏了，不如显示出来——它确实还在生效。
		entries = append(entries, AgentResidencyEntry{
			ID: id, Kind: kind, Name: name, Stale: true,
			Resident: agent.ResidentOverride(overrides, id),
		})
	}
	return entries, listed
}

// SetAgentResidency 把一个 ID 加进常驻名单或拿出去。
func (r *Runtime) SetAgentResidency(profileID, id string, resident *bool) error {
	if strings.TrimSpace(profileID) == "" {
		return errors.New("请选择机器人后改常驻名单")
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("缺少要增删的对象")
	}
	return agent.SaveExtensionResidency(AgentWorkspaceDir(), profileID, id, resident, agent.RecommendedResidencyIDs(replyAgentCoreTools))
}

// SaveAgentResidencyList 整份写下这台机器人的常驻名单；ids 为 nil 表示退回推荐名单。
func (r *Runtime) SaveAgentResidencyList(profileID string, ids []string) error {
	if strings.TrimSpace(profileID) == "" {
		return errors.New("请选择机器人后改常驻名单")
	}
	return agent.SaveResidencyList(AgentWorkspaceDir(), profileID, ids)
}
