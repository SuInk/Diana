// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// standardModeBotConfig 是明确写着标准模式的新建配置。测标准模式下工具怎么挂的用例
// 都从它起步，不依赖 DefaultBotConfig 的默认模式。
func standardModeBotConfig() BotConfig {
	cfg := DefaultBotConfig()
	cfg.AgentMode = AgentModeStandard
	return cfg
}

// 库里的旧配置按 agent_enabled 换算：开着的迁成标准模式（升级前后行为一致），关着的
// 迁成安全模式（库里 false 被省掉，没写就是关着）；已经写了模式的不动；写坏的模式值
// 按默认的标准模式。迁移后 Agent 一律开着。
func TestStoredProfilesMigrateAgentEnabledToMode(t *testing.T) {
	// agent_enabled=false 存盘时被 omitempty 省掉，所以「没写」就是旧的「关着」。
	raw := `{"profiles":[
		{"id":"on","agent_enabled":true},
		{"id":"off"},
		{"id":"kept-standard","agent_mode":"standard"},
		{"id":"kept-safe","agent_enabled":true,"agent_mode":"safe"},
		{"id":"typo","agent_enabled":true,"agent_mode":"Standrd"}
	]}`
	var set ProfileSet
	if err := json.Unmarshal([]byte(raw), &set); err != nil {
		t.Fatal(err)
	}
	set = set.WithAgentModeMigrated().WithDefaults()
	want := map[string]string{
		"on":            AgentModeStandard,
		"off":           AgentModeSafe,
		"kept-standard": AgentModeStandard,
		"kept-safe":     AgentModeSafe,
		// 库里写坏的模式值按默认的标准模式处理（记一次警告），安全模式只认明确的 safe。
		"typo": AgentModeStandard,
	}
	for _, profile := range set.Profiles {
		if profile.AgentMode != want[profile.ID] {
			t.Fatalf("%s 迁移后模式 = %q，want %q", profile.ID, profile.AgentMode, want[profile.ID])
		}
		if !profile.AgentEnabled {
			t.Fatalf("%s 迁移后 Agent 仍是关着的", profile.ID)
		}
	}
	// 迁移幂等：存回去再读，结果不变。
	body, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	var again ProfileSet
	if err := json.Unmarshal(body, &again); err != nil {
		t.Fatal(err)
	}
	for _, profile := range again.WithAgentModeMigrated().Profiles {
		if profile.AgentMode != want[profile.ID] {
			t.Fatalf("%s 二次迁移后模式 = %q", profile.ID, profile.AgentMode)
		}
	}
}

// 安全模式只在明确写着 safe 时生效：模式为空、认不出都按默认的标准模式。
func TestAgentSafeModeOnlyWhenExplicitlySafe(t *testing.T) {
	for _, mode := range []string{"", "  ", AgentModeStandard, "Standrd"} {
		if (BotConfig{AgentMode: mode}).agentSafeMode() {
			t.Fatalf("模式 %q 被当成了安全模式", mode)
		}
		if got := (BotConfig{AgentMode: mode}).effectiveAgentMode(); got != AgentModeStandard {
			t.Fatalf("模式 %q 的实际模式 = %q", mode, got)
		}
	}
	for _, mode := range []string{AgentModeSafe, " Safe "} {
		if !(BotConfig{AgentMode: mode}).agentSafeMode() {
			t.Fatalf("明确的安全模式 %q 被当成了标准模式", mode)
		}
	}
	if NormalizeAgentMode("Standrd") != AgentModeStandard || NormalizeAgentMode("") != "" {
		t.Fatal("模式规范化规则变了")
	}
	if AgentModeForLegacyConfig("", true) != AgentModeStandard || AgentModeForLegacyConfig("", false) != AgentModeSafe || AgentModeForLegacyConfig("safe", true) != AgentModeSafe {
		t.Fatal("旧开关换算规则变了")
	}
}

func TestNewBotDefaultsToStandardMode(t *testing.T) {
	cfg := DefaultBotConfig()
	if cfg.AgentMode != AgentModeStandard || !cfg.AgentEnabled || cfg.agentSafeMode() {
		t.Fatalf("新建机器人 mode=%q enabled=%v", cfg.AgentMode, cfg.AgentEnabled)
	}
	if got := PayloadFromConfig(cfg).AgentMode; got != AgentModeStandard {
		t.Fatalf("新建机器人的草稿 payload 模式 = %q", got)
	}
}

// 界面保存：请求写了模式就用请求的；编辑已有机器人时没带模式（旧前端只会回传
// agent_enabled=true）要沿用现有模式，不能把安全模式悄悄升成标准模式；新建时没带
// 模式按默认的标准模式，旧前端新建只带 agent_enabled=true 也一样。config.yaml 播种
// 按旧开关换算，由播种方先写进 payload，见 AgentModeForLegacyConfig。
func TestConfigFromPayloadResolvesAgentMode(t *testing.T) {
	existingSafe := DefaultBotConfig()
	existingSafe.ID = "bot-a"
	existingSafe.AgentMode = AgentModeSafe

	tests := []struct {
		name     string
		payload  ConfigPayload
		existing BotConfig
		want     string
	}{
		{name: "显式标准", payload: ConfigPayload{ID: "bot-a", AgentMode: AgentModeStandard}, existing: existingSafe, want: AgentModeStandard},
		{name: "显式安全", payload: ConfigPayload{ID: "bot-a", AgentMode: AgentModeSafe, AgentEnabled: true}, existing: standardWithID("bot-a"), want: AgentModeSafe},
		{name: "旧前端编辑不升级", payload: ConfigPayload{ID: "bot-a", AgentEnabled: true}, existing: existingSafe, want: AgentModeSafe},
		{name: "旧前端新建是标准", payload: ConfigPayload{AgentEnabled: true}, existing: DefaultBotConfig(), want: AgentModeStandard},
		{name: "新建没写模式是标准", payload: ConfigPayload{}, existing: DefaultBotConfig(), want: AgentModeStandard},
		{name: "新建显式安全", payload: ConfigPayload{AgentMode: AgentModeSafe}, existing: DefaultBotConfig(), want: AgentModeSafe},
		{name: "播种旧开关开着", payload: ConfigPayload{AgentEnabled: true, AgentMode: AgentModeForLegacyConfig("", true)}, existing: DefaultBotConfig(), want: AgentModeStandard},
		{name: "播种旧开关关着", payload: ConfigPayload{AgentMode: AgentModeForLegacyConfig("", false)}, existing: DefaultBotConfig(), want: AgentModeSafe},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ConfigFromPayload(tt.payload, tt.existing)
			if cfg.AgentMode != tt.want {
				t.Fatalf("mode = %q，want %q", cfg.AgentMode, tt.want)
			}
			if !cfg.AgentEnabled {
				t.Fatal("保存后的配置 Agent 必须开着")
			}
		})
	}
}

