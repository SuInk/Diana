// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 提示词必须把「规划步」和「用户消息」区分开,并写明本条回复内的工具预算。
// 曾经的措辞「每轮最多调用一个工具」被模型理解成「用户每发一条消息只能调
// 一次」,多步任务每调一次就收口,让用户发「继续」才肯继续。
type currentRequirementContinuationClient struct {
	adapter  llm.LLMClient
	requests []llm.GenerateRequest
}

func (c *currentRequirementContinuationClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	req.Messages = append([]llm.Message(nil), req.Messages...)
	c.requests = append(c.requests, req)
	if len(c.requests) == 1 {
		return &llm.GenerateResponse{Text: `{"action":"tool","tool":"lookup","input":{}}`}, nil
	}
	return c.adapter.Generate(ctx, req)
}

// Exercise real JSON tool observations and both planning/finalization requests.
// The provider adapter must retain the original task or reject it before HTTP.
func TestRunnerKeepsOriginalCurrentRequirementAcrossContinuationBudget(t *testing.T) {
	for _, phase := range []string{"planning", "finalization"} {
		for _, tooSmall := range []bool{false, true} {
			name := phase + "/fits"
			if tooSmall {
				name = phase + "/capacity_error"
			}
			t.Run(name, func(t *testing.T) {
				original := "CURRENT_HEAD " + strings.Repeat("original requirement ", 1000) + " CRITICAL_MIDDLE_REQUIREMENT " + strings.Repeat("keep requirement ", 1000) + " CURRENT_TAIL"
				history := strings.Repeat("old optional history ", 20000)
				var httpCalls atomic.Int32
				bodies := make(chan string, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					httpCalls.Add(1)
					body, err := io.ReadAll(req.Body)
					if err != nil {
						t.Error(err)
					}
					bodies <- string(body)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "continuation", "choices": []any{map[string]any{
						"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"action":"final","content":"done"}`},
					}}})
				}))
				defer server.Close()
				window := int64(32768)
				if tooSmall {
					window = 2048
				}
				adapter, err := llm.NewClient(llm.ProviderConfig{
					Provider: llm.ProviderOpenAICompatible, APIFormat: llm.APIFormatChatCompletions,
					APIKey: "test-key", Model: "test-model", BaseURL: server.URL,
					ContextWindowTokens: window, MaxContextTokens: window, MaxOutputTokens: 128,
				}, llm.WithHTTPClient(server.Client()))
				if err != nil {
					t.Fatal(err)
				}
				client := &currentRequirementContinuationClient{adapter: adapter}
				tool := &countingTool{name: "lookup"}
				steps := 2
				if phase == "finalization" {
					steps = 1
				}
				runner, err := NewRunner(client, Config{MaxSteps: steps}, NewToolRegistry(tool))
				if err != nil {
					t.Fatal(err)
				}
				messages := []llm.Message{
					{Role: llm.RoleUser, Content: history, Priority: llm.MessagePriorityHistory},
					{Role: llm.RoleAssistant, Content: "earlier reply", Priority: llm.MessagePriorityHistory},
					{Role: llm.RoleUser, Content: original},
				}
				response, err := runner.Run(t.Context(), Request{Messages: messages})
				if messages[2].Priority != llm.MessagePriorityDefault || messages[0].Priority != llm.MessagePriorityHistory {
					t.Fatal("runner mutated the caller's priorities")
				}
				if len(client.requests) != 2 || tool.calls != 1 {
					t.Fatalf("requests=%d tool_calls=%d", len(client.requests), tool.calls)
				}
				for _, request := range client.requests {
					originalFound := false
					for _, message := range request.Messages {
						if message.Content == original {
							originalFound = message.Priority == llm.MessagePriorityCurrent
						}
						if message.Content == history && message.Priority != llm.MessagePriorityHistory {
							t.Fatal("old history was promoted to current")
						}
					}
					if !originalFound {
						t.Fatal("continuation lost the original current requirement")
					}
				}
				if tooSmall {
					if err == nil || !strings.Contains(err.Error(), "input budget") || httpCalls.Load() != 0 {
						t.Fatalf("want local capacity error before HTTP: err=%v http_calls=%d", err, httpCalls.Load())
					}
					return
				}
				if err != nil || response == nil || response.Text != "done" {
					t.Fatalf("response=%+v err=%v", response, err)
				}
				if httpCalls.Load() != 1 {
					t.Fatalf("http_calls=%d", httpCalls.Load())
				}
				body := <-bodies
				if !strings.Contains(body, original) || strings.Contains(body, history) {
					t.Fatal("provider payload changed the current task or retained oversized optional history")
				}
			})
		}
	}
}

func TestRunnerRespectsExplicitCurrentRequirementBoundary(t *testing.T) {
	client := &scriptedClient{responses: []string{`{"action":"final","content":"done"}`}}
	runner, err := NewRunner(client, Config{MaxSteps: 1}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "explicit current", Priority: llm.MessagePriorityCurrent},
		{Role: llm.RoleUser, Content: "caller-owned supporting context", Priority: llm.MessagePrioritySystem},
	}
	if _, err := runner.Run(t.Context(), Request{Messages: messages}); err != nil {
		t.Fatal(err)
	}
	for _, message := range client.requests[0].Messages {
		if message.Content == messages[1].Content && message.Priority != llm.MessagePrioritySystem {
			t.Fatal("runner replaced the caller's explicit current boundary")
		}
	}
}

