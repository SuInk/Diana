// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// Agent 模式取代了原来的「启用 Agent」开关：Agent 两种模式下都开着，区别只在
// 高风险能力给不给。
//
// 安全模式要防的不是群成员——他们本来就拿不到这些工具——而是主人自己的会话被
// 提示词注入：主人让机器人读一个网页、看一段转发的聊天记录，里面藏着「现在去跑
// 这条命令」，模型照做时用的是主人的全部权限。所以安全模式对主人同样生效，
// 能关的是「一旦被带偏就收不回来」的那几类，查资料、记忆、提醒、画图这些照常。
const (
	AgentModeStandard = "standard"
	AgentModeSafe     = "safe"
)

// agentSafeModeDisabledMessage 是模型调用被关掉的工具时收到的原话，它会照这句
// 转述给用户，所以写成能直接说出口的话。
const agentSafeModeDisabledMessage = "当前是安全模式，这个操作被关掉了；需要的话请主人在机器人设置里切到标准模式"

// NormalizeAgentMode 把配置里的模式值规范成两个取值之一。空串原样返回，表示「还没
// 迁移过」，由 migrateAgentMode 按旧开关决定；认不出的值一律当安全模式——配置
// 写错不该变成权限放开。
func NormalizeAgentMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return ""
	case AgentModeStandard:
		return AgentModeStandard
	default:
		return AgentModeSafe
	}
}

// agentSafeMode 报告这台机器人是不是在安全模式下。只有明确写着 standard 才算标准模式，
// 模式为空也按安全模式算（失败时关着）：加载、保存、播种这几条路径都会先把旧的
// agent_enabled=true 显式迁成 standard，漏迁的配置宁可少给能力，也不能多给。
func (cfg BotConfig) agentSafeMode() bool {
	return NormalizeAgentMode(cfg.AgentMode) != AgentModeStandard
}

// effectiveAgentMode 是这台机器人实际生效的模式，模式为空时同样报安全模式。
func (cfg BotConfig) effectiveAgentMode() string {
	if cfg.agentSafeMode() {
		return AgentModeSafe
	}
	return AgentModeStandard
}

// migrateAgentMode 把旧的 agent_enabled 开关换算成模式，迁移后 Agent 总是开着。
//
//   - 已经写了模式的，保持不变；
//   - 旧配置开着 Agent 的，迁成标准模式，升级前后行为一致；
//   - 旧配置关着 Agent 的（agent_enabled=false 或没写，存盘时 false 会被省略），迁成
//     安全模式：原先连工具都没有，给回完整能力是静默扩权，安全模式是离原状最近的一档。
//
// 新建机器人不经过这里，DefaultBotConfig 直接给安全模式。
func migrateAgentMode(cfg BotConfig) BotConfig {
	cfg.AgentMode = AgentModeForLegacyConfig(cfg.AgentMode, cfg.AgentEnabled)
	// 旧的非 Agent 路径（AgentEnabled=false）从界面上已经走不到了，待后续移除；这里
	// 把开关钉成 true，后面所有按 AgentEnabled 分支的地方都走 Agent 路径。
	cfg.AgentEnabled = true
	return cfg
}

// ValidateAgentMode 检查外部传进来的模式值：只认 standard、safe 和空（空表示沿用或按
// 旧开关换算）。接口和 config.yaml 写错时直接报错，而不是悄悄按安全模式处理——配错了
// 应该让人知道，不该在界面上看着像「保存成功」。
func ValidateAgentMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", AgentModeStandard, AgentModeSafe:
		return nil
	}
	return fmt.Errorf("agent_mode 只能是 standard（标准模式）或 safe（安全模式），收到 %q", strings.TrimSpace(mode))
}

// AgentModeForLegacyConfig 是旧配置的换算规则：写了模式就用写的，没写时 agent_enabled
// 开着算标准模式、关着或没写算安全模式。migrateAgentMode 和 config.yaml 播种共用它。
func AgentModeForLegacyConfig(mode string, agentEnabled bool) string {
	if mode = NormalizeAgentMode(mode); mode != "" {
		return mode
	}
	if agentEnabled {
		return AgentModeStandard
	}
	return AgentModeSafe
}