func standardWithID(id string) BotConfig {
	cfg := standardModeBotConfig()
	cfg.ID = id
	return cfg
}

// safeModeTestRegistry 按主人身份建一份注册表，并挂上安全模式要管的那些运行时工具。
func safeModeTestRegistry(t *testing.T, mode string) *agent.ToolRegistry {
	t.Helper()
	registry, _ := safeModeTestRegistryWithRuntime(t, mode)
	return registry
}

func safeModeTestRegistryWithRuntime(t *testing.T, mode string) (*agent.ToolRegistry, *Runtime) {
	t.Helper()
	return safeModeRegistryForEvent(t, mode, MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", ProfileID: "bot-a"})
}

func safeModeRegistryForEvent(t *testing.T, mode string, event MessageEvent, reminders ...ReminderStore) (*agent.ToolRegistry, *Runtime) {
	t.Helper()
	cfg := DefaultBotConfig()
	cfg.AgentMode = mode
	cfg.AgentMCPConfigPath = filepath.Join(t.TempDir(), "missing-mcp.json")
	runtime := &Runtime{plugins: NewPluginManager()}
	if len(reminders) > 0 {
		runtime.reminders = reminders[0]
	}
	registry, err := runtime.newAgentRegistry(
		context.Background(),
		cfg.WithDefaults(),
		event,
		RelationshipPolicy{Owner: true},
		newDianaChatHistoryTool(runtime, event),
		newDianaReminderTool(runtime, event),
		newDianaRenderTool(runtime, event),
		newDianaCrossSessionTool(runtime, event, true),
		newDianaSaveToWorkspaceTool(runtime, event),
		newDianaPlatformTool(runtime, event),
		newDianaBotParticipationTool(runtime, event),
		newDianaReplyBlockTool(runtime, event),
		newDianaOneBotRequestsTool(runtime, event),
		newDianaLLMConfigTool(runtime, event),
		newDianaCodingTool(runtime, event, codingSettings(nil)),
		newDianaGitHubTool(runtime, event, nil, nil),
		newDianaEventTriggerTool(runtime, event),
		newDianaMCPMediaTool(runtime, event),
		newDianaSubscriptionTool(
			subscriptionBackend{kind: subscriptionKindSchedule, operations: []string{"create", "list", "update", "cancel", "delete"}, delegate: newDianaScheduleTool(runtime, event)},
			subscriptionBackend{kind: subscriptionKindRSS, operations: []string{"create", "list", "update", "cancel", "delete"}, delegate: newDianaRSSWatchTool(runtime, event)},
			subscriptionBackend{kind: subscriptionKindGitHub, operations: []string{"create", "list", "update", "cancel", "delete", "run"}, delegate: subscriptionGitHubDelegate(newDianaRepositoryWatchTool(runtime, event, true, nil, nil))},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = registry.Close()
		runtime.closeAgentRegistryCache()
	})
	return registry, runtime
}

// 安全模式对主人同样生效：规则表里每一项都被关掉，调用时拿到的是那句中文说明；
// 读类操作和规则表之外的工具照常可用。
func TestSafeModeDisablesEveryRuleForOwner(t *testing.T) {
	registry := safeModeTestRegistry(t, AgentModeSafe)
	for _, rule := range AgentSafeModeRules {
		switch {
		case rule.Tool == agentSafeModeMCPTools:
			reason, ok := registry.DisabledReason("mcp__notes__search")
			if !ok || reason != agentSafeModeDisabledMessage {
				t.Fatalf("MCP 工具原因 = %q, %v", reason, ok)
			}
		case len(rule.Operations) == 0:
			if _, ok := registry.Get(rule.Tool); ok {
				t.Fatalf("%s（%s）在安全模式下仍然可用", rule.Tool, rule.Category)
			}
			reason, ok := registry.DisabledReason(rule.Tool)
			if !ok || reason != agentSafeModeDisabledMessage {
				t.Fatalf("%s 的停用原因 = %q, %v", rule.Tool, reason, ok)
			}
			if !registry.PolicyDenied(rule.Tool) {
				t.Fatalf("%s 应当报「被关掉」，而不是「不存在」", rule.Tool)
			}
		default:
			for _, operation := range rule.Operations {
				err := registry.OperationDisabledError(rule.Tool, map[string]any{rule.Field: operation})
				if err == nil || !strings.Contains(err.Error(), agentSafeModeDisabledMessage) {
					t.Fatalf("%s %s=%s 报错 = %v", rule.Tool, rule.Field, operation, err)
				}
			}
		}
	}
	for _, name := range []string{
		"read_file", "list_files", "grep", "find_files", agent.ManageFilesToolName,
		dianaChatHistoryToolName, "reminder", dianaRenderToolName, dianaPlatformToolName,
		botParticipationToolName, dianaGitHubToolName, "llm_config", dianaIdentityCheckToolName,
	} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("安全模式不该关掉 %s", name)
		}
	}
	for tool, input := range map[string]map[string]any{
		agent.ManageFilesToolName: {"action": "stat"},
		dianaPlatformToolName:     {"operation": platformOpGroupInfo},
		dianaGitHubToolName:       {"operation": "get"},
		botParticipationToolName:  {"operation": "get"},
		"llm_config":              {"operation": "list"},
	} {
		if err := registry.OperationDisabledError(tool, input); err != nil {
			t.Fatalf("%s 的只读操作被拦了: %v", tool, err)
		}
	}
	if summary := registry.DisabledSummary(); !strings.Contains(summary, "run_command") || !strings.Contains(summary, agentSafeModeDisabledMessage) {
		t.Fatalf("提示词里的停用清单 = %q", summary)
	}
}

