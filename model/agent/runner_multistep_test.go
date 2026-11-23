// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

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
	for _, want := range []string{"调研默认交给 subtask（mode=search）", "一次 tasks 并行", "只有一个方向也派一个 Worker", "才自己 web_search"} {
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

// 一步里的多个调用按顺序全部执行，每个调用配自己的结果，和主流 agent 一致。
// 以前只执行第一个，其余让模型下一步重发，读连续几页要一页一轮。
func TestStepRunsEveryToolCallInOrder(t *testing.T) {
	client := &parallelToolClient{}
	tool := &countingTool{name: "lookup"}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	response, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查三个"}}})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 3 || len(client.requests) != 2 || len(response.Steps) != 3 {
		t.Fatalf("calls=%d requests=%d steps=%d", tool.calls, len(client.requests), len(response.Steps))
	}
	for index, query := range []string{"one", "two", "three"} {
		if response.Steps[index].Input["query"] != query {
			t.Fatalf("steps out of order: %+v", response.Steps)
		}
	}
	results := stepToolResults(client.requests[1])
	if len(results) != 3 || results["call-1"].ToolError || results["call-2"].ToolError || results["call-3"].ToolError {
		t.Fatalf("every call must get its own result: %+v", results)
	}
	if !strings.Contains(results["call-3"].Content, "剩余工具预算 1") {
		t.Fatalf("budget not counted per call: %s", results["call-3"].Content)
	}
}

func stepToolResults(req llm.GenerateRequest) map[string]llm.Message {
	results := map[string]llm.Message{}
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleTool {
			results[msg.ToolCallID] = msg
		}
	}
	return results
}

type scriptedCallsClient struct {
	calls    []llm.ToolCall
	requests []llm.GenerateRequest
}

func (c *scriptedCallsClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if len(c.requests) == 1 {
		return &llm.GenerateResponse{ToolCalls: c.calls}, nil
	}
	return &llm.GenerateResponse{Text: `{"action":"final","content":"done"}`}, nil
}

// 预算不够时执行到用完为止；后面的调用被拒（这里是连续重复）就停在那里，它和它之后的
// 都标成未执行；同一步的 send 不发。每个原始调用都有一条结果。
func TestStepStopsAtBudgetAndRejectedCalls(t *testing.T) {
	call := func(id, query string) llm.ToolCall {
		return llm.ToolCall{ID: id, Name: "lookup", Arguments: map[string]any{"query": query}}
	}
	for _, tc := range []struct {
		name     string
		maxSteps int
		calls    []llm.ToolCall
		executed int
		failed   map[string]string
	}{
		{"budget", 2, []llm.ToolCall{call("a", "one"), call("b", "two"), call("c", "three")}, 2, map[string]string{"c": "步数已用完"}},
		{"rejected", 8, []llm.ToolCall{call("a", "one"), call("b", "one"), call("c", "two")}, 1, map[string]string{"b": "连续重复", "c": "排在前面的调用被拒绝"}},
		{"send", 8, []llm.ToolCall{call("a", "one"), {ID: "s", Name: finalizeToolName, Arguments: map[string]any{"text": "好了"}}, call("b", "two")}, 2, map[string]string{"s": "send 没有发出"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &scriptedCallsClient{calls: tc.calls}
			tool := &countingTool{name: "lookup"}
			runner, err := NewRunner(client, Config{MaxSteps: tc.maxSteps}, NewToolRegistry(tool))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查"}}}); err != nil {
				t.Fatal(err)
			}
			if tool.calls != tc.executed {
				t.Fatalf("executed %d, want %d", tool.calls, tc.executed)
			}
			results := stepToolResults(client.requests[1])
			if len(results) != len(tc.calls) {
				t.Fatalf("results=%+v", results)
			}
			for id, result := range results {
				want, failed := tc.failed[id]
				if result.ToolError != failed || !strings.Contains(result.Content, want) {
					t.Fatalf("%s: %+v", id, result)
				}
			}
		})
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

func TestDefaultRunnerContinuesBeyondFormerStepLimit(t *testing.T) {
	client := &scriptedClient{}
	for i := 0; i < 40; i++ {
		client.responses = append(client.responses, fmt.Sprintf(`{"action":"tool","tool":"lookup","input":{"query":"item-%d"}}`, i))
	}
	tool := &countingTool{name: "lookup"}
	runner, err := NewRunner(client, Config{}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	response, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "完成全部查询"}}})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 40 || response.FinishReason != "final" {
		t.Fatalf("calls=%d response=%+v", tool.calls, response)
	}
	for _, req := range client.requests {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "剩余工具预算") {
				t.Fatal("unlimited run received finite budget guidance")
			}
		}
	}
}