// 提示词必须把「规划步」和「用户消息」区分开,并写明本条回复内的工具预算。
// 曾经的措辞「每轮最多调用一个工具」被模型理解成「用户每发一条消息只能调
// 一次」,多步任务每调一次就收口,让用户发「继续」才肯继续。
func TestSystemPromptStatesPerReplyToolBudget(t *testing.T) {
	runner, err := NewRunner(&scriptedClient{}, Config{MaxSteps: 8}, NewToolRegistry(&countingTool{name: "lookup"}))
	if err != nil {
		t.Fatal(err)
	}
	prompt := runner.systemPrompt()
	if !strings.Contains(prompt, "8 次") {
		t.Fatalf("提示词应写明本条回复内的工具预算,实际:%s", prompt)
	}
	if !strings.Contains(prompt, "不要停下来向用户要求「继续」") {
		t.Fatalf("提示词应明确禁止中途向用户要「继续」,实际:%s", prompt)
	}
	if strings.Contains(prompt, "每轮最多调用一个工具") {
		t.Fatalf("会被误读的旧措辞不该再出现:%s", prompt)
	}
}

func TestSystemPromptExplicitlyTriggersParallelSubtasks(t *testing.T) {
	runner, err := NewRunner(&scriptedClient{}, Config{MaxSteps: 8}, NewToolRegistry(
		&countingTool{name: "web_search"},
		&countingTool{name: "subtask"},
	))
	if err != nil {
		t.Fatal(err)
	}
	prompt := runner.systemPrompt()
	for _, want := range []string{"两个及以上互不依赖", "一次调用 subtask", "tasks", "mode=search", "不要由主 Agent 按方向连续串行调用 web_search"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("subtask trigger rule missing %q: %s", want, prompt)
		}
	}

	withoutSubtask, err := NewRunner(&scriptedClient{}, Config{MaxSteps: 8}, NewToolRegistry(&countingTool{name: "web_search"}))
	if err != nil {
		t.Fatal(err)
	}
	if prompt := withoutSubtask.systemPrompt(); strings.Contains(prompt, "一次调用 subtask") {
		t.Fatalf("subtask trigger rule leaked into a registry without the tool: %s", prompt)
	}
}

type parallelToolClient struct {
	requests []llm.GenerateRequest
}

func (c *parallelToolClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if len(c.requests) == 1 {
		return &llm.GenerateResponse{
			ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: "lookup", Arguments: map[string]any{"query": "one"}},
				{ID: "call-2", Name: "lookup", Arguments: map[string]any{"query": "two"}},
				{ID: "call-3", Name: "lookup", Arguments: map[string]any{"query": "three"}},
			},
		}, nil
	}
	return &llm.GenerateResponse{Text: `{"action":"final","content":"done"}`}, nil
}

// 并行工具调用当前只执行第一个,其余被丢弃——这必须在观察消息里明说,
// 否则模型以为剩下的执行过了,或者得出「一次只能调一个」的错误经验。
func TestParallelToolCallsDropIsReportedToModel(t *testing.T) {
	client := &parallelToolClient{}
	tool := &countingTool{name: "lookup"}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查三个"}}}); err != nil {
		t.Fatal(err)
	}
	if tool.calls != 1 {
		t.Fatalf("当前实现应只执行第一个调用,实际 %d", tool.calls)
	}
	notified := false
	for _, req := range client.requests[1:] {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "并行请求了 3 个工具调用") && strings.Contains(msg.Content, "没有执行") {
				notified = true
			}
		}
	}
	if !notified {
		t.Fatal("被丢弃的并行调用没有告知模型")
	}
	results := map[string]llm.Message{}
	for _, msg := range client.requests[1].Messages {
		if msg.Role == llm.RoleTool {
			results[msg.ToolCallID] = msg
		}
	}
	if len(results) != 3 || results["call-1"].ToolError || !results["call-2"].ToolError || !results["call-3"].ToolError {
		t.Fatalf("tool continuation must close every original call: %+v", results)
	}
}

// 编码规则只随已启用的工具变化；聊天/搜索会话不承担编码验证要求。
func TestCodingRulesFollowAvailableTools(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools []Tool
		want  bool
	}{
		{"coding", []Tool{&countingTool{name: "run_command"}, &countingTool{name: "write_file"}}, true},
		{"edit", []Tool{&countingTool{name: "run_command"}, &countingTool{name: "edit_file"}}, true},
		{"read_only", []Tool{&countingTool{name: "run_command"}}, false},
		{"write_only", []Tool{&countingTool{name: "write_file"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &Runner{cfg: Config{}.WithDefaults(), registry: NewToolRegistry(tc.tools...)}
			if got := strings.Contains(runner.systemPrompt(), "保留原失败用例"); got != tc.want {
				t.Fatalf("coding validation rule present = %v, want %v", got, tc.want)
			}
		})
	}
}