// 标准模式和以前完全一样：高风险工具照常挂上，没有任何停用登记。
func TestStandardModeKeepsFullAgentSurface(t *testing.T) {
	registry := safeModeTestRegistry(t, AgentModeStandard)
	for _, name := range []string{"run_command", "write_file", "edit_file", dianaSaveToWorkspaceToolName, dianaCrossSessionToolName, dianaCodingToolName, "install_skill", "mcp_install"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("标准模式下 %s 没有挂上", name)
		}
	}
	if err := registry.OperationDisabledError(dianaPlatformToolName, map[string]any{"operation": platformOpKick}); err != nil {
		t.Fatalf("标准模式下踢人被拦: %v", err)
	}
	if summary := registry.DisabledSummary(); summary != "" {
		t.Fatalf("标准模式不该有停用清单: %q", summary)
	}
}

// 纵深防御：安全模式下高风险对象压根不构造，步数规则不受模式影响。
func TestSafeModeAgentConfigDropsRiskyCapabilitiesButKeepsLimits(t *testing.T) {
	runtime := &Runtime{}
	runtime.SetBrowserBox(stubBuiltinBrowser{url: "http://127.0.0.1:1234"})
	safe := DefaultBotConfig()
	safe.AgentMode = AgentModeSafe
	safe = safe.WithDefaults()
	if runtime.browserBoxFor(safe) != nil {
		t.Fatal("安全模式下仍然取到了内置浏览器")
	}
	if !runtime.browserToolsDisabledFor(safe) {
		t.Fatal("安全模式下交互式浏览器工具仍会登记")
	}
	owner := runtime.agentRegistryConfig(safe, MessageEvent{}, true)
	if len(owner.CommandAllowlist) != 0 || owner.FileWriteEnabled || owner.BuiltinBrowser != nil || owner.BrowserControl != nil || owner.ExtensionManagement {
		t.Fatalf("安全模式下主人的 Agent 配置仍带高风险能力: %+v", owner)
	}
	if owner.MaxSteps != agent.MaxAllowedSteps {
		t.Fatalf("主人步数 = %d，want %d", owner.MaxSteps, agent.MaxAllowedSteps)
	}
	member := runtime.agentRegistryConfig(safe, MessageEvent{}, false)
	if member.MaxSteps != agent.DefaultMaxSteps {
		t.Fatalf("群成员步数 = %d，want %d", member.MaxSteps, agent.DefaultMaxSteps)
	}
	over := DefaultBotConfig()
	over.AgentMaxSteps = 40
	if got := over.WithDefaults().AgentMaxSteps; got != agent.MaxAllowedSteps {
		t.Fatalf("步数上限 = %d，want %d", got, agent.MaxAllowedSteps)
	}
}

// 规则表是唯一的一份：每条规则都归在某个类别下，界面拿到的目录和它一一对应。
func TestAgentSafeModeCatalogCoversEveryRule(t *testing.T) {
	categories := map[string]bool{}
	for _, category := range AgentSafeModeCategories {
		categories[category.ID] = true
	}
	total := 0
	for _, item := range AgentSafeModeCatalog() {
		if item.Impact == "" {
			t.Fatalf("类别 %s 没有影响说明", item.ID)
		}
		total += len(item.Rules)
	}
	for _, rule := range AgentSafeModeRules {
		if !categories[rule.Category] {
			t.Fatalf("规则 %s 的类别 %q 不在类别表里", rule.Tool, rule.Category)
		}
		if len(rule.Operations) > 0 && rule.Field == "" {
			t.Fatalf("规则 %s 只关部分操作却没写字段名", rule.Tool)
		}
	}
	if total != len(AgentSafeModeRules) {
		t.Fatalf("目录里 %d 条规则，规则表 %d 条", total, len(AgentSafeModeRules))
	}
}

// 按操作拦截的规则必须和工具执行时用同一套换算，否则别名和缺省值就是旁路。这里要求
// 每个带操作规则的工具都实现 CanonicalOperation：新加规则忘了实现会在这里失败。
func TestEveryOperationRuleToolReportsCanonicalOperation(t *testing.T) {
	registry := safeModeTestRegistry(t, AgentModeSafe)
	for _, rule := range AgentSafeModeRules {
		if len(rule.Operations) == 0 {
			continue
		}
		tool, ok := registry.Get(rule.Tool)
		if !ok {
			t.Fatalf("测试注册表里没有 %s，覆盖不到它的规则", rule.Tool)
		}
		if _, ok := tool.(agent.CanonicalOperationTool); !ok {
			t.Fatalf("%s 有按操作的安全模式规则，却没实现 CanonicalOperation", rule.Tool)
		}
	}
}