// WithAgentModeMigrated 对配置集里每台机器人做一次模式迁移，见 migrateAgentMode。
// 从数据库读出配置集时调用；迁移是幂等的，存回去之后再读不会再变。
func (s ProfileSet) WithAgentModeMigrated() ProfileSet {
	if len(s.Profiles) == 0 {
		return s
	}
	profiles := make([]BotConfig, len(s.Profiles))
	for i, profile := range s.Profiles {
		profiles[i] = migrateAgentMode(profile)
	}
	s.Profiles = profiles
	return s
}

// AgentSafeModeCategory 是安全模式关掉的一类能力。Impact 是给主人看的一句影响说明，
// 界面切换前的确认框和设置页的提示都直接用它。
type AgentSafeModeCategory struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Impact string `json:"impact"`
}

// AgentSafeModeRule 是安全模式关掉的一个工具，或工具里的某几种操作。
// Operations 为空表示整个工具关掉；非空时只关这几种操作，Field 是选操作的入参字段。
type AgentSafeModeRule struct {
	Category   string   `json:"category"`
	Tool       string   `json:"tool"`
	Field      string   `json:"field,omitempty"`
	Operations []string `json:"operations,omitempty"`
	Reason     string   `json:"reason"`
}

// agentSafeModeMCPTools 在规则表里代表「全部 MCP 工具」：它们的名字是运行时按服务
// 发现的，没法逐个列出。
const agentSafeModeMCPTools = "mcp__*"

const (
	safeModeCategoryHostExec   = "host_exec"
	safeModeCategoryThirdParty = "third_party"
	safeModeCategoryActAsOwner = "act_as_owner"
	safeModeCategoryFileWrite  = "file_write"
	safeModeCategorySelfModify = "self_modify"
)

// AgentSafeModeCategories 按界面展示顺序列出安全模式关掉的几类能力。
var AgentSafeModeCategories = []AgentSafeModeCategory{
	{ID: safeModeCategoryHostExec, Label: "本机代码执行", Impact: "本机命令（run_command）和编码代理停用，浏览器里也不能执行脚本"},
	{ID: safeModeCategoryThirdParty, Label: "安装和运行第三方代码", Impact: "不能安装、卸载或启用 Skill 和 MCP；已启用的 MCP 工具不可用（Skill 说明文档仍可读取）"},
	{ID: safeModeCategoryActAsOwner, Label: "以主人身份对外操作", Impact: "内置浏览器和浏览器控制扩展（带主人登录态）不能再操作；GitHub 写操作、跨会话/跨群发送停用；不能再建往别的会话发的事件触发、提醒和订阅，已有的这类任务暂停投递"},
	{ID: safeModeCategoryFileWrite, Label: "改动本地文件", Impact: "不能再写入、编辑、保存或整理工作区文件（列目录、读取、检索、发送附件照常）"},
	{ID: safeModeCategorySelfModify, Label: "改机器人设置和群管", Impact: "不能改机器人配置、回复屏蔽名单、机器人标记、模型设置和扩展权限，不能禁言、踢人或处理好友和加群请求"},
}

