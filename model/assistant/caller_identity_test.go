// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

type callerProbeTool struct {
	seen  agent.CallerIdentity
	found bool
}

func (t *callerProbeTool) Name() string                { return "caller_probe" }
func (t *callerProbeTool) Description() string         { return "Report the caller." }
func (t *callerProbeTool) InputSchema() map[string]any { return nil }
func (t *callerProbeTool) Run(ctx context.Context, _ map[string]any) (string, error) {
	t.seen, t.found = agent.CallerIdentityFromContext(ctx)
	return "ok", nil
}

// 隐私代理只挡模型：模型那边全是别名，工具从 ctx 拿到的仍是真实账号、群号和消息 ID。
func TestAgentToolsReceiveRealCallerWhileModelSeesAliases(t *testing.T) {
	provider := &privacyAwareTestProvider{}
	provider.generate = func(call int, _ llm.GenerateRequest) (string, error) {
		switch call {
		case 1:
			return `{"action":"tool","tool":"tools_load","input":{"names":["caller_probe"]}}`, nil
		case 2:
			return `{"action":"tool","tool":"tools_execute","input":{"name":"caller_probe","input":{}}}`, nil
		case 3:
			return `{"action":"final","content":"好了"}`, nil
		}
		return "", fmt.Errorf("unexpected LLM call %d", call)
	}
	cfg := BotConfig{
		OwnerID:                   "10001",
		BotAccount:                "10000",
		AgentEnabled:              true,
		AgentMaxSteps:             3,
		LLMIdentityMaskingEnabled: boolPointer(true),
		ReplySafetyMasterEnabled:  boolPointer(false),
	}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, SelfID: "10000", GroupID: "20001", UserID: "10001", MessageID: "30001", ToMe: true}
	probe := &callerProbeTool{}

	messages := []llm.Message{{Role: llm.RoleUser, Content: `{"user_id":"10001","group_id":"20001","message_id":"30001","text":"查一下"}`}}
	if _, err := runtime.generateReply(context.Background(), cfg, event, RelationshipPolicy{Owner: true}, messages, nil, probe); err != nil {
		t.Fatal(err)
	}
	want := agent.CallerIdentity{Platform: PlatformOneBotV11, BotID: "10000", UserID: "10001", GroupID: "20001", MessageID: "30001", ChatType: "group", IsOwner: true}
	if !probe.found || probe.seen != want {
		t.Fatalf("caller = %#v (found %v), want %#v", probe.seen, probe.found, want)
	}
	for _, req := range provider.requests {
		text := requestTextForPrivacyTest(req)
		for _, realID := range []string{"10001", "20001", "30001"} {
			if strings.Contains(text, realID) {
				t.Fatalf("model request leaked %s: %s", realID, text)
			}
		}
	}
}

func TestCallerIdentityForPrivateEventHasNoGroup(t *testing.T) {
	cfg := BotConfig{OwnerID: "10001", BotAccount: "10000"}
	got := callerIdentityForEvent(cfg, MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindPrivate, GroupID: "stale", UserID: "10002", MessageID: "7"})
	want := agent.CallerIdentity{Platform: PlatformOneBotV11, BotID: "10000", UserID: "10002", MessageID: "7", ChatType: "private"}
	if got != want {
		t.Fatalf("caller = %#v, want %#v", got, want)
	}
}