// 端到端：真实工具挂在安全模式的注册表里，模型按各种别名、大小写和缺省参数调用，
// 经 Runner 执行后都必须拿到安全模式的拒绝，而不是被工具按别名执行掉。用例按工具
// 自己的别名表写，不是照抄规则表。
func TestSafeModeRunnerRejectsOperationAliases(t *testing.T) {
	registry := safeModeTestRegistry(t, AgentModeSafe)
	denied := []struct {
		tool  string
		input map[string]any
	}{
		{"github", map[string]any{"operation": "create_issue", "repository": "octo/demo"}},
		{"github", map[string]any{"operation": "new", "repository": "octo/demo"}},
		{"github", map[string]any{"operation": "CREATE", "repository": "octo/demo"}},
		{"github", map[string]any{"operation": "edit", "repository": "octo/demo", "number": 1}},
		{"github", map[string]any{"operation": "update_issue", "repository": "octo/demo", "number": 1}},
		{"github", map[string]any{"operation": "comment_issue", "repository": "octo/demo", "number": 1}},
		{"github", map[string]any{"operation": "reply", "repository": "octo/demo", "number": 1}},
		{"github", map[string]any{"operation": "review_pull", "repository": "octo/demo", "number": 1}},
		{"github", map[string]any{"operation": "pull_review", "repository": "octo/demo", "number": 1}},
		{"github", map[string]any{"operation": "closed", "repository": "octo/demo", "number": 1}},
		{"github", map[string]any{"operation": "set_state", "state": "closed", "repository": "octo/demo", "number": 1}},
		{"github", map[string]any{"operation": "set_state", "state": "open", "repository": "octo/demo", "number": 1}},
		{"github", map[string]any{"operation": "reopen", "repository": "octo/demo", "number": 1}},
		{"github", map[string]any{"operation": "approve_draft", "code": "00000"}},
		{"llm_config", map[string]any{"model": "other-model"}},
		{"llm_config", map[string]any{"operation": " Update ", "model": "other-model"}},
		{"bot_config", map[string]any{"operation": "update", "chat_level": "always"}},
		{"reply_block", map[string]any{"operation": "block", "user_id": "10002"}},
		{"reply_block", map[string]any{"operation": "unblock", "user_id": "10002"}},
		{"bot_markers", map[string]any{"operation": "mark", "scope": "bot", "user_id": "10002"}},
		{"bot_markers", map[string]any{"operation": "unmark", "scope": "group", "user_id": "10002"}},
		{"extension_access", map[string]any{"action": "bot_tier", "id": "mcp:notes", "tier": "members"}},
		{"extension_access", map[string]any{"action": "allow", "id": "mcp:notes", "user_id": "10002"}},
		{"platform", map[string]any{"operation": "KICK", "user_id": "10002"}},
		{"platform", map[string]any{"operation": "mute", "user_id": "10002", "duration": 60}},
		{"platform", map[string]any{"operation": "unmute", "user_id": "10002"}},
		{"onebot_requests", map[string]any{"operation": "approve", "id": "1"}},
		{"onebot_requests", map[string]any{"operation": "Reject", "id": "1"}},
		{agent.ManageFilesToolName, map[string]any{"action": "DELETE", "path": "a.txt"}},
		{agent.ManageFilesToolName, map[string]any{"action": "move", "path": "a.txt", "to": "b.txt"}},
		{agent.ManageFilesToolName, map[string]any{"action": "mkdir", "path": "keep/x"}},
		{"event_trigger", map[string]any{"operation": "create", "where": "group", "group_id": "20002", "message": "hi"}},
		{"event_trigger", map[string]any{"operation": "add", "where": "anywhere", "message": "hi"}},
		{"event_trigger", map[string]any{"operation": "create", "where": " GROUP ", "group_id": "20001", "message": "hi"}},
	}
	for _, tc := range denied {
		call, err := json.Marshal(map[string]any{"action": "tool", "tool": tc.tool, "input": tc.input})
		if err != nil {
			t.Fatal(err)
		}
		provider := &agentSequenceLLMProvider{responses: []string{string(call), `{"action":"final","content":"做不了"}`}}
		runner, err := agent.NewRunner(provider, agent.Config{MaxSteps: 3}, registry)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := runner.Run(context.Background(), agent.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "照做"}}})
		if err != nil {
			t.Fatalf("%s %v: %v", tc.tool, tc.input, err)
		}
		if len(resp.Steps) == 0 || !strings.Contains(resp.Steps[0].Error, agentSafeModeDisabledMessage) {
			t.Fatalf("%s %v 没有被安全模式拦下：%+v", tc.tool, tc.input, resp.Steps)
		}
	}

	allowed := []struct {
		tool  string
		input map[string]any
	}{
		{"github", map[string]any{"operation": "get"}},
		{"github", map[string]any{"operation": "view"}},
		{"github", map[string]any{"operation": "cancel_draft"}},
		{"llm_config", map[string]any{"operation": "list"}},
		{"bot_config", map[string]any{"operation": "get"}},
		{"reply_block", map[string]any{"operation": "list"}},
		{"extension_access", map[string]any{}},
		{"platform", map[string]any{"operation": "recall"}},
		{"onebot_requests", map[string]any{"operation": "list"}},
		{agent.ManageFilesToolName, map[string]any{"action": "stat", "path": "a.txt"}},
		{"event_trigger", map[string]any{"operation": "add", "message": "hi"}},
		{"event_trigger", map[string]any{"operation": "create", "where": "here", "message": "hi"}},
		{"event_trigger", map[string]any{"operation": "remove", "id": "1"}},
	}
	for _, tc := range allowed {
		if err := registry.OperationDisabledError(tc.tool, tc.input); err != nil {
			t.Fatalf("%s %v 不该被拦：%v", tc.tool, tc.input, err)
		}
	}
}

// 全是安全模式时不拉起共享扩展底座：底座一建就会启动配置里的全部 MCP 服务。Skill 说明
// 仍然读得到（直接读 Skill 目录），标准模式照旧拉起底座。
func TestSafeModeDoesNotStartSharedExtensionBase(t *testing.T) {
	registry, runtime := safeModeTestRegistryWithRuntime(t, AgentModeSafe)
	runtime.agentRegistryMu.Lock()
	started := len(runtime.agentRegistryCache)
	runtime.agentRegistryMu.Unlock()
	if started != 0 {
		t.Fatalf("安全模式拉起了 %d 个共享扩展底座", started)
	}
	if _, ok := registry.Get("read_skill"); !ok {
		t.Fatal("安全模式下 read_skill 不见了")
	}
	_, standard := safeModeTestRegistryWithRuntime(t, AgentModeStandard)
	standard.agentRegistryMu.Lock()
	started = len(standard.agentRegistryCache)
	standard.agentRegistryMu.Unlock()
	if started == 0 {
		t.Fatal("标准模式的主人会话应当拉起共享扩展底座")
	}
}

// runSafeModeCall 经 Runner 执行一次工具调用，返回第一步的记录。
func runSafeModeCall(t *testing.T, registry *agent.ToolRegistry, tool string, input map[string]any) agent.Step {
	t.Helper()
	call, err := json.Marshal(map[string]any{"action": "tool", "tool": tool, "input": input})
	if err != nil {
		t.Fatal(err)
	}
	provider := &agentSequenceLLMProvider{responses: []string{string(call), `{"action":"final","content":"好"}`}}
	runner, err := agent.NewRunner(provider, agent.Config{MaxSteps: 3}, registry)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), agent.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "照做"}}})
	if err != nil {
		t.Fatalf("%s %v: %v", tool, input, err)
	}
	if len(resp.Steps) == 0 {
		t.Fatalf("%s %v 没有执行记录", tool, input)
	}
	return resp.Steps[0]
}

