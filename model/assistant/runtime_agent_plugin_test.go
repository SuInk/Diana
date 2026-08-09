package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func TestGenerateReplyRunsOnlyPluginToolsWhenAgentDisabled(t *testing.T) {
	provider := &sequenceLLMProvider{responses: []string{
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

type sequenceLLMProvider struct {
	responses []string
	requests  []llm.GenerateRequest
}

func (p *sequenceLLMProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	response := p.responses[0]
	p.responses = p.responses[1:]
	return &llm.GenerateResponse{Text: response}, nil
}

type echoAgentTool struct {
	calls int
}

func (t *echoAgentTool) Name() string        { return "plugin.echo" }
func (t *echoAgentTool) Description() string { return `input: {"text":"value"}` }
func (t *echoAgentTool) Run(_ context.Context, input map[string]any) (string, error) {
	t.calls++
	return "echo: " + input["text"].(string), nil
}