// AgentSafeModeRules 是安全模式关掉的全部工具和操作，唯一的一份：运行时按它摘工具，
// 界面按它生成说明，要审计「安全模式到底关了什么」只看这里。
//
// 没列进来的照常可用：聊天记录和历史媒体、远程图片、联网搜索和网页读取（一次性沙盒
// 浏览器 browser_render）、记忆、当前会话里的提醒和订阅、画图改图、渲染，以及工作区的
// 只读工具（list_files、read_file、find_files、grep、manage_files 的 stat、view_image、
// send_attachment）。
//
// 有意保留、不算漏网的写操作：relationship 的 set / adjust / portrait_* / romance_*（好感度、
// 画像和恋人关系）、self_note 的写入、notebook、thread_state。它们和记忆同一类，只改
// 机器人自己记下的东西，不碰本机、不对外发消息、不改权限；其中改别人好感度和清空自述
// 本来就只给主人，被带偏的代价是一段记错的记忆，主人在控制台能看到并改回。
//
// 除了调用时拦，安全模式还在到点时停发已有的「往别的会话发」的任务（盯别的群的事件触发、
// 替别人建的提醒和订阅），见 safeModeHoldsTask。
var AgentSafeModeRules = []AgentSafeModeRule{
	// 本机代码执行：命令、编码代理和页面脚本都是在本机（或主人的浏览器里）跑任意代码。
	{Category: safeModeCategoryHostExec, Tool: "run_command", Reason: "在本机执行命令"},
	{Category: safeModeCategoryHostExec, Tool: dianaCodingToolName, Reason: "编码代理在仓库里不受白名单约束地跑命令、改代码"},
	{Category: safeModeCategoryHostExec, Tool: "browser_eval", Reason: "在带登录态的页面里执行任意 JavaScript"},

	// 安装和运行第三方代码：装进来的东西带着本进程的全部权限跑。
	{Category: safeModeCategoryThirdParty, Tool: "install_skill", Reason: "从外部来源安装 Skill"},
	{Category: safeModeCategoryThirdParty, Tool: "uninstall_skill", Reason: "卸载 Skill 属于扩展管理，和安装同一档"},
	{Category: safeModeCategoryThirdParty, Tool: "mcp_install", Reason: "安装 MCP 服务就是在本机装一个第三方程序"},
	{Category: safeModeCategoryThirdParty, Tool: "mcp_uninstall", Reason: "MCP 服务管理，和安装同一档"},
	{Category: safeModeCategoryThirdParty, Tool: "mcp_set_enabled", Reason: "启用 MCP 服务等于让第三方程序开始跑"},
	{Category: safeModeCategoryThirdParty, Tool: agentSafeModeMCPTools, Reason: "MCP 服务是第三方程序，以机器人的权限运行，已启用的也不给用"},
	// mcp_media 取的是 MCP 工具交出来的媒体。那份暂存是各机器人共用的，安全模式的机器人
	// 自己跑不了 MCP，能取到的只会是别的（标准模式）机器人的 MCP 产物。
	{Category: safeModeCategoryThirdParty, Tool: dianaMCPMediaToolName, Reason: "取 MCP 工具的产物；安全模式不跑 MCP，共用暂存里只会是别的机器人的东西"},

	// 以主人身份对外操作：常驻浏览器和浏览器控制扩展都带着主人的登录态，连只读的
	// 截图、取文本读到的也是主人账号里的东西，所以整组关掉，只留一次性沙盒渲染。
	{Category: safeModeCategoryActAsOwner, Tool: "browser_open", Reason: "操作带主人登录态的浏览器"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_text", Reason: "读取带主人登录态的浏览器页面"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_click", Reason: "操作带主人登录态的浏览器"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_type", Reason: "操作带主人登录态的浏览器"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_screenshot", Reason: "读取带主人登录态的浏览器页面"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_tabs", Reason: "操作带主人登录态的浏览器"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_scroll", Reason: "操作带主人登录态的浏览器"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_press_key", Reason: "操作带主人登录态的浏览器"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_navigate", Reason: "操作带主人登录态的浏览器"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_select", Reason: "操作带主人登录态的浏览器"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_wait", Reason: "操作带主人登录态的浏览器"},
	// 请主人在内置浏览器里接手一步再继续：接着做的还是那个带登录态的浏览器。
	{Category: safeModeCategoryActAsOwner, Tool: dianaBrowserHandoffToolName, Reason: "请主人在带登录态的内置浏览器里接手后继续操作"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_ext_tabs", Reason: "操作主人自己的浏览器（浏览器控制扩展）"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_ext_read", Reason: "读取主人自己的浏览器页面（浏览器控制扩展）"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_ext_open", Reason: "操作主人自己的浏览器（浏览器控制扩展）"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_ext_click", Reason: "操作主人自己的浏览器（浏览器控制扩展）"},
	{Category: safeModeCategoryActAsOwner, Tool: "browser_ext_type", Reason: "操作主人自己的浏览器（浏览器控制扩展）"},
	{Category: safeModeCategoryActAsOwner, Tool: dianaGitHubToolName, Field: "operation",
		Operations: []string{"create", "update", "comment", "review", "close", "reopen", "approve"},
		Reason:     "以主人配置的 GitHub 身份写入仓库；读仓库、搜 Issue 照常"},
	{Category: safeModeCategoryActAsOwner, Tool: dianaCrossSessionToolName, Reason: "往当前会话以外的私聊或群发消息"},
	// 事件触发任务：盯别的群或任何地方的（where=group / anywhere）等于一条往别的会话
	// 发话、在别处跑 Agent 的长期通道，关掉；只盯当前会话、结果也发回当前会话的照常，
	// 查看、取消、删除照常。取保守的一边：where=group 哪怕填的就是当前群也算别处。
	{Category: safeModeCategoryActAsOwner, Tool: dianaEventTriggerToolName, Field: "operation",
		Operations: []string{eventTriggerOpCreateElsewhere},
		Reason:     "创建盯别的群或任何地方的事件触发任务；只盯当前会话的照常"},
	// 提醒和订阅（schedule、rss 含 X/Twitter、github 仓库订阅）：主人在私聊里用
	// target_user_id 替别人建的，到点发到那个人的私聊，周期查询还会在那边跑 Agent；
	// 按 id 改或立即执行一条投递到当前会话以外的已有任务（别的群、别人的私聊、WebUI 配
	// 的投递目标）同理——改掉内容或订阅源，发出去的就是新写的东西。只投递回当前会话的
	// 照常；取消、删除只会让任务少发，不拦。换算见 taskCanonicalOperation。
	{Category: safeModeCategoryActAsOwner, Tool: "reminder", Field: "operation",
		Operations: []string{"create_elsewhere", "update_elsewhere"},
		Reason:     "建或改投递到当前会话以外的提醒；当前会话里的提醒照常"},
	{Category: safeModeCategoryActAsOwner, Tool: dianaSubscriptionToolName, Field: "operation",
		Operations: []string{"create_elsewhere", "update_elsewhere", "run_elsewhere"},
		Reason:     "建、改或立即执行投递到当前会话以外的订阅；当前会话里的订阅照常"},

	// 改动本地文件：写入锁在 workspace 里，但注入能借它留下文件、改掉别的任务的产物。
	// 长期保存区 keep/ 的写入（save_to_workspace keep=true、write_file/edit_file/manage_files
	// 落在 keep/ 下）走的是同一组工具，一并关掉；读长期区照常。
	{Category: safeModeCategoryFileWrite, Tool: "write_file", Reason: "写入工作区文件"},
	{Category: safeModeCategoryFileWrite, Tool: "edit_file", Reason: "修改工作区文件"},
	{Category: safeModeCategoryFileWrite, Tool: dianaSaveToWorkspaceToolName, Reason: "把文件存进工作区"},
	{Category: safeModeCategoryFileWrite, Tool: agent.ManageFilesToolName, Field: "action",
		Operations: []string{"move", "copy", "delete", "mkdir"},
		Reason:     "挪动、复制、删除文件或建目录；stat 照常"},

	// 改机器人设置和群管：被带偏的模型改掉自己的设置，主人未必马上察觉。
	{Category: safeModeCategorySelfModify, Tool: botParticipationToolName, Field: "operation", Operations: []string{"update"}, Reason: "修改机器人的接话和闲聊设置；读取照常"},
	{Category: safeModeCategorySelfModify, Tool: replyBlockToolName, Field: "operation", Operations: []string{"block", "unblock"}, Reason: "修改回复屏蔽名单；查看照常"},
	{Category: safeModeCategorySelfModify, Tool: "bot_markers", Field: "operation", Operations: []string{"mark", "unmark"}, Reason: "修改机器人标记名单；查看照常"},
	{Category: safeModeCategorySelfModify, Tool: "llm_config", Field: "operation", Operations: []string{"update"}, Reason: "切换这台机器人用的模型；查看照常"},
	{Category: safeModeCategorySelfModify, Tool: dianaExtensionAccessToolName, Field: "action",
		Operations: []string{"bot_tier", "group_tier", "allow", "deny"},
		Reason:     "修改扩展对群成员的开放档位和名单；查看照常"},
	{Category: safeModeCategorySelfModify, Tool: dianaPlatformToolName, Field: "operation",
		Operations: []string{platformOpMute, platformOpUnmute, platformOpKick},
		Reason:     "禁言、解禁、踢人等群管操作；查群资料、撤回自己的消息照常"},
	{Category: safeModeCategorySelfModify, Tool: dianaOneBotRequestsToolName, Field: "operation", Operations: []string{"approve", "reject"}, Reason: "同意或拒绝好友、加群请求；查看照常"},
}

