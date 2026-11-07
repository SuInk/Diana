// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"reflect"
	"testing"
)

func historyReplyPair(id, text string) []Message {
	callID := HistoryReplyCallIDPrefix + id
	return []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: callID, Name: "send", Arguments: map[string]any{"text": text}}}, Priority: MessagePriorityHistory},
		{Role: RoleTool, ToolCallID: callID, ToolName: "send", Content: "已发送", Priority: MessagePriorityHistory},
	}
}

// 不带工具定义的请求里，历史发言压回 assistant 纯文本，「已发送」结果丢掉，结果上的
// 缓存断点挪到发言上；真实的工具调用不动。
func TestWithDefaultsFlattensHistoryRepliesWithoutTools(t *testing.T) {
	pair := historyReplyPair("a", "早上好")
	pair[1].CacheBreakpoint = true
	real := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "web_search"}}},
		{Role: RoleTool, ToolCallID: "call_1", ToolName: "web_search", Content: "结果"},
	}
	messages := append(append(append([]Message{{Role: RoleUser, Content: "[历史] 甲: 早"}}, pair...), real...), Message{Role: RoleUser, Content: "现在呢"})
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "m"}

	flat := GenerateRequest{Messages: messages}.withDefaults(cfg).Messages
	want := []Message{
		{Role: RoleUser, Content: "[历史] 甲: 早"},
		{Role: RoleAssistant, Content: "早上好", Priority: MessagePriorityHistory, CacheBreakpoint: true},
		real[0], real[1],
		{Role: RoleUser, Content: "现在呢"},
	}
	if !reflect.DeepEqual(flat, want) {
		t.Fatalf("flattened:\n got %#v\nwant %#v", flat, want)
	}

	kept := GenerateRequest{Messages: messages, Tools: []ToolDefinition{{Name: "send"}}}.withDefaults(cfg).Messages
	if !reflect.DeepEqual(kept, messages) {
		t.Fatalf("requests with tools must keep history replies as calls: %#v", kept)
	}
}

func TestIsHistoryReplyMessageNeedsEveryCallPrefixed(t *testing.T) {
	pair := historyReplyPair("a", "好")
	mixed := pair[0]
	mixed.ToolCalls = append(mixed.ToolCalls, ToolCall{ID: "call_1", Name: "send"})
	if IsHistoryReplyMessage(mixed) || IsHistoryReplyMessage(Message{Role: RoleAssistant, Content: "好"}) {
		t.Fatal("only all-history calls count")
	}
	if HistoryReplyText(pair[0]) != "好" {
		t.Fatalf("text = %q", HistoryReplyText(pair[0]))
	}
}
