package assistant

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

type testConsoleConfigManager struct {
	calls   int
	payload map[string]any
	failure bool
}

func (*testConsoleConfigManager) Catalog() any {
	return map[string]any{"provider": map[string]any{"actions": []string{"update"}}, "plugin": map[string]any{"actions": []string{"update"}}}
}
func (*testConsoleConfigManager) Read(context.Context, string, string, string) (any, error) {
	return map[string]any{"api_key": "abc", "base_url": "https://private.invalid"}, nil
}
func (m *testConsoleConfigManager) Apply(_ context.Context, _, _, _, _ string, p map[string]any) (any, error) {
	m.calls++
	m.payload = p
	if m.failure {
		return nil, fmt.Errorf("Get https://private.invalid failed")
	}
	return map[string]any{"ok": true}, nil
}
func TestConsoleConfigPlainConfirmationIsolation(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.ID = "b"
	cfg.OwnerID = "owner"
	cfg.AgentMode = AgentModeStandard
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	manager := &testConsoleConfigManager{}
	r.SetConsoleConfigManager(manager)
	event := MessageEvent{ProfileID: "b", UserID: "owner", Kind: EventKindPrivate, MessageID: "1", ContextNamespace: "first"}
	run := func(e MessageEvent, in map[string]any) (string, error) {
		return (&dianaConsoleConfigTool{runtime: r, event: e}).Run(context.Background(), in)
	}
	stage := map[string]any{"operation": "stage", "resource": "provider", "action": "update", "target": "p", "payload": map[string]any{"api_key": "abc", "base_url": "https://private.invalid"}}
	out, err := run(event, stage)
	if err != nil {
		t.Fatal(err)
	}
	if manager.calls != 0 || strings.Contains(out, "abc") || strings.Contains(out, "private.invalid") {
		t.Fatalf("stage leaked or applied: %s", out)
	}
	confirm := map[string]any{"operation": "confirm"}
	for _, change := range []func(*MessageEvent){func(e *MessageEvent) { e.RawMessage = "确认" }, func(e *MessageEvent) { e.MessageID = "2"; e.RawMessage = "查询配置" }, func(e *MessageEvent) { e.MessageID = "2"; e.RawMessage = "确认"; e.ContextNamespace = "second" }, func(e *MessageEvent) { e.MessageID = "2"; e.RawMessage = "确认"; e.UserID = "other" }} {
		e := event
		change(&e)
		if _, err := run(e, confirm); err == nil {
			t.Fatal("invalid confirmation accepted")
		}
	}
	event.MessageID = "2"
	event.RawMessage = "确认"
	if _, err := run(event, confirm); err != nil {
		t.Fatal(err)
	}
	if manager.calls != 1 || manager.payload["api_key"] != "abc" {
		t.Fatal("didn't apply original pending values")
	}
	if _, err := run(event, confirm); err == nil {
		t.Fatal("replayed confirmation")
	}
}
func TestConsoleConfigConfirmationOnlyRunsOnceConcurrently(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.ID = "b"
	cfg.OwnerID = "owner"
	cfg.AgentMode = AgentModeStandard
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	manager := &testConsoleConfigManager{}
	r.SetConsoleConfigManager(manager)
	event := MessageEvent{ProfileID: "b", UserID: "owner", Kind: EventKindPrivate, MessageID: "1"}
	tool := &dianaConsoleConfigTool{runtime: r, event: event}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "stage", "resource": "provider", "action": "update", "payload": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	tool.event.MessageID = "2"
	tool.event.RawMessage = "确认"
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = tool.Run(context.Background(), map[string]any{"operation": "confirm"}) }()
	}
	wg.Wait()
	if manager.calls != 1 {
		t.Fatalf("applied %d times", manager.calls)
	}
}

func TestConsoleConfigExpiryFailureAndSafeMode(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.ID = "b"
	cfg.OwnerID = "owner"
	cfg.AgentMode = AgentModeStandard
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	manager := &testConsoleConfigManager{}
	r.SetConsoleConfigManager(manager)
	event := MessageEvent{ProfileID: "b", UserID: "owner", Kind: EventKindPrivate, MessageID: "1"}
	tool := &dianaConsoleConfigTool{runtime: r, event: event}
	stage := map[string]any{"operation": "stage", "resource": "provider", "action": "update", "payload": map[string]any{}}
	if _, err := tool.Run(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	key := configPendingKey(event)
	pending := r.pendingConsoleConfig[key]
	pending.expires = time.Now().Add(-time.Second)
	r.pendingConsoleConfig[key] = pending
	tool.event.MessageID = "2"
	tool.event.RawMessage = "确认"
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "confirm"}); err == nil || manager.calls != 0 {
		t.Fatal("expired request applied")
	}
	if _, err := tool.Run(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	manager.failure = true
	tool.event.MessageID = "3"
	out, err := tool.Run(context.Background(), map[string]any{"operation": "confirm"})
	if err == nil || out != "" || strings.Contains(err.Error(), "private.invalid") {
		t.Fatal("failure not safely reported")
	}
	cfg.AgentMode = AgentModeSafe
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
	if _, err := tool.Run(context.Background(), stage); err == nil {
		t.Fatal("safe mode allowed writes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Run(ctx, map[string]any{"operation": "get", "resource": "provider"}); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestConsoleConfigAdminChatPlainConfirmationEndToEnd(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	provider := &agentSequenceLLMProvider{responses: []string{
		`{"action":"tool","tool":"console_config","input":{"operation":"stage","resource":"provider","action":"update","target":"p","payload":{"name":"新名称"}}}`,
		`{"action":"final","content":"已暂存，请回复确认。"}`,
		`{"action":"tool","tool":"console_config","input":{"operation":"confirm"}}`,
		`{"action":"final","content":"已保存。"}`,
	}}
	cfg := DefaultBotConfig()
	cfg.ID = "b"
	cfg.OwnerID = "owner"
	cfg.AgentMode = AgentModeStandard
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	defer r.closeAgentRegistryCache()
	manager := &testConsoleConfigManager{}
	r.SetConsoleConfigManager(manager)
	ctx := WithAdminChatSession(context.Background(), "console-session")
	first, err := r.RunAdminChat(ctx, "b", agent.Request{TraceID: "first", Messages: AdminChatMessages([]llm.Message{{Role: llm.RoleUser, Content: "把提供商 p 改名为新名称"}})})
	if err != nil {
		t.Fatal(err)
	}
	if manager.calls != 0 || len(r.pendingConsoleConfig) != 1 {
		t.Fatalf("stage not wired: %+v", first)
	}
	second, err := r.RunAdminChat(ctx, "b", agent.Request{TraceID: "second", Messages: AdminChatMessages([]llm.Message{{Role: llm.RoleUser, Content: "确认"}})})
	if err != nil {
		t.Fatal(err)
	}
	if manager.calls != 1 {
		t.Fatalf("confirm not wired: %+v", second)
	}
}