// restrictAgentConfigForMode 在安全模式下从源头上不构造高风险工具：命令白名单清空
// 就不注册 run_command，写入开关关掉就不注册 write_file 一类，浏览器两路都不接。
// 注册表层面还会再按 AgentSafeModeRules 摘一次，这里是纵深防御——对象压根不存在，
// 就不会有哪条漏掉的路径把它放出来。
func restrictAgentConfigForMode(cfg BotConfig, agentCfg agent.Config) agent.Config {
	if !cfg.agentSafeMode() {
		return agentCfg
	}
	agentCfg.CommandAllowlist = nil
	agentCfg.FileWriteEnabled = false
	agentCfg.BrowserControl = nil
	agentCfg.BuiltinBrowser = nil
	agentCfg.BrowserToolsDisabled = true
	agentCfg.ExtensionManagement = false
	return agentCfg
}

// agentFileWriteAllowed 是「这台机器人能不能往工作目录写」的唯一判断：写入开关打开、
// 且不在安全模式。不经过工具的写入（比如画图成品自动落进 outputs/）也要问它，
// 只看 AgentFileWriteEnabled 会让安全模式下照样往磁盘写。
func (cfg BotConfig) agentFileWriteAllowed() bool {
	return cfg.AgentFileWriteEnabled && !cfg.agentSafeMode()
}

