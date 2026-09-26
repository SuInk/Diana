// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func TestParseReplyIntentDecisionKeepsOnlyRegisteredTools(t *testing.T) {
	registry := agent.NewToolRegistry(
		&scopeTestTool{name: "web_search"},
		&scopeTestTool{name: "browser_render"},
	)
	decision, scope, ok := parseReplyIntentDecision(`{
		"action":"none",
		"prompt":"",
		"tools":["web_search","missing.tool","web_search"],
		"context_message_ids":["m2","m2","m4"],
		"keep_older_summary":true
	}`, registry)
	if !ok || decision.Action != visualIntentNone || !scope.Routed {
		t.Fatalf("decision = %#v scope = %#v ok = %v", decision, scope, ok)
	}
	if strings.Join(scope.ToolNames, ",") != "web_search" {
		t.Fatalf("tools = %#v", scope.ToolNames)
	}
	if strings.Join(scope.ContextMessageIDs, ",") != "m2,m4" || !scope.KeepContextSummary {
		t.Fatalf("scope = %#v", scope)
	}
}

func TestOwnerAgentExtensionCatalogIncludesDefaultPlugins(t *testing.T) {
	plugins := NewDefaultPluginManager()
	runtime := &Runtime{plugins: plugins}
	workDir := t.TempDir()
	cfg := standardModeBotConfig()
	cfg.AgentMCPConfigPath = filepath.Join(workDir, "missing-mcp.json")
	registry, err := runtime.newAgentRegistry(
		context.Background(),
		cfg.WithDefaults(),
		MessageEvent{Kind: EventKindPrivate, UserID: "owner"},
		RelationshipPolicy{Owner: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, ok := registry.Get(dianaUsageToolName); !ok {
		t.Fatal("owner usage tool is missing")
	}
	list, ok := registry.Get("list_capabilities")
	if !ok {
		t.Fatal("list_capabilities is missing for owner")
	}
	body, err := list.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range plugins.List() {
		if !strings.Contains(body, state.Manifest.ID) || !strings.Contains(body, state.Manifest.Name) {
			t.Fatalf("default plugin %q missing from extension catalog: %s", state.Manifest.ID, body)
		}
	}
	for _, toolName := range []string{"install_skill", "mcp_install", "mcp_uninstall"} {
		if _, ok := registry.Get(toolName); !ok {
			t.Fatalf("owner extension management tool %q is missing", toolName)
		}
	}
}

func TestAgentRegistryExposesLLMConfigOnlyToOwner(t *testing.T) {
	workDir := t.TempDir()
	cfg := standardModeBotConfig()
	cfg.AgentSkillRoots = []string{filepath.Join(workDir, "skills")}
	cfg.AgentMCPConfigPath = filepath.Join(workDir, "missing-mcp.json")
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)

	tests := []struct {
		name         string
		event        MessageEvent
		relationship RelationshipPolicy
		wantTool     bool
	}{
		{name: "owner", event: MessageEvent{Kind: EventKindPrivate, UserID: "owner"}, relationship: RelationshipPolicy{Owner: true}, wantTool: true},
		{name: "non-owner", event: MessageEvent{Kind: EventKindPrivate, UserID: "member"}, relationship: RelationshipPolicy{Score: 60}, wantTool: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, err := runtime.newAgentRegistry(
				context.Background(),
				cfg.WithDefaults(),
				tt.event,
				tt.relationship,
				newDianaLLMConfigTool(runtime, tt.event),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer registry.Close()
			_, gotTool := registry.Get("llm_config")
			_, gotMarkers := registry.Get("bot_markers")
			if gotMarkers != tt.wantTool {
				t.Fatalf("bot marker tool visible=%v want=%v", gotMarkers, tt.wantTool)
			}
			if gotTool != tt.wantTool {
				t.Fatalf("llm_config visible = %v, want %v", gotTool, tt.wantTool)
			}
		})
	}
}

func TestOwnerAgentRegistryReusesSharedExtensionsAcrossRequests(t *testing.T) {
	workDir := t.TempDir()
	cfg := standardModeBotConfig()
	cfg.AgentSkillRoots = []string{filepath.Join(workDir, "skills")}
	cfg.AgentMCPConfigPath = filepath.Join(workDir, "missing-mcp.json")
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner"}
	policy := RelationshipPolicy{Owner: true}

	first, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), event, policy)
	if err != nil {
		t.Fatal(err)
	}
	base, err := runtime.sharedAgentRegistry(context.Background(), runtime.agentRegistryConfig(cfg, event, true))
	if err != nil {
		t.Fatal(err)
	}
	base.Register(&scopeTestTool{name: "mcp__shared__probe"})
	if _, ok := first.Get("mcp__shared__probe"); !ok {
		t.Fatal("existing request did not see shared extension update")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), event, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, ok := second.Get("mcp__shared__probe"); !ok {
		t.Fatal("closing first request closed or discarded the shared registry")
	}
	if len(runtime.agentRegistryCache) != 1 {
		t.Fatalf("shared registry cache entries = %d, want 1", len(runtime.agentRegistryCache))
	}
}

