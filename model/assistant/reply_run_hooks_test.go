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

func TestReplyRunHooksUseActualExecutionAndKeepPrivateInputsOut(t *testing.T) {
	hooks := newReplyRunHooks()
	ctx := context.Background()
	request := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "stable", CacheBreakpoint: true}, {Role: llm.RoleUser, Content: "current"}}}
	first, err := hooks.PrepareRequest(ctx, request)
	if err != nil || !strings.Contains(first.Messages[1].Content, `"total":0`) {
		t.Fatalf("request=%+v err=%v", first, err)
	}
	hooks.AfterToolCall(ctx, agent.Step{Tool: "chat_history", Input: map[string]any{"scope": "current", "group_id": "group-1", "token": "secret", "query": "private question"}, Error: "denied", Skipped: true})
	second, err := hooks.PrepareRequest(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	note := second.Messages[1].Content
	if !strings.Contains(note, `"status":"not_executed"`) || !strings.Contains(note, `"group_id":"group-1"`) || strings.Contains(note, "secret") || strings.Contains(note, "private question") {
		t.Fatalf("bad ledger: %s", note)
	}
	if len(request.Messages) != 2 || request.Messages[1].Content != "current" || second.Messages[len(second.Messages)-1].Content != "current" || !second.Messages[0].CacheBreakpoint {
		t.Fatal("changed canonical history or cache prefix")
	}
	for i := 0; i < 20; i++ {
		hooks.AfterToolCall(ctx, agent.Step{Tool: "lookup"})
	}
	bounded, _ := hooks.PrepareRequest(ctx, request)
	if !strings.Contains(bounded.Messages[1].Content, `"omitted":9`) {
		t.Fatal("unbounded execution context")
	}
	batch := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "one", Name: "lookup"}, {ID: "two", Name: "lookup"}}},
		{Role: llm.RoleTool, ToolCallID: "one"},
		{Role: llm.RoleTool, ToolCallID: "two"},
	}}
	prepared, _ := hooks.PrepareRequest(ctx, batch)
	if prepared.Messages[1].ToolCallID != "one" || prepared.Messages[2].ToolCallID != "two" || prepared.Messages[3].Role != llm.RoleSystem {
		t.Fatal("execution context split a native tool batch")
	}
}