// applyAgentSafeMode 按 AgentSafeModeRules 把注册表收窄到安全模式。标准模式原样返回。
//
// 关掉的工具不是删掉就算：名字留在注册表里，模型点名调用时收到的是「安全模式关了」，
// 系统提示词里也会单列一段，不会以为自己拼错了名字再去换名字猜。
func applyAgentSafeMode(cfg BotConfig, registry *agent.ToolRegistry) {
	if registry == nil || !cfg.agentSafeMode() {
		return
	}
	var tools []string
	var operations []agent.DisabledOperation
	for _, rule := range AgentSafeModeRules {
		switch {
		case rule.Tool == agentSafeModeMCPTools:
			registry.DisableMCPTools(agentSafeModeDisabledMessage)
		case len(rule.Operations) == 0:
			tools = append(tools, rule.Tool)
		default:
			operations = append(operations, agent.DisabledOperation{Tool: rule.Tool, Field: rule.Field, Values: rule.Operations})
		}
	}
	registry.DisableTools(agentSafeModeDisabledMessage, tools...)
	registry.DisableOperations(agentSafeModeDisabledMessage, operations...)
}

// AgentSafeModeCatalogCategory 是给界面的一类说明，带上这一类关掉的工具。
type AgentSafeModeCatalogCategory struct {
	AgentSafeModeCategory
	Rules []AgentSafeModeRule `json:"rules"`
}

