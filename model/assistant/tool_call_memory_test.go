// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 线上 10-06：16:01 调过 web_search，16:04 进程重启，16:05 被问「你搜索了吗」却回答「没搜呀」。
// 调用记录要跟着回复落库，从持久化的事件重新渲染历史时仍然在：渲染成回复前面的工具调用
// 加占位结果，当时查到的内容不带过来。
func TestReplyToolCallsSurviveRestartViaHistory(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "10000"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	source := MessageEvent{Kind: EventKindGroup, GroupID: "20006", UserID: "10005", SelfID: "10000", MessageID: "ask-moe"}
	ctx := withReplyToolTrace(context.Background())
	executed := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "w1", Name: "web_search", Arguments: map[string]any{"query": "MoE 大模型 主流架构 区别"}},
			{ID: "i1", Name: agent.ToolsExecuteToolName, Arguments: map[string]any{"name": "image", "input": map[string]any{"operation": "generate", "prompt": "一只猫"}}},
			{ID: "b1", Name: "browser_render", Arguments: map[string]any{"url": "https://ai.google.dev/"}},
			{ID: "s1", Name: "send", Arguments: map[string]any{"text": "回复正文"}},
		}},
		{Role: llm.RoleTool, ToolCallID: "w1", Content: "搜索结果正文"},
		{Role: llm.RoleTool, ToolCallID: "i1", Content: "queued"},
		{Role: llm.RoleTool, ToolCallID: "b1", Content: "timeout", ToolError: true},
		{Role: llm.RoleTool, ToolCallID: "s1", Content: "这次 send 没有发出", ToolError: true},
	}
	replyToolTraceFromContext(ctx).add(executed)
	// 同一轮重来时下一版带着上一版的调用，不能记两遍。
	replyToolTraceFromContext(ctx).add(executed)
	runtime.rememberOutgoingWithMessageID(ctx, source, OutgoingMessage{Text: "主流 MoE 有 DeepSeek、Mixtral。"}, "reply-1")
	runtime.rememberOutgoingWithMessageID(ctx, source, OutgoingMessage{Text: "第二段。"}, "reply-2")

	history := runtime.contextHistory(source)
	if len(history) != 2 {
		t.Fatalf("history = %#v", history)
	}
	if len(history[0].ToolCalls) != 2 || history[0].ToolCalls[1].Tool != "image" || history[0].ToolCalls[1].Summary != "generate" {
		t.Fatalf("tool calls = %#v", history[0].ToolCalls)
	}
	if len(history[1].ToolCalls) != 0 {
		t.Fatalf("tool calls should attach to the first message only: %#v", history[1].ToolCalls)
	}
	// 模拟重启：只留下落库的 JSON。
	payload, err := json.Marshal(history[0])
	if err != nil {
		t.Fatal(err)
	}
	var restored MessageEvent
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatal(err)
	}

	current := MessageEvent{Kind: EventKindGroup, GroupID: "20006", UserID: "10005", SelfID: "10000", MessageID: "ask-searched"}
	messages := runtime.renderPromptHistoryEvent(context.Background(), current, restored, runtime.effectiveConfigForEvent(current), false)
	if len(messages) != 5 || !llm.IsHistoryToolMessage(messages[0]) || !llm.IsHistoryToolMessage(messages[1]) || !llm.IsHistoryToolMessage(messages[2]) || !llm.IsHistoryReplyMessage(messages[3]) || !llm.IsHistoryReplyMessage(messages[4]) {
		t.Fatalf("rendered = %#v", messages)
	}
	calls := messages[0].ToolCalls
	if len(calls) != 2 || calls[0].Name != "web_search" || calls[0].Arguments["query"] != "MoE 大模型 主流架构 区别" || calls[1].Name != agent.ToolsExecuteToolName || calls[1].Arguments["name"] != "image" {
		t.Fatalf("calls = %#v", calls)
	}
	for index, result := range messages[1:3] {
		if result.Role != llm.RoleTool || result.ToolCallID != calls[index].ID || strings.Contains(result.Content, "搜索结果正文") || strings.Contains(result.Content, "queued") {
			t.Fatalf("result %d = %#v", index, result)
		}
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "运行时记录") {
			t.Fatalf("legacy note rendered beside real calls: %q", message.Content)
		}
	}
}

// 11-24 之前落库的记录只有工具名和摘要，没有调用原样，仍渲染成回复后面的旁注。
func TestLegacyReplyToolCallsRenderAsNote(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "10000"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	current := MessageEvent{Kind: EventKindGroup, GroupID: "20006", UserID: "10005", SelfID: "10000", MessageID: "ask"}
	item := MessageEvent{Kind: EventKindGroup, GroupID: "20006", UserID: "10000", SelfID: "10000", MessageID: "old-reply", RawMessage: "查到了", Time: 1, ToolCalls: []ReplyToolCall{
		{Tool: "web_search", Summary: "MoE"},
		{Tool: "browser_render", Summary: "https://ai.google.dev/", Failed: true},
	}}
	messages := runtime.renderPromptHistoryEvent(context.Background(), current, item, runtime.effectiveConfigForEvent(current), false)
	if len(messages) != 3 || !llm.IsHistoryReplyMessage(messages[0]) || messages[2].Role != llm.RoleUser {
		t.Fatalf("rendered = %#v", messages)
	}
	for _, want := range []string{"web_search「MoE」", "browser_render「https://ai.google.dev/」（调用失败）", "不要因为对方质疑就改口说没查"} {
		if !strings.Contains(messages[2].Content, want) {
			t.Fatalf("note missing %q:\n%s", want, messages[2].Content)
		}
	}
}

func TestReplyToolTraceCapsAndTruncates(t *testing.T) {
	trace := &replyToolTrace{}
	for index := 0; index < replyToolTraceLimit+3; index++ {
		id := "w" + strconv.Itoa(index)
		trace.add([]llm.Message{
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "web_search", Arguments: map[string]any{"query": strings.Repeat("长", 120)}}}},
			{Role: llm.RoleTool, ToolCallID: id, Content: "ok"},
		})
	}
	calls := trace.take()
	if len(calls) != replyToolTraceLimit {
		t.Fatalf("kept %d calls, want %d", len(calls), replyToolTraceLimit)
	}
	if strings.Contains(calls[0].Summary, strings.Repeat("长", 81)) {
		t.Fatal("query summary was not truncated")
	}
	if again := trace.take(); len(again) != 0 {
		t.Fatalf("take should drain: %#v", again)
	}
	if _, ok := replyToolTraceNote(nil); ok {
		t.Fatal("no calls should render no note")
	}
}
