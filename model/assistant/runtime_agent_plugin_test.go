// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
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
		`{"action":"tool","tool":"tools_load","input":{"names":["plugin.echo"]}}`,
		`{"action":"tool","tool":"tools_execute","input":{"name":"plugin.echo","input":{"text":"hello"}}}`,
		`{"action":"final","content":"search result used"}`,
	}}
	tool := &echoAgentTool{}
	plugins := NewPluginManager(&echoAgentToolPlugin{tool: tool})
	runtime := NewRuntime(BotConfig{
		OwnerID:      "owner",
		AgentEnabled: false,
		// 只验证插件工具路径，发送前审核的额外模型往返与本断言无关，关掉保持序列模型刚好够用。
		ReplySafetyMasterEnabled: boolPointer(false),
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
	if len(provider.requests) != 4 {
		t.Fatalf("provider requests = %d, want route + load + tool + final", len(provider.requests))
	}
}

// agentSequenceLLMProvider 按顺序回放预设回复，只服务主链路（路由、Agent 各轮）。
//
// 查证门控（evidence_gate.go）在后台和 Agent 同时调用同一个 provider。共用一条
// 序列时谁先拿到哪条全看调度：Agent 先到就拿着给路由的 JSON 去协议修复、多跑一轮，
// 把序列耗尽。门控一律答「不需要」，不占序列，记在 sideRequests 而不是 requests。
type agentSequenceLLMProvider struct {
	mu        sync.Mutex
	responses []string
	requests  []llm.GenerateRequest
	// sideRequests 记录被单独答掉的并行判断请求，便于需要时断言。
	sideRequests []llm.GenerateRequest
	// last 是序列用完后的兜底：重复最后一条，一般就是收尾的 final。多出来的
	// 调用仍会记进 requests，按调用次数断言的测试照样能发现。
	last string
}

func (p *agentSequenceLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if llmUsagePurposeFromContext(ctx) == PurposeEvidenceGate {
		p.sideRequests = append(p.sideRequests, req)
		return &llm.GenerateResponse{Text: `{"needs_evidence":false,"reason":"test"}`}, nil
	}
	p.requests = append(p.requests, req)
	if len(p.responses) == 0 {
		if p.last == "" {
			return nil, errors.New("agentSequenceLLMProvider: 预设回复为空")
		}
		return &llm.GenerateResponse{Text: p.last}, nil
	}
	p.last = p.responses[0]
	p.responses = p.responses[1:]
	return &llm.GenerateResponse{Text: p.last}, nil
}

// 查证门控和 Agent 并行调同一个 provider，不论谁先到，门控都不能吃掉主链路的序列。
func TestAgentSequenceProviderKeepsSequenceForEvidenceGate(t *testing.T) {
	provider := &agentSequenceLLMProvider{responses: []string{`{"action":"final","content":"好"}`}}
	gateCtx := withLLMUsagePurpose(context.Background(), PurposeEvidenceGate)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = provider.Generate(gateCtx, llm.GenerateRequest{})
		}()
	}
	wg.Wait()
	for range 2 {
		resp, err := provider.Generate(context.Background(), llm.GenerateRequest{})
		if err != nil || resp.Text != `{"action":"final","content":"好"}` {
			t.Fatalf("resp=%#v err=%v", resp, err)
		}
	}
	if len(provider.requests) != 2 || len(provider.sideRequests) != 8 {
		t.Fatalf("requests=%d side=%d", len(provider.requests), len(provider.sideRequests))
	}
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