// AgentSafeModeCatalog 按类别整理规则表，界面据此生成说明和确认框，不在前端另抄一份。
func AgentSafeModeCatalog() []AgentSafeModeCatalogCategory {
	out := make([]AgentSafeModeCatalogCategory, 0, len(AgentSafeModeCategories))
	for _, category := range AgentSafeModeCategories {
		item := AgentSafeModeCatalogCategory{AgentSafeModeCategory: category, Rules: []AgentSafeModeRule{}}
		for _, rule := range AgentSafeModeRules {
			if rule.Category == category.ID {
				rule.Operations = append([]string(nil), rule.Operations...)
				item.Rules = append(item.Rules, rule)
			}
		}
		out = append(out, item)
	}
	return out
}

// agentSafeModeDisabledList 把规则表压成一行一条，给 config 工具的只读快照用。
func agentSafeModeDisabledList(cfg BotConfig) []string {
	if !cfg.agentSafeMode() {
		return nil
	}
	out := make([]string, 0, len(AgentSafeModeRules))
	for _, rule := range AgentSafeModeRules {
		name := rule.Tool
		if len(rule.Operations) > 0 {
			name += "（" + rule.Field + "=" + strings.Join(rule.Operations, "/") + "）"
		}
		out = append(out, name)
	}
	return out
}

// RunningCodingJobCount 数这台机器人名下还在跑的编码任务。切到安全模式不会打断已经
// 派出去的任务——它们在独立进程里跑，中途杀掉会留下改了一半的仓库——只是之后不能
// 再派新的。界面切换前据此提醒主人，保存时也把它记进操作日志。
func (r *Runtime) RunningCodingJobCount(profileID string) int {
	profileID = strings.TrimSpace(profileID)
	if r == nil || profileID == "" {
		return 0
	}
	count := 0
	for _, job := range listCodingJobs() {
		if job.Status != codingJobStatusRunning {
			continue
		}
		if owner, ok := r.codingJobOwner(job.Target.ProfileID); ok && owner == profileID {
			count++
		}
	}
	return count
}

// safeModeLocalSkills 直接读 Skill 目录，给没有共享底座的安全模式会话用。只读 SKILL.md，
// 不碰 MCP 配置、不启动任何进程；目录读失败时返回能读到的那部分。
func safeModeLocalSkills(agentCfg agent.Config) []agent.SkillMetadata {
	paths, err := agent.GlobalExtensionPaths(agentCfg)
	if err != nil {
		log.Printf("diana agent: 安全模式读取 Skill 目录位置失败: %v", err)
		return nil
	}
	skills, err := agent.LoadSkills(paths.SkillRoots)
	if err != nil {
		log.Printf("diana agent: 安全模式读取 Skill 目录失败: %v", err)
	}
	return skills
}

// skillExtensionIDs 把 Skill 列表换成扩展 ID（skill:<名字>），算群成员放行范围用。
func skillExtensionIDs(skills []agent.SkillMetadata) []string {
	ids := make([]string, 0, len(skills))
	for _, skill := range skills {
		ids = append(ids, "skill:"+skill.Name)
	}
	sort.Strings(ids)
	return ids
}

// reminderDeliversElsewhere 报告这条任务到点时是不是往「建它的那条会话」以外的地方发：
//   - 事件触发任务盯的是别的群或任何地方（where=group / anywhere）；
//   - 提醒和订阅投递到私聊，而投递对象不是在对话里建它的人（主人替别人建的）。
//
// 旧记录没有 RequestedBy，分不出是本人建的还是主人替他建的，按本人算，不停发——宁可
// 漏停几条旧任务，也不能把用户自己建的提醒全部掐掉。WebUI 里配的投递目标是管理员
// 在控制台定的，不经过对话，不在这里管。
func reminderDeliversElsewhere(item Reminder) bool {
	if reminderIsEventTrigger(item) {
		spec, ok := decodeEventTrigger(item.EventTriggerJSON)
		return !ok || spec.WatchAnywhere || strings.TrimSpace(spec.WatchGroupID) != ""
	}
	if strings.TrimSpace(item.GroupID) != "" {
		return false
	}
	requester := normalizeRelationshipUserID(item.RequestedBy)
	return requester != "" && requester != normalizeRelationshipUserID(item.UserID)
}

