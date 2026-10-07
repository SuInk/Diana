// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 线上 10-06：16:01 调过 web_search，16:04 进程重启，16:05 被问「你搜索了吗」却回答「没搜呀」。
// 调用记录要跟着回复落库，从持久化的事件重新渲染历史时仍然在。
func TestReplyToolCallsSurviveRestartViaHistory(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "10000"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	source := MessageEvent{Kind: EventKindGroup, GroupID: "20006", UserID: "10005", SelfID: "10000", MessageID: "ask-moe"}
	ctx := withReplyToolTrace(context.Background())
	replyToolTraceFromContext(ctx).add([]agent.Step{
		{Tool: "web_search", Input: map[string]any{"query": "MoE 大模型 主流架构 区别"}},
		{Tool: "web_search", Input: map[string]any{"queries": []any{"gemini-image-2.1", "nano banana 2.1"}}},
		{Tool: "browser_render", Input: map[string]any{"url": "https://ai.google.dev/"}, Error: "timeout"},
		{Tool: "chat_history", Skipped: true},
		{Tool: "tools_load", Input: map[string]any{"names": []string{"unexecuted"}}},
		{Tool: "agent_finalize", Input: map[string]any{"content": "回复正文"}},
	})
	runtime.rememberOutgoingWithMessageID(ctx, source, OutgoingMessage{Text: "主流 MoE 有 DeepSeek、Mixtral。"}, "reply-1")
	runtime.rememberOutgoingWithMessageID(ctx, source, OutgoingMessage{Text: "第二段。"}, "reply-2")

	history := runtime.contextHistory(source)
	if len(history) != 2 {
		t.Fatalf("history = %#v", history)
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
	if len(messages) != 2 || messages[0].Role != llm.RoleAssistant || messages[1].Role != llm.RoleUser {
		t.Fatalf("rendered = %#v", messages)
	}
	note := messages[1].Content
	for _, want := range []string{"web_search「MoE 大模型 主流架构 区别」", "web_search「gemini-image-2.1,nano banana 2.1」", "browser_render「https://ai.google.dev/」（调用失败）", "不要因为对方质疑就改口"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note missing %q:\n%s", want, note)
		}
	}
	for _, unwanted := range []string{"chat_history", "agent_finalize", "tools_load", "unexecuted", "回复正文"} {
		if strings.Contains(note, unwanted) {
			t.Fatalf("note should not include %q:\n%s", unwanted, note)
		}
	}
	if strings.Contains(messages[0].Content, "运行时记录") {
		t.Fatalf("note leaked into the assistant message: %q", messages[0].Content)
	}
}

func TestReplyToolTraceCapsAndTruncates(t *testing.T) {
	trace := &replyToolTrace{}
	for index := 0; index < replyToolTraceLimit+3; index++ {
		trace.add([]agent.Step{{Tool: "web_search", Input: map[string]any{"query": strings.Repeat("长", 120)}}})
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