func TestFilterAgentReplyHistoryKeepsSelectedReferencesAndNeighbors(t *testing.T) {
	history := make([]MessageEvent, 0, 10)
	for index := 1; index <= 10; index++ {
		history = append(history, MessageEvent{MessageID: "m" + strconv.Itoa(index), RawMessage: "history"})
	}
	event := MessageEvent{
		MessageID:                "current",
		SemanticSourceMessageID:  "m7",
		SemanticSourceMessageIDs: []string{"m7", "m9"},
		Segments:                 []MessageSegment{{Type: "reply", Data: map[string]string{"id": "m2"}}},
		Quoted:                   &QuotedMessage{MessageID: "m2"},
	}
	scope := agentReplyScope{Routed: true, ContextMessageIDs: []string{"m5"}}

	filtered := filterAgentReplyHistory(history, event, scope)
	got := make([]string, 0, len(filtered))
	for _, item := range filtered {
		got = append(got, item.MessageID)
	}
	want := "m1,m2,m3,m4,m5,m6,m7,m8,m9,m10"
	if strings.Join(got, ",") != want {
		t.Fatalf("filtered IDs = %q, want %q", strings.Join(got, ","), want)
	}
}

func TestRouteReplyIntentUsesCompactToolCatalog(t *testing.T) {
	provider := &scopeRouteProvider{response: `{
		"action":"none",
		"prompt":"",
		"tools":["web_search"],
		"context_message_ids":["m1"],
		"keep_older_summary":false
	}`}
	runtime := NewRuntime(BotConfig{}, nil, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1", RawMessage: "之前在聊长鑫存储"})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m2", RawMessage: "搜索一下具体 IPO 时间"}
	registry := agent.NewToolRegistry(&scopeTestTool{
		name:        "web_search",
		description: `实时搜索。input: {"query":"keywords","num_results":10}`,
	})

	decision, scope, ok := runtime.routeReplyIntent(context.Background(), event, event.RawMessage, registry, false)
	if !ok || decision.Action != visualIntentNone || !scope.Routed || strings.Join(scope.ToolNames, ",") != "web_search" {
		t.Fatalf("decision = %#v scope = %#v ok = %v", decision, scope, ok)
	}
	if len(provider.request.Messages) != 2 {
		t.Fatalf("request messages = %#v", provider.request.Messages)
	}
	content := provider.request.Messages[1].Content
	start := strings.Index(content, "{")
	if start < 0 {
		t.Fatalf("router payload missing JSON: %s", content)
	}
	var payload visualIntentPayload
	if err := json.Unmarshal([]byte(content[start:]), &payload); err != nil {
		t.Fatalf("decode router payload: %v\n%s", err, content)
	}
	if len(payload.AvailableTools) != 1 || payload.AvailableTools[0].Name != "web_search" {
		t.Fatalf("available tools = %#v", payload.AvailableTools)
	}
	if strings.Contains(strings.ToLower(payload.AvailableTools[0].Description), "input:") || strings.Contains(payload.AvailableTools[0].Description, "num_results") {
		t.Fatalf("router catalog leaked schema: %#v", payload.AvailableTools[0])
	}
	for _, expected := range []string{"具体商品", "口碑", "味道", "好不好", "web_search"} {
		if !strings.Contains(provider.request.Messages[0].Content, expected) {
			t.Fatalf("router search guidance missing %q: %s", expected, provider.request.Messages[0].Content)
		}
	}
}