// safeModeHoldsTask 报告这条任务现在该不该停发：所属机器人在安全模式，且任务往别的会话
// 发。停发只是这一次不投、任务原样保留，切回标准模式后照常投递。每条任务只记一次日志。
//
// 这是调用时拦截之外的第二道：标准模式下建好的这类任务，切到安全模式后不该继续往外发。
func (r *Runtime) safeModeHoldsTask(item Reminder) bool {
	if r == nil {
		return false
	}
	return r.safeModeTaskFilter()(item)
}

// safeModeTaskFilter 是会记日志的版本，调度路径用；列表和计数用 safeModeHoldChecker。
func (r *Runtime) safeModeTaskFilter() func(Reminder) bool {
	holds := r.safeModeHoldChecker()
	return func(item Reminder) bool {
		if !holds(item) {
			r.safeModeHeldLogged.Delete(item.ID)
			return false
		}
		if _, logged := r.safeModeHeldLogged.LoadOrStore(item.ID, true); !logged {
			log.Printf("diana agent: 机器人 %q 处于安全模式，任务 %s 往当前会话以外投递，暂停发送（任务保留，切回标准模式后恢复）", item.ProfileID, item.ID)
		}
		return true
	}
}

// safeModeHoldChecker 先把各机器人的模式取出来，返回的判断函数不再碰 r.mu：调用方可能
// 正拿着 reminderMu（见 claimDueReminders、claimEventTriggers），在里面再去拿 r.mu 会和
// 别处的加锁顺序打架。找机器人的规则和 profileConfig 一致。
func (r *Runtime) safeModeHoldChecker() func(Reminder) bool {
	r.mu.RLock()
	modes := make(map[string]bool, len(r.profileConfigs))
	for id, cfg := range r.profileConfigs {
		modes[strings.TrimSpace(id)] = cfg.agentSafeMode()
	}
	r.mu.RUnlock()
	safeFor := func(profileID string) bool {
		if safe, ok := modes[strings.TrimSpace(profileID)]; ok {
			return safe
		}
		if len(modes) == 1 {
			for _, safe := range modes {
				return safe
			}
		}
		return DefaultBotConfig().agentSafeMode()
	}
	return func(item Reminder) bool {
		return reminderDeliversElsewhere(item) && safeFor(item.ProfileID)
	}
}

// safeModeHeldReminderMaxDelay 是往别处投递的一次性提醒最多补发多久以前的：安全模式
// 期间停发的提醒切回标准模式后才投递，超过这个时长就取消不发。
const safeModeHeldReminderMaxDelay = 24 * time.Hour

// reminderStillPending 报告任务以后还会不会发：一次性提醒没发过、周期任务没取消、事件
// 触发任务没取消没过期、一次性的还没点燃过。停发计数只数这些。
func reminderStillPending(item Reminder, now time.Time) bool {
	if !item.CancelledAt.IsZero() {
		return false
	}
	if reminderIsEventTrigger(item) {
		spec, ok := decodeEventTrigger(item.EventTriggerJSON)
		if !ok || spec.Expired || (!spec.ExpiresAt.IsZero() && !spec.ExpiresAt.After(now)) {
			return false
		}
		return spec.Repeat || spec.FireCount == 0
	}
	if reminderIsRecurring(item) {
		return true
	}
	return item.LastRunAt.IsZero()
}

// SafeModeHeldTaskCount 数这台机器人名下「切到安全模式就会停发」的任务，界面切换前
// 的确认框里报这个数。
func (r *Runtime) SafeModeHeldTaskCount(profileID string) int {
	profileID = strings.TrimSpace(profileID)
	if r == nil || r.reminders == nil || profileID == "" {
		return 0
	}
	r.reminderMu.Lock()
	items := r.reminders.Reminders()
	r.reminderMu.Unlock()
	now := time.Now()
	count := 0
	for _, item := range items {
		if strings.TrimSpace(item.ProfileID) != profileID || !reminderStillPending(item, now) {
			continue
		}
		if reminderDeliversElsewhere(item) {
			count++
		}
	}
	return count
}
