// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestFinishHookContinuesWithoutRepeatingCompletedTool(t *testing.T) {
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"lookup","input":{}}`,
		`{"action":"final","content":"未经核实的结论"}`,
		`{"action":"final","content":"仅说明查到的内容"}`,
	}}
	tool := &countingTool{name: "lookup"}
	runner, _ := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(tool))
	checks := 0
	resp, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "查资料"}},
		Hooks: RunHooks{BeforeFinish: func(_ context.Context, candidate FinishCandidate) (FinishDecision, error) {
			checks++
			if len(candidate.Steps) != 1 || candidate.Steps[0].Error != "" {
				t.Fatalf("missing execution evidence: %+v", candidate)
			}
			if checks == 1 {
				return FinishDecision{Feedback: "已有查询不支持这个结论，请修正"}, nil
			}
			return FinishDecision{}, nil
		}},
	})
	if err != nil || resp.Text != "仅说明查到的内容" || tool.calls != 1 || checks != 2 {
		t.Fatalf("response=%+v err=%v executions=%d reviews=%d", resp, err, tool.calls, checks)
	}
	last := client.requests[len(client.requests)-1]
	if !strings.Contains(last.Messages[len(last.Messages)-1].Content, "已有查询不支持") {
		t.Fatal("missing actionable feedback")
	}
}

func TestFinishHookRejectsAllOutputRoutes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses []string
		tool      Tool
		steps     int
	}{
		{"plain", []string{"无依据的资料", "仍无依据", "还是无依据"}, nil, 4},
		{"silent_content", []string{`{"action":"final","silent":true,"content":"假称已发"}`}, nil, 4},
		{"terminal", []string{`{"action":"tool","tool":"terminal","input":{}}`}, &terminalTestTool{}, 4},
		{"budget", []string{`{"action":"tool","tool":"lookup","input":{}}`, "预算耗尽后也不可信"}, &countingTool{name: "lookup"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &scriptedClient{responses: tc.responses}
			registry := NewToolRegistry()
			if tc.tool != nil {
				registry.Register(tc.tool)
			}
			runner, _ := NewRunner(client, Config{MaxSteps: tc.steps}, registry)
			checks := 0
			resp, err := runner.Run(context.Background(), Request{
				Messages: []llm.Message{{Role: llm.RoleUser, Content: "查资料"}},
				Hooks: RunHooks{BeforeFinish: func(_ context.Context, candidate FinishCandidate) (FinishDecision, error) {
					checks++
					return FinishDecision{Feedback: "没有证据"}, nil
				}},
			})
			if resp != nil || !errors.Is(err, ErrFinishRejected) || checks > 3 {
				t.Fatalf("response=%+v error=%v checks=%d", resp, err, checks)
			}
		})
	}
}

func TestToolHookBlocksNativeCallAndPreservesFailure(t *testing.T) {
	tool := &countingTool{name: "lookup"}
	client := &batchClient{first: []llm.ToolCall{{ID: "blocked", Name: "lookup", Arguments: map[string]any{}}}}
	runner, _ := NewRunner(client, Config{}, NewToolRegistry(tool))
	var observed []Step
	var completed RunEvent
	_, err := runner.Run(context.Background(), Request{
		Observer: func(_ context.Context, event RunEvent) {
			if event.Phase == RunPhaseCompleted {
				completed = event
			}
		},
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "查询"}},
		Hooks: RunHooks{
			BeforeToolCall: func(context.Context, ToolCallContext) error { return errors.New("目标群未核验") },
			AfterToolCall:  func(_ context.Context, s Step) { observed = append(observed, s) },
		},
	})
	if err != nil || tool.calls != 0 || len(observed) != 1 || !observed[0].Skipped || observed[0].Error == "" {
		t.Fatalf("error=%v calls=%d observed=%+v", err, tool.calls, observed)
	}
	if completed.ToolsExecuted != 0 {
		t.Fatalf("blocked tool counted as executed: %+v", completed)
	}
	results := toolResults(client.requests[1])
	if len(results) != 1 || !results[0].ToolError {
		t.Fatalf("failure lost: %+v", results)
	}
}

func TestPrepareHookRunsAtBudgetFinalizationWithoutAccumulatingContext(t *testing.T) {
	client := &scriptedClient{responses: []string{`{"action":"tool","tool":"lookup","input":{}}`, "done"}}
	runner, _ := NewRunner(client, Config{MaxSteps: 1}, NewToolRegistry(&countingTool{name: "lookup"}))
	prepared := 0
	_, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "query"}},
		Hooks: RunHooks{PrepareRequest: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateRequest, error) {
			prepared++
			for _, m := range req.Messages {
				if m.Content == "temporary state" {
					t.Fatal("temporary context leaked into canonical history")
				}
			}
			req.Messages = append(req.Messages, llm.Message{Role: llm.RoleSystem, Content: "temporary state"})
			return req, nil
		}},
	})
	if err != nil || prepared != 2 {
		t.Fatalf("error=%v preparations=%d", err, prepared)
	}
}

func TestParallelToolHooksObserveBlockedAndSuccessfulCalls(t *testing.T) {
	var active, peak atomic.Int32
	tool := &slowReadTool{name: WebSearchToolName, active: &active, peak: &peak}
	client := &batchClient{first: []llm.ToolCall{
		{ID: "one", Name: WebSearchToolName, Arguments: map[string]any{"query": "allowed"}},
		{ID: "two", Name: WebSearchToolName, Arguments: map[string]any{"query": "blocked"}},
	}}
	runner, _ := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(tool))
	var observed []Step // No host-side mutex: hook serialization is part of the API.
	_, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查询"}}, Hooks: RunHooks{
		BeforeToolCall: func(_ context.Context, c ToolCallContext) error {
			if c.Input["query"] == "blocked" {
				return errors.New("blocked by host")
			}
			return nil
		},
		AfterToolCall: func(_ context.Context, s Step) { observed = append(observed, s) },
	}})
	if err != nil || len(tool.order) != 1 || tool.order[0] != "allowed" || len(observed) != 2 {
		t.Fatalf("err=%v executions=%v observations=%+v", err, tool.order, observed)
	}
	results := toolResults(client.requests[1])
	if len(results) != 2 || results[0].ToolError || !results[1].ToolError {
		t.Fatalf("results=%+v", results)
	}
}

func TestHookErrorsFailClosed(t *testing.T) {
	for _, stage := range []string{"prepare", "finish"} {
		t.Run(stage, func(t *testing.T) {
			client := &scriptedClient{responses: []string{"candidate"}}
			runner, _ := NewRunner(client, Config{}, NewToolRegistry())
			hooks := RunHooks{}
			if stage == "prepare" {
				hooks.PrepareRequest = func(context.Context, llm.GenerateRequest) (llm.GenerateRequest, error) {
					return llm.GenerateRequest{}, errors.New("host unavailable")
				}
			} else {
				hooks.BeforeFinish = func(context.Context, FinishCandidate) (FinishDecision, error) {
					return FinishDecision{}, errors.New("host unavailable")
				}
			}
			resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "query"}}, Hooks: hooks})
			if err == nil || resp != nil {
				t.Fatalf("accepted after hook failed: response=%+v error=%v", resp, err)
			}
			if stage == "prepare" && len(client.requests) != 0 {
				t.Fatal("called provider after preparation failed")
			}
		})
	}
}

func TestNativeToolFailureIsMarkedWithoutHooks(t *testing.T) {
	client := &batchClient{first: []llm.ToolCall{{ID: "failure", Name: "error"}}}
	runner, _ := NewRunner(client, Config{}, NewToolRegistry(&errorTestTool{message: "failed"}))
	_, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "query"}}})
	if err != nil {
		t.Fatal(err)
	}
	results := toolResults(client.requests[1])
	if len(results) != 1 || !results[0].ToolError {
		t.Fatalf("failure encoded as success: %+v", results)
	}
}