func TestSystemPromptOmitsUnselectedToolRules(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nil, NewPluginManager(), nil, nil, nil, nil)
	registry := agent.NewToolRegistry(&scopeTestTool{name: "web_search"})
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(
		MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "owner"},
		nil,
		false,
		RelationshipPolicy{Owner: true, AllowPersonalSchedule: true},
		true,
		registry,
	)
	for _, unexpected := range []string{"config", "llm_config", "relationship", "tasks", "reminder", "schedule", "tts", "match_avatar", dianaNotebookToolName} {
		if strings.Contains(prompt, unexpected) {
			t.Fatalf("prompt unexpectedly contains unselected tool %q: %s", unexpected, prompt)
		}
	}
}

func TestReplyToUsesSingleAgentDecisionWithoutPreRouter(t *testing.T) {
	provider := &scopeRouteProvider{response: `{
		"action":"none",
		"prompt":"",
		"tools":[],
		"context_message_ids":[],
		"keep_older_summary":false
	}`}
	channel := &recordingChannel{}
	workDir := t.TempDir()
	runtime := NewRuntime(BotConfig{
		BotAccount:         "42",
		OwnerID:            "owner",
		AgentEnabled:       true,
		AgentSkillRoots:    []string{filepath.Join(workDir, "skills")},
		AgentMCPConfigPath: filepath.Join(workDir, "missing-mcp.json"),
		// 这条只数 Agent 决策的调用次数，发送前审核的额外往返不在断言范围里，显式关掉。
		ReplySafetyMasterEnabled: boolPointer(false),
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	provider.reply = `{"action":"final","content":"普通自然语言回复"}`
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner", MessageID: "m1", RawMessage: "你好"}

	reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "普通自然语言回复" || provider.replyCalls != 1 {
		t.Fatalf("reply = %q reply calls = %d", reply, provider.replyCalls)
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != "普通自然语言回复" {
		t.Fatalf("sent = %#v", channel.sent)
	}
	if len(provider.request.Messages) != 0 {
		t.Fatalf("legacy pre-router was called: %#v", provider.request.Messages)
	}
	foundProtocol := false
	for _, message := range provider.replyRequest.Messages {
		if strings.Contains(message.Content, "Diana 的内置 Agent") && strings.Contains(message.Content, `{"action":"tool"`) {
			foundProtocol = true
		}
	}
	if !foundProtocol {
		t.Fatalf("Agent protocol missing: %#v", provider.replyRequest.Messages)
	}
}

type scopeTestTool struct {
	name        string
	description string
}

func (t *scopeTestTool) Name() string { return t.name }
func (t *scopeTestTool) Description() string {
	if t.description != "" {
		return t.description
	}
	return t.name
}
func (t *scopeTestTool) Run(context.Context, map[string]any) (string, error) { return "", nil }

type scopeRouteProvider struct {
	request      llm.GenerateRequest
	response     string
	replyRequest llm.GenerateRequest
	reply        string
	replyCalls   int
}

func (p *scopeRouteProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if requestMessagesContain(req.Messages, "功能路由器") {
		p.request = req
		return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: p.response}, nil
	}
	p.replyCalls++
	p.replyRequest = req
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: p.reply}, nil
}

// 扩展底座在机器人之间共享后，本地工具仍按各自配置：没开命令执行的机器人不能从底座
// 借到别的机器人的 run_command；随事件变化的内置 Skill 也只出现在对应请求里。
func TestSharedExtensionRegistryKeepsLocalToolsPerBot(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	defer runtime.closeAgentRegistryCache()
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner"}
	policy := RelationshipPolicy{Owner: true}
	withCommands, without := standardModeBotConfig(), standardModeBotConfig()
	withCommands.AgentCommandAllowlist = []string{"echo"}
	without.AgentCommandAllowlist = []string{}

	first, err := runtime.newAgentRegistry(context.Background(), withCommands.WithDefaults(), event, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := runtime.newAgentRegistry(context.Background(), without.WithDefaults(), event, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if len(runtime.agentRegistryCache) != 1 {
		t.Fatalf("扩展底座条目 = %d，想要 1", len(runtime.agentRegistryCache))
	}
	if _, ok := first.Get("run_command"); !ok {
		t.Fatal("开了命令白名单的机器人没有 run_command")
	}
	if _, ok := second.Get("run_command"); ok {
		t.Fatal("没开命令执行的机器人从共享底座借到了 run_command")
	}
	hasBotProtocol := false
	for _, skill := range second.Skills() {
		hasBotProtocol = hasBotProtocol || skill.Name == "bot-protocol"
	}
	if !hasBotProtocol {
		t.Fatalf("请求视图缺少内置 Skill：%+v", second.Skills())
	}
}

// stubExtensionCatalog 替代真实 MCP 进程：只提供「有这么一个服务，它发现了这些工具」。
type stubExtensionCatalog struct{ states []agent.ExtensionState }

func (c stubExtensionCatalog) Extensions() []agent.ExtensionState { return c.states }

func TestMemberMCPPermissionIsOptInPerRobot(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(dbDir, "diana.db"))
	workDir := AgentWorkspaceDir()
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := standardModeBotConfig()
	cfg.AgentSkillRoots = []string{filepath.Join(dbDir, "skills")}
	cfg.AgentMCPConfigPath = filepath.Join(dbDir, "missing-mcp.json")
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "member", ProfileID: "bot-a"}
	member := RelationshipPolicy{Score: 60}

	// 底座按已经跑起来的共享扩展模拟：一个 MCP 服务，发现了一个工具。
	baseCfg := runtime.agentRegistryConfig(cfg.WithDefaults(), event, true)
	_, key, err := agentRegistryCacheKey(baseCfg)
	if err != nil {
		t.Fatal(err)
	}
	base := agent.NewToolRegistry(&scopeTestTool{name: "mcp__probe__ping"})
	base.SetExtensionCatalog(stubExtensionCatalog{states: []agent.ExtensionState{{
		Kind:      agent.ExtensionKindMCP,
		ID:        "mcp:probe",
		Name:      "probe",
		Installed: true,
		Enabled:   true,
		Tools:     []string{"mcp__probe__ping"},
	}}})
	runtime.agentRegistryCache = map[string]*agent.ToolRegistry{key: base}

	registry, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), event, member)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("mcp__probe__ping"); ok {
		t.Fatal("群成员默认拿到了 MCP 工具")
	}
	if !registry.PolicyDenied("mcp__probe__ping") {
		t.Fatal("拒绝原因没被识别成权限问题")
	}
	if _, ok := registry.Get("list_capabilities"); ok {
		t.Fatal("群成员看到了完整扩展目录")
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}

	overrides := workspaceStateTestPath(t, workDir, "extension-overrides.json")
	if err := os.WriteFile(overrides, []byte(`{"bot-a":{"members:mcp:probe":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), event, member)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if _, ok := opened.Get("mcp__probe__ping"); !ok {
		t.Fatal("放开后群成员仍然拿不到 MCP 工具")
	}
	if _, ok := opened.Get("read_file"); ok {
		t.Fatal("放开一个 MCP 把本地文件工具也带了出来")
	}

	// 名单限定之后，只有名单里的人在名单里的群能用。
	if err := os.WriteFile(overrides, []byte(`{"bot-a":{"members:mcp:probe":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceStateTestPath(t, workDir, "extension-audience.json"), []byte(`{"bot-a":{"mcp:probe":{"users":["member"],"groups":["g1"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	listed, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), event, member)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := listed.Get("mcp__probe__ping"); !ok {
		t.Fatal("名单里的人被挡住了")
	}
	if err := listed.Close(); err != nil {
		t.Fatal(err)
	}
	outsider := event
	outsider.UserID = "stranger"
	blocked, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), outsider, member)
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	if _, ok := blocked.Get("mcp__probe__ping"); ok {
		t.Fatal("名单外的人也拿到了工具")
	}
	elsewhere := event
	elsewhere.GroupID = "g9"
	otherGroup, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), elsewhere, member)
	if err != nil {
		t.Fatal(err)
	}
	defer otherGroup.Close()
	if _, ok := otherGroup.Get("mcp__probe__ping"); ok {
		t.Fatal("名单外的群也拿到了工具")
	}
	// 群管门槛：事件自带身份时直接判定，普通成员拿不到。
	if err := os.WriteFile(workspaceStateTestPath(t, workDir, "extension-audience.json"), []byte(`{"bot-a":{"mcp:probe":{"min_role":"admin"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	plain, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), event, member)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plain.Get("mcp__probe__ping"); ok {
		t.Fatal("普通成员越过了群管门槛")
	}
	if err := plain.Close(); err != nil {
		t.Fatal(err)
	}
	adminEvent := event
	adminEvent.SenderRole = "admin"
	asAdmin, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), adminEvent, member)
	if err != nil {
		t.Fatal(err)
	}
	defer asAdmin.Close()
	if _, ok := asAdmin.Get("mcp__probe__ping"); !ok {
		t.Fatal("群管理员没拿到设了群管门槛的工具")
	}
	if err := os.WriteFile(workspaceStateTestPath(t, workDir, "extension-audience.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// 另一台机器人没开，同一个底座下仍然只有主人能用。
	otherBot := event
	otherBot.ProfileID = "bot-b"
	other, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), otherBot, member)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, ok := other.Get("mcp__probe__ping"); ok {
		t.Fatal("群成员权限跨机器人生效了")
	}
}

func TestMemberSkillPermissionOpensOnlyTheChosenSkill(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(dbDir, "diana.db"))
	workDir := AgentWorkspaceDir()
	skillRoot := filepath.Join(dbDir, "skills")
	for _, name := range []string{"open-guide", "private-runbook"} {
		dir := filepath.Join(skillRoot, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: " + name + " 说明\n---\n" + name + " 正文"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 带脚本的 skill 要被标出来：成员没有命令和文件工具，脚本段落执行不了。
	if err := os.WriteFile(filepath.Join(skillRoot, "open-guide", "fetch.py"), []byte("print('hi')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	overrides := workspaceStateTestPath(t, workDir, "extension-overrides.json")
	if err := os.WriteFile(overrides, []byte(`{"bot-a":{"members:skill:open-guide":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := standardModeBotConfig()
	cfg.AgentSkillRoots = []string{skillRoot}
	cfg.AgentMCPConfigPath = filepath.Join(dbDir, "missing-mcp.json")
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "member", ProfileID: "bot-a"}

	registry, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), event, RelationshipPolicy{Score: 60})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	names := []string{}
	bundled := map[string]bool{}
	for _, skill := range registry.Skills() {
		names = append(names, skill.Name)
		bundled[skill.Name] = skill.Bundled
	}
	// 内置协议 skill 一直都在，自定义 skill 只应出现放开的那一份。
	if !slices.Contains(names, "open-guide") || slices.Contains(names, "private-runbook") {
		t.Fatalf("群成员看到的 skill = %v", names)
	}
	if !bundled["open-guide"] {
		t.Fatal("带脚本的 skill 没有被标记，界面提示不出来")
	}
	read, ok := registry.Get("read_skill")
	if !ok {
		t.Fatal("read_skill 不见了")
	}
	if _, err := read.Run(context.Background(), map[string]any{"name": "private-runbook"}); err == nil {
		t.Fatal("没放开的 skill 被读到了")
	}
	body, err := read.Run(context.Background(), map[string]any{"name": "open-guide"})
	if err != nil || !strings.Contains(body, "open-guide 正文") {
		t.Fatalf("放开的 skill 读不到：body=%q err=%v", body, err)
	}

	// 另一台机器人没开，同一份 skill 目录下成员仍然只有内置协议那几份。
	other := event
	other.ProfileID = "bot-b"
	closed, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), other, RelationshipPolicy{Score: 60})
	if err != nil {
		t.Fatal(err)
	}
	defer closed.Close()
	for _, skill := range closed.Skills() {
		if skill.Name == "open-guide" {
			t.Fatal("skill 的成员权限跨机器人生效了")
		}
	}
}

func TestGroupExtensionAccessOverridesBotTier(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(dbDir, "diana.db"))
	workDir := AgentWorkspaceDir()
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// 机器人那一档：仅主人。
	if err := os.WriteFile(workspaceStateTestPath(t, workDir, "extension-overrides.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := standardModeBotConfig()
	cfg.ID = "bot-a"
	cfg.OwnerID = "owner"
	cfg.AgentSkillRoots = []string{filepath.Join(dbDir, "skills")}
	cfg.AgentMCPConfigPath = filepath.Join(dbDir, "missing-mcp.json")
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "member", ProfileID: "bot-a"}

	baseCfg := runtime.agentRegistryConfig(cfg.WithDefaults(), event, true)
	_, key, err := agentRegistryCacheKey(baseCfg)
	if err != nil {
		t.Fatal(err)
	}
	base := agent.NewToolRegistry(&scopeTestTool{name: "mcp__probe__ping"})
	base.SetExtensionCatalog(stubExtensionCatalog{states: []agent.ExtensionState{{
		Kind: agent.ExtensionKindMCP, ID: "mcp:probe", Name: "probe", Installed: true, Enabled: true,
		Tools: []string{"mcp__probe__ping"},
	}}})
	runtime.agentRegistryCache = map[string]*agent.ToolRegistry{key: base}

	groupAccess := func(access GroupExtensionAccess) {
		groupCfg := DefaultGroupConfig("g1", cfg.WithDefaults())
		groupCfg.BotProfileID = "bot-a"
		groupCfg.ExtensionAccess = map[string]GroupExtensionAccess{"mcp:probe": access}
		runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"g1": groupCfg}})
	}
	visible := func(e MessageEvent, policy RelationshipPolicy) bool {
		registry, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), e, policy)
		if err != nil {
			t.Fatal(err)
		}
		defer registry.Close()
		_, ok := registry.Get("mcp__probe__ping")
		return ok
	}
	member := RelationshipPolicy{Score: 60}
	owner := RelationshipPolicy{Owner: true}
	adminEvent := event
	adminEvent.SenderRole = "admin"

	// 本群放宽到群成员：机器人那一档是仅主人，群里照样能用。
	groupAccess(GroupExtensionAccess{Tier: "members"})
	if !visible(event, member) {
		t.Fatal("本群放宽没有生效")
	}
	// 本群只给群管：普通成员挡住，管理员放行。
	groupAccess(GroupExtensionAccess{Tier: "admins"})
	if visible(event, member) {
		t.Fatal("普通成员越过了本群的群管档")
	}
	if !visible(adminEvent, member) {
		t.Fatal("群管理员被本群的群管档挡住了")
	}
	// 本群停用：主人也用不了。
	groupAccess(GroupExtensionAccess{Tier: "off"})
	if visible(event, member) || visible(event, owner) {
		t.Fatal("本群停用没有对所有人生效")
	}
	stranger := event
	stranger.UserID = "stranger"
	// 黑名单压过档位：这一档本来人人能用，名单里的人也用不了。
	groupAccess(GroupExtensionAccess{Tier: "members", Deny: []string{"member"}})
	if visible(event, member) {
		t.Fatal("黑名单没挡住")
	}
	if !visible(stranger, member) {
		t.Fatal("黑名单误伤了名单外的人")
	}
	// 白名单是例外放行：机器人和本群都只给主人，名单里的人照样能用。
	groupAccess(GroupExtensionAccess{Tier: "owner", Allow: []string{"member"}})
	if !visible(event, member) {
		t.Fatal("白名单没放行")
	}
	if visible(stranger, member) {
		t.Fatal("白名单外的人跟着放开了")
	}
	// 黑名单压过白名单。
	groupAccess(GroupExtensionAccess{Tier: "members", Allow: []string{"member"}, Deny: []string{"member"}})
	if visible(event, member) {
		t.Fatal("同时在黑白名单里时没有按黑名单处理")
	}
	// 停用压过白名单：这个群没这个能力，放行也放不出来。
	groupAccess(GroupExtensionAccess{Tier: "off", Allow: []string{"member"}})
	if visible(event, member) || visible(event, owner) {
		t.Fatal("停用被白名单绕过了")
	}

	// 私聊不看群配置。
	private := MessageEvent{Kind: EventKindPrivate, UserID: "owner", ProfileID: "bot-a"}
	if !visible(private, owner) {
		t.Fatal("群配置影响到了私聊")
	}

	// 机器人那个开关只是默认：默认关着的扩展，某个群可以单独打开。否则想让一个群
	// 用它，只能先全局打开再把别的群一个个关回去。
	if err := os.WriteFile(workspaceStateTestPath(t, workDir, "extension-overrides.json"), []byte(`{"bot-a":{"mcp:probe":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	groupAccess(GroupExtensionAccess{})
	if visible(event, member) || visible(event, owner) {
		t.Fatal("机器人默认关着、群里没说话时不该有这个扩展")
	}
	groupAccess(GroupExtensionAccess{Tier: "members"})
	if !visible(event, member) {
		t.Fatal("群里单独打开没有生效")
	}
	if !visible(event, owner) {
		t.Fatal("群里打开了，主人在这个群反而用不了")
	}
	// 本群显式停用仍然压过一切。
	groupAccess(GroupExtensionAccess{Tier: "off"})
	if visible(event, member) || visible(event, owner) {
		t.Fatal("本群停用没有对所有人生效")
	}
}

// 机器人级停用只是默认档，群里单独设过就该以群里那一档为准。
//
// 以前群级只能往严了改：群管理页把 MCP 开到群成员，机器人级那个停用仍然赢，用户开完
// 还被回一句「没启用」，群里那个开关等于摆设。
func TestGroupExtensionTierOverridesBotLevelDisable(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("APP_DB_PATH", filepath.Join(dbDir, "diana.db"))
	workDir := AgentWorkspaceDir()
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// 机器人级：这条 MCP 停用。
	if err := os.WriteFile(workspaceStateTestPath(t, workDir, "extension-overrides.json"), []byte(`{"bot-a":{"mcp:probe":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := standardModeBotConfig()
	cfg.ID = "bot-a"
	cfg.OwnerID = "owner"
	cfg.AgentSkillRoots = []string{filepath.Join(dbDir, "skills")}
	cfg.AgentMCPConfigPath = filepath.Join(dbDir, "missing-mcp.json")
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "member", ProfileID: "bot-a"}

	baseCfg := runtime.agentRegistryConfig(cfg.WithDefaults(), event, true)
	_, key, err := agentRegistryCacheKey(baseCfg)
	if err != nil {
		t.Fatal(err)
	}
	base := agent.NewToolRegistry(&scopeTestTool{name: "mcp__probe__ping"})
	base.SetExtensionCatalog(stubExtensionCatalog{states: []agent.ExtensionState{{
		Kind: agent.ExtensionKindMCP, ID: "mcp:probe", Name: "probe", Installed: true, Enabled: true,
		Tools: []string{"mcp__probe__ping"},
	}}})
	runtime.agentRegistryCache = map[string]*agent.ToolRegistry{key: base}

	groupAccess := func(access map[string]GroupExtensionAccess) {
		groupCfg := DefaultGroupConfig("g1", cfg.WithDefaults())
		groupCfg.BotProfileID = "bot-a"
		groupCfg.ExtensionAccess = access
		runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"g1": groupCfg}})
	}
	visible := func(e MessageEvent, policy RelationshipPolicy) bool {
		registry, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), e, policy)
		if err != nil {
			t.Fatal(err)
		}
		defer registry.Close()
		_, ok := registry.Get("mcp__probe__ping")
		return ok
	}
	member := RelationshipPolicy{Score: 60}
	owner := RelationshipPolicy{Owner: true}

	// 本群没设过：跟随机器人，停用照旧。
	groupAccess(nil)
	if visible(event, owner) || visible(event, member) {
		t.Fatal("机器人级停用在没有群级覆盖时失效了")
	}
	// 本群开到群成员：机器人级停用只是默认档，群里这一档说了算。
	groupAccess(map[string]GroupExtensionAccess{"mcp:probe": {Tier: "members"}})
	if !visible(event, member) {
		t.Fatal("群级开启没有盖过机器人级停用")
	}
	// 本群只给主人：主人能用，群成员不能。
	groupAccess(map[string]GroupExtensionAccess{"mcp:probe": {Tier: "owner"}})
	if !visible(event, owner) {
		t.Fatal("群级仅主人档没有把停用的扩展放出来")
	}
	if visible(event, member) {
		t.Fatal("群成员越过了仅主人档")
	}
	// 本群停用：两边都用不了。
	groupAccess(map[string]GroupExtensionAccess{"mcp:probe": {Tier: "off"}})
	if visible(event, owner) || visible(event, member) {
		t.Fatal("群级停用没有生效")
	}
}

// 群成员一句闲聊不拉起 MCP 底座，那一轮注册表里没有扩展。以前档位目录每轮整份覆盖，
// 主人会话记下的 MCP 被它冲掉，名单页上就一直看不到。
func TestResidencyCatalogSurvivesMemberRounds(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "diana.db"))
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	withMCP := func(tools ...string) *agent.ToolRegistry {
		registry := agent.NewToolRegistry()
		for _, name := range tools {
			registry.Register(&scopeTestTool{name: name})
		}
		registry.SetExtensionCatalog(stubExtensionCatalog{states: []agent.ExtensionState{
			{Kind: agent.ExtensionKindMCP, ID: "mcp:demo", Name: "demo", Enabled: true, Tools: []string{"mcp__demo__lookup"}},
		}})
		return registry
	}
	ids := func() map[string]bool {
		entries, _ := runtime.AgentResidency("bot-a")
		out := map[string]bool{}
		for _, entry := range entries {
			out[entry.ID] = true
		}
		return out
	}
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot-a", GroupID: "1"}
	runtime.rememberAgentResidencyCatalog(event, withMCP("web_search", "run_command", "mcp__demo__lookup"), true)
	runtime.rememberAgentResidencyCatalog(event, agent.NewToolRegistry(&scopeTestTool{name: "web_search"}), false)
	got := ids()
	for _, id := range []string{"mcp:demo", agent.ToolResidentID("mcp__demo__lookup"), agent.ToolResidentID("run_command"), agent.ToolResidentID("web_search")} {
		if !got[id] {
			t.Fatalf("%s lost after a member round: %v", id, got)
		}
	}

	// 主人那一轮以它为准：卸掉的 MCP 要能从名单页消失。
	runtime.rememberAgentResidencyCatalog(event, agent.NewToolRegistry(&scopeTestTool{name: "web_search"}), true)
	if got := ids(); got["mcp:demo"] || got[agent.ToolResidentID("run_command")] {
		t.Fatalf("owner round should replace the catalog: %v", got)
	}
}
