// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func TestGenerateReplyRunsOnlyPluginToolsWhenAgentDisabled(t *testing.T) {
	provider := &agentSequenceLLMProvider{responses: []string{
		`{"action":"tool","tool":"plugin.echo","input":{"text":"hello"}}`,
		`{"action":"final","content":"plugin result used"}`,
	}}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	tool := &echoAgentTool{}
	reply, err := runtime.generateReplyWithAgentTools(
		context.Background(),
		BotConfig{AgentEnabled: false}.WithDefaults(),
		[]llm.Message{{Role: llm.RoleUser, Content: "hello"}},
		[]agent.Tool{tool},
	)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "plugin result used" || tool.calls != 1 {
		t.Fatalf("reply=%q calls=%d", reply, tool.calls)
	}
	if len(provider.requests) == 0 {
		t.Fatal("provider was not called")
	}
	prompt := provider.requests[0].Messages[0].Content
	if !strings.Contains(prompt, "plugin.echo") || strings.Contains(prompt, "list_files") || strings.Contains(prompt, "run_command") {
		t.Fatalf("unexpected tool prompt: %s", prompt)
	}
}

func TestReplyPathRunsInstalledPluginToolWhenAgentDisabled(t *testing.T) {
	provider := &agentSequenceLLMProvider{responses: []string{
		`{"action":"none","prompt":"","tools":["plugin.echo"],"context_message_ids":[],"keep_older_summary":false}`,
		`{"action":"tool","tool":"plugin.echo","input":{"text":"hello"}}`,
		`{"action":"final","content":"search result used"}`,
	}}
	tool := &echoAgentTool{}
	plugins := NewPluginManager(&echoAgentToolPlugin{tool: tool})
	runtime := NewRuntime(BotConfig{
		OwnerID:      "owner",
		AgentEnabled: false,
	}, nilChannel{}, plugins, nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})

	reply, err := runtime.replyTo(context.Background(), MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "owner",
		MessageID: "message-1",
	}, "search for this")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "search result used" || tool.calls != 1 {
		t.Fatalf("reply=%q calls=%d", reply, tool.calls)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("provider requests = %d, want route + tool + final", len(provider.requests))
	}
}

type agentSequenceLLMProvider struct {
	responses []string
	requests  []llm.GenerateRequest
}

func (p *agentSequenceLLMProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	response := p.responses[0]
	p.responses = p.responses[1:]
	return &llm.GenerateResponse{Text: response}, nil
}

type echoAgentTool struct {
	calls int
}

type echoAgentToolPlugin struct {
	tool *echoAgentTool
}

func (p *echoAgentToolPlugin) Manifest() PluginManifest {
	return PluginManifest{ID: "test.echo-tool", Name: "Echo", BuiltIn: true}
}

func (p *echoAgentToolPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func (p *echoAgentToolPlugin) AgentTools(SettingValues) ([]agent.Tool, error) {
	return []agent.Tool{p.tool}, nil
}

func (t *echoAgentTool) Name() string        { return "plugin.echo" }
func (t *echoAgentTool) Description() string { return `input: {"text":"value"}` }
func (t *echoAgentTool) Run(_ context.Context, input map[string]any) (string, error) {
	t.calls++
	return "echo: " + input["text"].(string), nil
}