// 主人在私聊里用 target_user_id 替别人建提醒和订阅，到点发到别人的私聊：安全模式下
// 创建和修改都要拦；只在当前会话里的照常。群里建的任务投递回当前群，不算别处。
func TestSafeModeRunnerRejectsTasksDeliveredElsewhere(t *testing.T) {
	private, _ := safeModeRegistryForEvent(t, AgentModeSafe, MessageEvent{Kind: EventKindPrivate, UserID: "10001", ProfileID: "bot-a"})
	denied := []struct {
		tool  string
		input map[string]any
	}{
		{"reminder", map[string]any{"operation": "create", "target_user_id": "20002", "delay": "1s", "message": "该开会了"}},
		{"reminder", map[string]any{"operation": "add", "target_user_id": "@20002", "delay": "1s", "message": "该开会了"}},
		{"reminder", map[string]any{"operation": "Edit", "target_user_id": "20002", "id": "r1", "message": "改了"}},
		{"subscription", map[string]any{"operation": "create", "kind": "schedule", "target_user_id": "20002", "interval": "1h", "query": "查天气"}},
		{"subscription", map[string]any{"operation": "update", "kind": "schedule", "target_user_id": "20002", "id": "s1", "query": "查股价"}},
		{"subscription", map[string]any{"operation": "create", "kind": "rss", "target_user_id": "20002", "feed_url": "https://example.com/feed", "judge_prompt": "有新文章就说"}},
		{"subscription", map[string]any{"operation": "update", "kind": "RSS", "target_user_id": "20002", "id": "w1", "judge_prompt": "都说"}},
	}
	for _, tc := range denied {
		step := runSafeModeCall(t, private, tc.tool, tc.input)
		if !strings.Contains(step.Error, agentSafeModeDisabledMessage) {
			t.Fatalf("%s %v 没有被安全模式拦下：%+v", tc.tool, tc.input, step)
		}
	}
	for tool, input := range map[string]map[string]any{
		"reminder":     {"operation": "create", "delay": "1s", "message": "喝水"},
		"subscription": {"operation": "create", "kind": "schedule", "target_user_id": "10001", "interval": "1h", "query": "查天气"},
	} {
		if err := private.OperationDisabledError(tool, input); err != nil {
			t.Fatalf("私聊里给自己建的 %s 不该被拦：%v", tool, err)
		}
	}
	group := safeModeTestRegistry(t, AgentModeSafe)
	if err := group.OperationDisabledError("reminder", map[string]any{"operation": "create", "target_user_id": "20002", "delay": "1s", "message": "该开会了"}); err != nil {
		t.Fatalf("群里建的提醒投递回当前群，不该被拦：%v", err)
	}
	if err := group.OperationDisabledError("reminder", map[string]any{"operation": "update", "target_user_id": "20002", "id": "r1"}); err == nil {
		t.Fatal("改别人名下的提醒可能改的是发到他私聊的话，应当拦")
	}
	if reason, ok := group.DisabledReason(dianaMCPMediaToolName); !ok || reason != agentSafeModeDisabledMessage {
		t.Fatalf("安全模式下 mcp_media 应当关掉：%q %v", reason, ok)
	}
}

// 标准模式下建好的「往别处发」的任务，切到安全模式后到点不发、任务保留；切回标准模式
// 后照常投递。给自己建的提醒不受影响。
func TestSafeModeHoldsTasksDeliveredElsewhereAtFireTime(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := &stubReminderStore{items: []Reminder{
		{ID: "for-other", Kind: ReminderKindMessage, ProfileID: "bot-a", OwnerID: "20002", UserID: "20002", RequestedBy: "10001", Message: "替别人建的", TriggerAt: past, CreatedAt: past},
		{ID: "for-self", Kind: ReminderKindMessage, ProfileID: "bot-a", OwnerID: "10001", UserID: "10001", RequestedBy: "10001", Message: "自己的", TriggerAt: past, CreatedAt: past},
		{ID: "legacy", Kind: ReminderKindMessage, ProfileID: "bot-a", OwnerID: "20003", UserID: "20003", Message: "旧记录", TriggerAt: past, CreatedAt: past},
	}}
	channel := &recordingChannel{}
	cfg := BotConfig{ID: "bot-a", Enabled: true, OwnerID: "10001", AgentEnabled: true, AgentMode: AgentModeSafe}
	runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, store, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	if runtime.SafeModeHeldTaskCount("bot-a") != 1 {
		t.Fatalf("会停发的任务数 = %d", runtime.SafeModeHeldTaskCount("bot-a"))
	}

	runtime.fireDueReminders(context.Background())
	sentTo := map[string]bool{}
	for _, message := range channel.sent {
		sentTo[message.UserID] = true
	}
	if sentTo["20002"] || !sentTo["10001"] || !sentTo["20003"] {
		t.Fatalf("安全模式下的投递 = %#v", channel.sent)
	}
	held := false
	for _, item := range store.items {
		if item.ID == "for-other" {
			held = true
		}
	}
	if !held {
		t.Fatal("停发的任务被删掉了，切回标准模式就恢复不了")
	}

	cfg.AgentMode = AgentModeStandard
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	channel.sent = nil
	runtime.fireDueReminders(context.Background())
	if len(channel.sent) != 1 || channel.sent[0].UserID != "20002" {
		t.Fatalf("切回标准模式后应当补发：%#v", channel.sent)
	}
}

// 事件触发任务盯别的群或任何地方的，安全模式下到点不点燃；只盯当前会话的照常。
func TestReminderDeliversElsewhereClassifiesEventTriggers(t *testing.T) {
	trigger := func(spec EventTrigger) Reminder {
		return Reminder{ID: "t", Kind: ReminderKindEventTrigger, EventTriggerJSON: encodeEventTrigger(spec)}
	}
	if !reminderDeliversElsewhere(trigger(EventTrigger{WatchGroupID: "20002"})) || !reminderDeliversElsewhere(trigger(EventTrigger{WatchAnywhere: true})) {
		t.Fatal("盯别的群、盯任何地方的触发任务应当算往别处发")
	}
	if reminderDeliversElsewhere(trigger(EventTrigger{})) {
		t.Fatal("只盯当前会话的触发任务不该算往别处发")
	}
}

