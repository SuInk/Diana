// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// standardModeBotConfig 是新建配置切到标准模式后的样子。测标准模式下工具怎么挂的
// 用例都从它起步：DefaultBotConfig 现在默认安全模式，高风险工具本来就不挂。
func standardModeBotConfig() BotConfig {
	cfg := DefaultBotConfig()
	cfg.AgentMode = AgentModeStandard
	return cfg
}

// 库里的旧配置按 agent_enabled 换算：开着的迁成标准模式（升级前后行为一致），关着或
// 没写的迁成安全模式；已经写了模式的不动。迁移后 Agent 一律开着。
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
		// 写错的模式值按安全模式处理：配置写错不该变成权限放开。
		"typo": AgentModeSafe,
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

func TestNewBotDefaultsToSafeMode(t *testing.T) {
	cfg := DefaultBotConfig()
	if cfg.AgentMode != AgentModeSafe || !cfg.AgentEnabled {
		t.Fatalf("新建机器人 mode=%q enabled=%v", cfg.AgentMode, cfg.AgentEnabled)
	}
	if got := PayloadFromConfig(cfg).AgentMode; got != AgentModeSafe {
		t.Fatalf("新建机器人的草稿 payload 模式 = %q", got)
	}
}

// 界面保存：请求写了模式就用请求的；编辑已有机器人时没带模式（旧前端只会回传
// agent_enabled=true）要沿用现有模式，不能把安全模式悄悄升成标准模式；新建和
// config.yaml 播种没带模式时按 agent_enabled 换算。
func TestConfigFromPayloadResolvesAgentMode(t *testing.T) {
	existingSafe := DefaultBotConfig()
	existingSafe.ID = "bot-a"

	tests := []struct {
		name     string
		payload  ConfigPayload
		existing BotConfig
		want     string
	}{
		{name: "显式标准", payload: ConfigPayload{ID: "bot-a", AgentMode: AgentModeStandard}, existing: existingSafe, want: AgentModeStandard},
		{name: "显式安全", payload: ConfigPayload{ID: "bot-a", AgentMode: AgentModeSafe, AgentEnabled: true}, existing: standardWithID("bot-a"), want: AgentModeSafe},
		{name: "旧前端编辑不升级", payload: ConfigPayload{ID: "bot-a", AgentEnabled: true}, existing: existingSafe, want: AgentModeSafe},
		{name: "新建旧开关开着", payload: ConfigPayload{AgentEnabled: true}, existing: DefaultBotConfig(), want: AgentModeStandard},
		{name: "新建旧开关关着", payload: ConfigPayload{}, existing: DefaultBotConfig(), want: AgentModeSafe},
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
	cfg := DefaultBotConfig()
	cfg.AgentMode = mode
	cfg.AgentMCPConfigPath = filepath.Join(t.TempDir(), "missing-mcp.json")
	runtime := &Runtime{plugins: NewPluginManager()}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", ProfileID: "bot-a"}
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
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	return registry
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
	safe := DefaultBotConfig().WithDefaults()
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
