// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

func TestAgentRunObserverWritesCorrelatedLifecycleLogs(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := &Runtime{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "20001",
		UserID:    "10001",
		MessageID: "30001",
	}
	observe := runtime.agentRunObserver(event)
	observe(context.Background(), agent.RunEvent{
		TraceID:      "trace-1",
		Phase:        agent.RunPhaseToolStarted,
		ModelTurn:    2,
		ToolCall:     1,
		MaxToolCalls: 8,
		Tool:         "demo.tool",
		InputKeys:    []string{"query"},
	})
	observe(context.Background(), agent.RunEvent{
		TraceID:      "trace-1",
		Phase:        agent.RunPhaseCompleted,
		ModelTurn:    3,
		ToolCall:     1,
		MaxToolCalls: 8,
		DurationMS:   42,
		FinishReason: "final",
	})

	if len(logs.entries) != 2 {
		t.Fatalf("entries = %#v", logs.entries)
	}
	started := logs.entries[0]
	if started.Action != "chatbot.agent_tool" || started.Message != "Agent 工具调用开始 [#-------] 1/8" || started.Target != "demo.tool" {
		t.Fatalf("tool log = %#v", started)
	}
	if started.Metadata["trace_id"] != "trace-1" || started.Metadata["message_id"] != "30001" || started.Metadata["tool_call"] != 1 || started.Metadata["progress_percent"] != 12 {
		t.Fatalf("tool metadata = %#v", started.Metadata)
	}
	completed := logs.entries[1]
	if completed.Action != "chatbot.agent_run" || completed.Message != "Agent 运行完成 [########] done" || completed.Metadata["finish_reason"] != "final" || completed.Metadata["progress_percent"] != 100 {
		t.Fatalf("completed log = %#v", completed)
	}
}

func TestAgentRunObserverKeepsWebSearchOperationLogsPrivate(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := &Runtime{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "private-group", UserID: "private-user", MessageID: "private-message"}
	runtime.agentRunObserver(event)(context.Background(), agent.RunEvent{
		TraceID:   "trace-search",
		Phase:     agent.RunPhaseToolCompleted,
		Tool:      agent.WebSearchToolName,
		ToolInput: map[string]any{"query": "private raw query"},
		Metadata: map[string]any{
			"status":   "no_results",
			"strategy": "bounded_query_exploration",
			"queries":  []map[string]any{{"hash": "abc123", "length": 17, "language": "en"}},
		},
	})
	entries := logs.entriesSnapshot()
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	entry := entries[0]
	if entry.Actor != "" || entry.Target != agent.WebSearchToolName {
		t.Fatalf("entry identity = %#v", entry)
	}
	for _, key := range []string{"group_id", "user_id", "message_id"} {
		if _, exists := entry.Metadata[key]; exists {
			t.Fatalf("metadata retained %s: %#v", key, entry.Metadata)
		}
	}
	body, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, private := range []string{"private-group", "private-user", "private-message", "private raw query"} {
		if strings.Contains(text, private) {
			t.Fatalf("operation log leaked %q: %s", private, text)
		}
	}
	if !strings.Contains(text, "abc123") || !strings.Contains(text, "bounded_query_exploration") {
		t.Fatalf("safe search diagnostics missing: %s", text)
	}
}

func TestAgentRunObserverRedactsOneBotV11DebugPayload(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{DebugModeEnabled: true}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner", MessageID: "message-1"}
	ctx := runtime.withDebugTraceContext(context.Background(), event)
	runtime.agentRunObserver(event)(ctx, agent.RunEvent{
		Phase:      agent.RunPhaseToolCompleted,
		Tool:       dianaOneBotV11ToolName,
		InputKeys:  []string{"action", "params"},
		ToolInput:  map[string]any{"action": "get_credentials", "params": map[string]any{"domain": "secret.example"}},
		ToolOutput: `{"ok":true,"data":{"cookies":"owner-secret"}}`,
		Error:      "adapter rejected owner-secret for secret.example",
	})
	entries := logs.entriesSnapshot()
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	debug := entries[1]
	if strings.Contains(entries[0].Detail, "owner-secret") || strings.Contains(entries[0].Detail, "secret.example") {
		t.Fatalf("operation log leaked OneBot error: %#v", entries[0])
	}
	input, _ := debug.Metadata["tool_input"].(map[string]any)
	if input["action"] != "get_credentials" || strings.Contains(debug.Metadata["tool_output"].(string), "owner-secret") {
		t.Fatalf("debug payload = %#v", debug.Metadata)
	}
	if strings.Contains(strings.TrimSpace(debug.Metadata["tool_output"].(string)), "secret.example") {
		t.Fatalf("debug payload leaked parameter value: %#v", debug.Metadata)
	}
	if strings.Contains(debug.Metadata["error"].(string), "owner-secret") || strings.Contains(debug.Metadata["error"].(string), "secret.example") {
		t.Fatalf("debug payload leaked OneBot error: %#v", debug.Metadata)
	}
}

func TestAgentRunObserverRedactsRepositoryIssueDebugPayload(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{DebugModeEnabled: true}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner", MessageID: "message-2"}
	ctx := runtime.withDebugTraceContext(context.Background(), event)
	runtime.agentRunObserver(event)(ctx, agent.RunEvent{
		Phase:      agent.RunPhaseToolCompleted,
		Tool:       dianaRepositoryIssuesToolName,
		ToolInput:  map[string]any{"operation": "create", "repository": "acme/demo", "title": "private title", "body": "token=owner-secret"},
		ToolOutput: `{"ok":true,"issue":{"number":12,"title":"private title"}}`,
		Error:      "GitHub rejected owner-secret",
	})
	entries := logs.entriesSnapshot()
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	operation, debug := entries[0], entries[1]
	for _, entry := range entries {
		encoded := fmt.Sprintf("%#v", entry)
		for _, secret := range []string{"private title", "owner-secret"} {
			if strings.Contains(encoded, secret) {
				t.Fatalf("repository issue log leaked %q: %#v", secret, entry)
			}
		}
	}
	if operation.Detail != "[repository issue tool error omitted]" {
		t.Fatalf("operation detail = %q", operation.Detail)
	}
	input, _ := debug.Metadata["tool_input"].(map[string]any)
	if input["operation"] != "create" || input["repository"] != "acme/demo" {
		t.Fatalf("debug input = %#v", input)
	}
	if debug.Metadata["tool_output"] != "[repository issue tool output omitted]" || debug.Metadata["error"] != "[repository issue tool error omitted]" {
		t.Fatalf("debug metadata = %#v", debug.Metadata)
	}
}
