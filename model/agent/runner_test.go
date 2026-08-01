package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// TestParseActionAcceptsFencedJSON 验证对应功能场景。
func TestParseActionAcceptsFencedJSON(t *testing.T) {
	action, ok := parseAction("```json\n{\"action\":\"tool\",\"tool\":\"read_file\",\"input\":{\"path\":\"README.md\"}}\n```")
	if !ok {
		t.Fatal("expected JSON action")
	}
	if action.Action != "tool" || action.Tool != "read_file" || action.Input["path"] != "README.md" {
		t.Fatalf("action = %#v", action)
	}
}

// TestParseActionAcceptsFunctionCallJSON 验证兼容 Responses API function_call 形状。
func TestParseActionAcceptsFunctionCallJSON(t *testing.T) {
	action, ok := parseAction(`{"type":"function_call","name":"mcp__demo__echo","arguments":"{\"text\":\"hello\"}"}`)
	if !ok {
		t.Fatal("expected JSON action")
	}
	if action.Action != "tool" || action.Tool != "mcp__demo__echo" || action.Input["text"] != "hello" {
		t.Fatalf("action = %#v", action)
	}
}

// TestRunnerCallsToolAndReturnsFinal 验证对应功能场景。
func TestRunnerCallsToolAndReturnsFinal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TZ", "UTC")
	writeTestFile(t, dir, "note.txt", "hello from file")

	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"read_file","input":{"path":"note.txt"}}`,
		`{"action":"final","content":"文件里写着 hello from file"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: dir, MaxSteps: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "读一下 note.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "文件里写着 hello from file" {
		t.Fatalf("Text = %q", resp.Text)
	}
	if len(resp.Steps) != 1 || resp.Steps[0].Tool != "read_file" {
		t.Fatalf("Steps = %#v", resp.Steps)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(client.requests))
	}
	foundToolResult := false
	for _, msg := range client.requests[1].Messages {
		if strings.Contains(msg.Content, "hello from file") {
			foundToolResult = true
			break
		}
	}
	if !foundToolResult {
		t.Fatalf("second request did not include tool result: %#v", client.requests[1].Messages)
	}
}

// TestRunnerPromptIncludesSkills 验证 Agent prompt 只暴露 skills 清单和读取工具。
func TestRunnerPromptIncludesSkills(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetSkills([]SkillMetadata{{Name: "demo-skill", Description: "Use demo.", Path: "/tmp/demo/SKILL.md"}})
	runner := &Runner{cfg: Config{SkillsListBudget: 8000}.WithDefaults(), registry: registry}
	prompt := runner.systemPrompt(Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "请用 $demo-skill"}}})
	if !strings.Contains(prompt, "demo-skill") || !strings.Contains(prompt, "Explicitly Mentioned Skills") {
		t.Fatalf("prompt = %s", prompt)
	}
}

type scriptedClient struct {
	responses []string
	requests  []llm.GenerateRequest
}

// Generate 调用当前模型 provider 生成回复。
func (c *scriptedClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if len(c.responses) == 0 {
		return &llm.GenerateResponse{Text: `{"action":"final","content":"done"}`}, nil
	}
	next := c.responses[0]
	c.responses = c.responses[1:]
	return &llm.GenerateResponse{Text: next}, nil
}