func TestValidateAgentModeRejectsUnknownValues(t *testing.T) {
	for _, ok := range []string{"", "standard", " SAFE "} {
		if err := ValidateAgentMode(ok); err != nil {
			t.Fatalf("%q 应当合法：%v", ok, err)
		}
	}
	if err := ValidateAgentMode("Standrd"); err == nil || !strings.Contains(err.Error(), "standard") {
		t.Fatalf("写错的模式应当报错：%v", err)
	}
}

// elsewhereTaskStore 是一份按 id 改任务时会碰到的已有任务：有的投递回当前私聊，有的
// 投递到别的群、别人的私聊、WebUI 配的投递目标，还有一条是别的机器人的。
func elsewhereTaskStore() *stubReminderStore {
	future := time.Now().Add(time.Hour)
	targets := encodeReminderDeliveryTargets([]ReminderDeliveryTarget{{ProfileID: "bot-a", GroupID: "30004"}})
	return &stubReminderStore{items: []Reminder{
		{ID: "rem-own", Kind: ReminderKindMessage, ProfileID: "bot-a", OwnerID: "10001", UserID: "10001", Message: "喝水", TriggerAt: future},
		{ID: "rem-group", Kind: ReminderKindMessage, ProfileID: "bot-a", OwnerID: "10001", GroupID: "30003", UserID: "10001", Message: "开会", TriggerAt: future},
		{ID: "rem-other-bot", Kind: ReminderKindMessage, ProfileID: "bot-b", OwnerID: "10001", UserID: "10001", Message: "别的机器人的", TriggerAt: future},
		{ID: "sched-group", Kind: ReminderKindQuery, ProfileID: "bot-a", OwnerID: "10001", GroupID: "30003", UserID: "10001", Message: "查天气", IntervalSeconds: 3600, TriggerAt: future},
		{ID: "sched-other-bot", Kind: ReminderKindQuery, ProfileID: "bot-b", OwnerID: "10001", UserID: "10001", Message: "查天气", IntervalSeconds: 3600, TriggerAt: future},
		{ID: "rss-dm", Kind: ReminderKindRSSWatch, ProfileID: "bot-a", OwnerID: "20002", UserID: "20002", Message: "盯博客", IntervalSeconds: 3600, TriggerAt: future},
		{ID: "rss-webui", Kind: ReminderKindRSSWatch, ProfileID: "bot-a", OwnerID: "10001", UserID: "10001", NotificationTargetsJSON: targets, Message: "盯博客", IntervalSeconds: 3600, TriggerAt: future},
		{ID: "rss-other-bot", Kind: ReminderKindRSSWatch, ProfileID: "bot-b", OwnerID: "10001", UserID: "10001", Message: "盯博客", IntervalSeconds: 3600, TriggerAt: future},
		{ID: "gh-group", Kind: ReminderKindRepositoryWatch, ProfileID: "bot-a", OwnerID: "10001", GroupID: "30003", UserID: "10001", Repository: "octo/demo", IntervalSeconds: 3600, TriggerAt: future},
		{ID: "trigger-other-bot", Kind: ReminderKindEventTrigger, ProfileID: "bot-b", OwnerID: "10001", UserID: "10001", EventTriggerJSON: encodeEventTrigger(EventTrigger{Event: "message", Action: "message", DeliverTo: "event"})},
	}}
}

// 不带 target_user_id、按 id 改或立即执行一条投递到别处的已有任务，同样是往别处发话：
// 私聊里改群提醒的内容、改别人私聊里 RSS 订阅的源和判断条件、改 WebUI 配了投递目标的
// 订阅、改群里仓库订阅的仓库再立即执行，安全模式下都要拦。投递回当前私聊的照常改，
// 取消和删除不拦。
func TestSafeModeRunnerRejectsUpdatesOfTasksDeliveredElsewhere(t *testing.T) {
	registry, _ := safeModeRegistryForEvent(t, AgentModeSafe, MessageEvent{Kind: EventKindPrivate, UserID: "10001", ProfileID: "bot-a"}, elsewhereTaskStore())
	denied := []struct {
		tool  string
		input map[string]any
	}{
		{"reminder", map[string]any{"operation": "update", "id": "rem-group", "message": "广告"}},
		{"reminder", map[string]any{"operation": "edit", "id": "rem-group", "delay": "1s"}},
		{"subscription", map[string]any{"operation": "update", "kind": "schedule", "id": "sched-group", "query": "每次都发广告"}},
		{"subscription", map[string]any{"operation": "update", "kind": "rss", "id": "rss-dm", "feed_urls": []any{"https://example.invalid/feed"}, "judge_prompt": "每条都通知"}},
		{"subscription", map[string]any{"operation": "update", "kind": "rss", "id": "rss-webui", "judge_prompt": "每条都通知"}},
		{"subscription", map[string]any{"operation": "update", "kind": "github", "id": "gh-group", "repository": "evil/repo"}},
		{"subscription", map[string]any{"operation": "run", "kind": "github", "id": "gh-group"}},
	}
	for _, tc := range denied {
		step := runSafeModeCall(t, registry, tc.tool, tc.input)
		if !strings.Contains(step.Error, agentSafeModeDisabledMessage) {
			t.Fatalf("%s %v 没有被安全模式拦下：%+v", tc.tool, tc.input, step)
		}
	}
	for _, tc := range []struct {
		tool  string
		input map[string]any
	}{
		{"reminder", map[string]any{"operation": "update", "id": "rem-own", "message": "多喝水"}},
		{"reminder", map[string]any{"operation": "cancel", "id": "rem-group"}},
		{"subscription", map[string]any{"operation": "delete", "kind": "rss", "id": "rss-dm"}},
		{"subscription", map[string]any{"operation": "cancel", "kind": "github", "id": "gh-group"}},
	} {
		if err := registry.OperationDisabledError(tc.tool, tc.input); err != nil {
			t.Fatalf("%s %v 不该被拦：%v", tc.tool, tc.input, err)
		}
	}
}

