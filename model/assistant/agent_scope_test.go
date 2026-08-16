package assistant

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func TestParseReplyIntentDecisionKeepsOnlyRegisteredTools(t *testing.T) {
	registry := agent.NewToolRegistry(
		&scopeTestTool{name: "web_search.search"},
		&scopeTestTool{name: "browser_render"},
	)
	decision, scope, ok := parseReplyIntentDecision(`{
		"action":"none",
		"prompt":"",
		"tools":["web_search.search","missing.tool","web_search.search"],
		"context_message_ids":["m2","m2","m4"],
		"keep_older_summary":true
	}`, registry)
	if !ok || decision.Action != visualIntentNone || !scope.Routed {
		t.Fatalf("decision = %#v scope = %#v ok = %v", decision, scope, ok)
	}
	if strings.Join(scope.ToolNames, ",") != "web_search.search" {
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
	cfg := DefaultBotConfig()
	cfg.AgentWorkDir = workDir
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
	list, ok := registry.Get("extensions.list")
	if !ok {
		t.Fatal("extensions.list is missing for owner")
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
	for _, toolName := range []string{"skills.install", "mcp.install", "mcp.uninstall"} {
		if _, ok := registry.Get(toolName); !ok {
			t.Fatalf("owner extension management tool %q is missing", toolName)
		}
	}
}

func TestAgentRegistryExposesLLMConfigOnlyToOwner(t *testing.T) {
	workDir := t.TempDir()
	cfg := DefaultBotConfig()
	cfg.AgentWorkDir = workDir
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
		{name: "non-owner", event: MessageEvent{Kind: EventKindPrivate, UserID: "member"}, relationship: RelationshipPolicy{Tier: RelationshipFriend}, wantTool: false},
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
			_, gotTool := registry.Get("diana.llm_config")
			if gotTool != tt.wantTool {
				t.Fatalf("diana.llm_config visible = %v, want %v", gotTool, tt.wantTool)
			}
		})
	}
}

func TestOwnerAgentRegistryReusesSharedExtensionsAcrossRequests(t *testing.T) {
	workDir := t.TempDir()
	cfg := DefaultBotConfig()
	cfg.AgentWorkDir = workDir
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
		"tools":["web_search.search"],
		"context_message_ids":["m1"],
		"keep_older_summary":false
	}`}
	runtime := NewRuntime(BotConfig{}, nil, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1", RawMessage: "之前在聊长鑫存储"})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m2", RawMessage: "搜索一下具体 IPO 时间"}
	registry := agent.NewToolRegistry(&scopeTestTool{
		name:        "web_search.search",
		description: `实时搜索。input: {"query":"keywords","num_results":10}`,
	})

	decision, scope, ok := runtime.routeReplyIntent(context.Background(), event, event.RawMessage, registry, false)
	if !ok || decision.Action != visualIntentNone || !scope.Routed || strings.Join(scope.ToolNames, ",") != "web_search.search" {
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
	if len(payload.AvailableTools) != 1 || payload.AvailableTools[0].Name != "web_search.search" {
		t.Fatalf("available tools = %#v", payload.AvailableTools)
	}
	if strings.Contains(strings.ToLower(payload.AvailableTools[0].Description), "input:") || strings.Contains(payload.AvailableTools[0].Description, "num_results") {
		t.Fatalf("router catalog leaked schema: %#v", payload.AvailableTools[0])
	}
	for _, expected := range []string{"具体商品", "口碑", "味道", "好不好", "web_search.search"} {
		if !strings.Contains(provider.request.Messages[0].Content, expected) {
			t.Fatalf("router search guidance missing %q: %s", expected, provider.request.Messages[0].Content)
		}
	}
}

func TestQQSystemPromptOmitsUnselectedToolRules(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nil, NewPluginManager(), nil, nil, nil, nil)
	registry := agent.NewToolRegistry(&scopeTestTool{name: "web_search.search"})
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(
		MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "owner"},
		nil,
		false,
		RelationshipPolicy{Owner: true, AllowPersonalSchedule: true},
		true,
		registry,
	)
	for _, unexpected := range []string{"diana.config", "diana.llm_config", "diana.relationship", "diana.tasks", "diana.reminder", "diana.schedule", "diana.tts", "diana.qq_group"} {
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
		BotQQ:              "42",
		OwnerID:            "owner",
		AgentEnabled:       true,
		AgentWorkDir:       workDir,
		AgentSkillRoots:    []string{filepath.Join(workDir, "skills")},
		AgentMCPConfigPath: filepath.Join(workDir, "missing-mcp.json"),
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