// 按 id 碰别的机器人的任务，不管什么模式都按找不到处理：几台机器人主人是同一个号时，
// 以前按归属人匹配就能改到另一台机器人的提醒、周期查询和订阅。
func TestTaskToolsRefuseOtherBotsTasksInStandardMode(t *testing.T) {
	store := elsewhereTaskStore()
	registry, _ := safeModeRegistryForEvent(t, AgentModeStandard, MessageEvent{Kind: EventKindPrivate, UserID: "10001", ProfileID: "bot-a"}, store)
	for _, tc := range []struct {
		tool  string
		input map[string]any
	}{
		{"reminder", map[string]any{"operation": "update", "id": "rem-other-bot", "message": "改掉"}},
		{"reminder", map[string]any{"operation": "delete", "id": "rem-other-bot"}},
		{"subscription", map[string]any{"operation": "update", "kind": "schedule", "id": "sched-other-bot", "query": "改掉"}},
		{"subscription", map[string]any{"operation": "cancel", "kind": "rss", "id": "rss-other-bot"}},
		{"event_trigger", map[string]any{"operation": "cancel", "id": "trigger-other-bot"}},
		{"event_trigger", map[string]any{"operation": "delete", "id": "trigger-other-bot"}},
	} {
		step := runSafeModeCall(t, registry, tc.tool, tc.input)
		if !strings.Contains(step.Error, "没有找到") && !strings.Contains(step.Output, "没有找到") {
			t.Fatalf("%s %v 碰到了别的机器人的任务：%+v", tc.tool, tc.input, step)
		}
	}
	for _, item := range store.items {
		if item.ProfileID == "bot-b" && (item.Message == "改掉" || !item.CancelledAt.IsZero()) {
			t.Fatalf("别的机器人的任务被改了：%+v", item)
		}
	}
	if len(store.items) != len(elsewhereTaskStore().items) {
		t.Fatal("别的机器人的任务被删了")
	}
}

// 停发的一次性提醒切回标准模式后补发时注明原定时间；迟到超过一天的不再补发，直接取消。
// 周期任务停发期间什么都不写。
func TestHeldTasksResumeWithOriginalTimeOrExpire(t *testing.T) {
	store := &stubReminderStore{items: []Reminder{
		{ID: "late", Kind: ReminderKindMessage, ProfileID: "bot-a", OwnerID: "20002", UserID: "20002", RequestedBy: "10001", Message: "开会", TriggerAt: time.Now().Add(-2 * time.Hour)},
		{ID: "stale", Kind: ReminderKindMessage, ProfileID: "bot-a", OwnerID: "20003", UserID: "20003", RequestedBy: "10001", Message: "昨天的", TriggerAt: time.Now().Add(-25 * time.Hour)},
		{ID: "fired", Kind: ReminderKindMessage, ProfileID: "bot-a", OwnerID: "20004", UserID: "20004", RequestedBy: "10001", Message: "发过了", TriggerAt: time.Now().Add(-3 * time.Hour), LastRunAt: time.Now().Add(-3 * time.Hour)},
		{ID: "periodic", Kind: ReminderKindQuery, ProfileID: "bot-a", OwnerID: "20005", UserID: "20005", RequestedBy: "10001", Message: "查天气", IntervalSeconds: 3600, TriggerAt: time.Now().Add(-time.Minute)},
	}}
	channel := &recordingChannel{}
	cfg := BotConfig{ID: "bot-a", Enabled: true, OwnerID: "10001", AgentEnabled: true, AgentMode: AgentModeSafe}
	runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, store, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	// 发过的一次性提醒不算停发。
	if got := runtime.SafeModeHeldTaskCount("bot-a"); got != 3 {
		t.Fatalf("会停发的任务数 = %d，want 3", got)
	}
	before := append([]Reminder(nil), store.items...)
	runtime.fireDueReminders(context.Background())
	if len(channel.sent) != 0 {
		t.Fatalf("安全模式下不该投递：%#v", channel.sent)
	}
	for index, item := range store.items {
		if !item.LastRunAt.Equal(before[index].LastRunAt) || !item.TriggerAt.Equal(before[index].TriggerAt) {
			t.Fatalf("停发期间任务被改写了：%+v", item)
		}
	}

	cfg.AgentMode = AgentModeStandard
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	channel.sent = nil
	runtime.fireDueReminders(context.Background())
	var late *OutgoingMessage
	for index := range channel.sent {
		if channel.sent[index].UserID == "20003" {
			t.Fatalf("迟到超过一天的提醒不该补发：%#v", channel.sent[index])
		}
		if channel.sent[index].UserID == "20002" {
			late = &channel.sent[index]
		}
	}
	if late == nil || !strings.Contains(late.Text, "原定") || !strings.Contains(late.Text, "推迟送达") {
		t.Fatalf("补发的提醒应当注明原定时间：%#v", channel.sent)
	}
	for _, item := range store.items {
		if item.ID == "stale" && item.CancelledAt.IsZero() {
			t.Fatalf("过期不补发的提醒应当取消：%+v", item)
		}
	}
}

// 飞书 ou_xxx、QQ 官方 openid 这类非数字账号：身份比较不能靠只认数字的
// normalizeRelationshipUserID，否则两个不同的人都成了空串、被当成同一个人，改别人私聊
// 里的订阅就不算「别处」了。
func TestSafeModeComparesNonNumericAccountIDs(t *testing.T) {
	future := time.Now().Add(time.Hour)
	store := &stubReminderStore{items: []Reminder{
		{ID: "rss-ou-other", Kind: ReminderKindRSSWatch, ProfileID: "bot-a", OwnerID: "ou_other", UserID: "ou_other", Message: "盯博客", IntervalSeconds: 3600, TriggerAt: future},
		{ID: "rss-ou-own", Kind: ReminderKindRSSWatch, ProfileID: "bot-a", OwnerID: "ou_owner", UserID: "ou_owner", Message: "盯博客", IntervalSeconds: 3600, TriggerAt: future},
		{ID: "gh-ou-other", Kind: ReminderKindRepositoryWatch, ProfileID: "bot-a", OwnerID: "ou_other", UserID: "ou_other", Repository: "octo/demo", IntervalSeconds: 3600, TriggerAt: future},
	}}
	registry, _ := safeModeRegistryForEvent(t, AgentModeSafe, MessageEvent{Kind: EventKindPrivate, Platform: PlatformFeishu, UserID: "ou_owner", ProfileID: "bot-a"}, store)
	for _, tc := range []struct {
		tool  string
		input map[string]any
	}{
		{"subscription", map[string]any{"operation": "update", "kind": "rss", "id": "rss-ou-other", "judge_prompt": "每条都通知"}},
		{"subscription", map[string]any{"operation": "update", "kind": "github", "id": "gh-ou-other", "repository": "evil/repo"}},
		{"subscription", map[string]any{"operation": "run", "kind": "github", "id": "gh-ou-other"}},
		{"reminder", map[string]any{"operation": "create", "target_user_id": "ou_other", "delay": "1s", "message": "该开会了"}},
	} {
		step := runSafeModeCall(t, registry, tc.tool, tc.input)
		if !strings.Contains(step.Error, agentSafeModeDisabledMessage) {
			t.Fatalf("%s %v 没有被安全模式拦下：%+v", tc.tool, tc.input, step)
		}
	}
	for tool, input := range map[string]map[string]any{
		"subscription": {"operation": "update", "kind": "rss", "id": "rss-ou-own", "judge_prompt": "每条都通知"},
		"reminder":     {"operation": "create", "target_user_id": "ou_owner", "delay": "1s", "message": "喝水"},
	} {
		if err := registry.OperationDisabledError(tool, input); err != nil {
			t.Fatalf("%s %v 投递回自己，不该被拦：%v", tool, input, err)
		}
	}
	if !reminderDeliversElsewhere(Reminder{Kind: ReminderKindMessage, UserID: "ou_other", RequestedBy: "ou_owner"}) {
		t.Fatal("非数字账号替别人建的提醒应当算往别处发")
	}
	if reminderDeliversElsewhere(Reminder{Kind: ReminderKindMessage, UserID: "ou_owner", RequestedBy: "ou_owner"}) {
		t.Fatal("非数字账号给自己建的提醒不该算往别处发")
	}
}

func TestSameAccountID(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"10001", "10001", true},
		{"@10001", "[CQ:at,qq=10001]", true},
		{"ou_a", "ou_a", true},
		{"ou_a", "ou_b", false},
		{"", "", false},
		{"ou_a", "", false},
	} {
		if got := sameAccountID(tc.a, tc.b); got != tc.want {
			t.Fatalf("sameAccountID(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

// 标准模式下从没被停发过的提醒，停机一天多之后照常补发、不作废：只有真被安全模式
// 停发过的才过期作废。晚到超过 missedReminderGrace 的会注明是错过的提醒，但不是
// 安全模式的那种标注。
func TestStandardModeLateReminderIsNotDropped(t *testing.T) {
	store := &stubReminderStore{items: []Reminder{
		{ID: "after-downtime", Kind: ReminderKindMessage, ProfileID: "bot-a", OwnerID: "20002", UserID: "20002", RequestedBy: "10001", Message: "开会", TriggerAt: time.Now().Add(-30 * time.Hour)},
	}}
	channel := &recordingChannel{}
	cfg := BotConfig{ID: "bot-a", Enabled: true, OwnerID: "10001", AgentEnabled: true, AgentMode: AgentModeStandard}
	runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, store, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	runtime.fireDueReminders(context.Background())
	if len(channel.sent) != 1 || strings.Contains(channel.sent[0].Text, "安全模式") || !strings.Contains(channel.sent[0].Text, "错过的提醒") {
		t.Fatalf("标准模式下迟到的提醒应当照常投递，注明错过而不是安全模式停发：%#v", channel.sent)
	}
	if !store.items[0].CancelledAt.IsZero() {
		t.Fatal("标准模式下迟到的提醒不该被作废")
	}
}

// 查别人的提醒、周期查询只看这台机器人名下的；没有机器人 ID 的旧记录按这台机器人的算。
func TestTaskListsForOtherUsersStayWithinThisBot(t *testing.T) {
	future := time.Now().Add(time.Hour)
	store := &stubReminderStore{items: []Reminder{
		{ID: "mine-bot", Kind: ReminderKindMessage, ProfileID: "bot-a", OwnerID: "20002", UserID: "20002", Message: "这台的", TriggerAt: future},
		{ID: "other-bot", Kind: ReminderKindMessage, ProfileID: "bot-b", OwnerID: "20002", UserID: "20002", Message: "别的机器人的", TriggerAt: future},
		{ID: "legacy", Kind: ReminderKindMessage, OwnerID: "20002", UserID: "20002", Message: "旧记录", TriggerAt: future},
		{ID: "sched-other-bot", Kind: ReminderKindQuery, ProfileID: "bot-b", OwnerID: "20002", UserID: "20002", Message: "别的机器人的查询", IntervalSeconds: 3600, TriggerAt: future},
	}}
	cfg := BotConfig{ID: "bot-a", Enabled: true, OwnerID: "10001", AgentEnabled: true, AgentMode: AgentModeStandard}
	runtime := NewRuntime(cfg, &recordingChannel{}, NewPluginManager(), nil, store, nil, nil)
	runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", ProfileID: "bot-a"}
	out, err := newDianaReminderTool(runtime, event).Run(context.Background(), map[string]any{"operation": "list", "target_user_id": "20002"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "别的机器人的") || !strings.Contains(out, "这台的") || !strings.Contains(out, "旧记录") {
		t.Fatalf("提醒列表 = %s", out)
	}
	out, err = newDianaScheduleTool(runtime, event).Run(context.Background(), map[string]any{"operation": "list", "target_user_id": "20002"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "别的机器人的查询") {
		t.Fatalf("周期查询列表 = %s", out)
	}
	if !runtime.sameBotAsEvent("", event) {
		t.Fatal("没有机器人 ID 的旧记录应当按这台机器人的算")
	}
}
