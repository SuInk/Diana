// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

const appendTestRule = "这是一条逐轮都不变的尾部说明，续接时照样整段再发一遍。"

func appendTestTurn(history []string, tailExtra, current string) []llm.Message {
	messages := []llm.Message{{Role: llm.RoleSystem, Content: "人设头部"}}
	for _, line := range history {
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: line})
	}
	messages[len(messages)-1].CacheBreakpoint = true
	messages = append(messages,
		llm.Message{Role: llm.RoleSystem, Content: appendTestRule + "\n" + tailExtra},
		llm.Message{Role: llm.RoleUser, Content: current})
	return messages
}

func appendTestLog(t *testing.T, messages []llm.Message, reply string) *appendContextLog {
	t.Helper()
	transcript := append(append([]llm.Message(nil), messages...),
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: agent.FinalizeToolName, Arguments: map[string]any{"text": reply}}}},
		llm.Message{Role: llm.RoleTool, ToolCallID: "c1", ToolName: agent.FinalizeToolName, Content: "已发送"})
	log := newAppendContextLog(&agent.Response{Transcript: transcript, ConversationKey: "k"}, appendContextHeadKey(messages))
	if log == nil {
		t.Fatal("log not created")
	}
	return log
}

func TestAppendContextAppendsNewHistoryAndWholeTail(t *testing.T) {
	first := appendTestTurn([]string{"[历史 10:00] 甲: 早", "[历史 10:01] 乙: 早啊"}, "当前发言者：甲，这一行每轮都会变化，长度也够。", "【当前需要回复的消息】甲: 在吗")
	log := appendTestLog(t, first, "在的")
	second := appendTestTurn([]string{"[历史 10:01] 乙: 早啊", "[历史 10:02] 甲: 在吗", "[历史 10:03] 丙: 吃了吗"}, "当前发言者：丙，这一行每轮都会变化，长度也够。", "【当前需要回复的消息】丙: 吃了吗")
	second = append(second[:3], append([]llm.Message{{Role: llm.RoleAssistant, Content: "在的"}}, second[3:]...)...)
	continuation := appendContextContinuation(log, second)
	if continuation == nil {
		t.Fatal("expected a continuation")
	}
	var got []string
	for _, message := range continuation.Append {
		got = append(got, string(message.Role)+":"+message.Content)
	}
	joined := strings.Join(got, "|")
	for _, want := range []string{"[历史 10:02] 甲: 在吗", "[历史 10:03] 丙: 吃了吗", appendTestRule + "\n当前发言者：丙"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	for _, unwanted := range []string{"乙: 早啊", "assistant:在的", "人设头部"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("%q should not be appended again: %q", unwanted, joined)
		}
	}
	if last := continuation.Append[len(continuation.Append)-1]; last.Content != "【当前需要回复的消息】丙: 吃了吗" || last.CacheBreakpoint {
		t.Fatalf("current message must stay last: %#v", last)
	}
}

func TestAppendContextRestartsWhenHeadChangesOrGrowsTooLong(t *testing.T) {
	first := appendTestTurn([]string{"[历史 10:00] 甲: 早"}, "", "甲: 在吗")
	log := appendTestLog(t, first, "在的")
	changed := appendTestTurn([]string{"[历史 10:00] 甲: 早"}, "", "甲: 还在吗")
	changed[0].Content = "换了人设"
	if appendContextContinuation(log, changed) != nil {
		t.Fatal("a changed head must restart")
	}
	long := appendTestTurn([]string{"[历史 10:00] 甲: 早", "[历史 10:05] 甲: " + strings.Repeat("很长的一段话。", 400)}, "", "甲: 还在吗")
	log.prefix = append(log.prefix, llm.Message{Role: llm.RoleUser, Content: strings.Repeat("旧内容。", 3000)})
	if appendContextContinuation(log, long) != nil {
		t.Fatal("a prefix past the growth limit must restart")
	}
}

// 整轮走两遍：第二次主回复请求必须以第一次实际发出的请求加上那次的 send 开头。
func TestReplyTurnsContinueAppendOnly(t *testing.T) {
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{
		`tool_calls:[{"id":"s1","name":"send","arguments":{"text":"在的"}}]`,
		`tool_calls:[{"id":"s2","name":"send","arguments":{"text":"吃过了"}}]`,
	}}
	runtime := NewRuntime(BotConfig{AgentEnabled: true, BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	first := interimTestEvent()
	first.MessageID, first.RawMessage = "append-1", "在吗"
	first.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "在吗"}}}
	if _, err := runtime.replyTo(t.Context(), first, first.RawMessage); err != nil {
		t.Fatal(err)
	}
	second := interimTestEvent()
	second.MessageID, second.RawMessage, second.Time = "append-2", "吃了吗", first.Time+30
	second.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "吃了吗"}}}
	if _, err := runtime.replyTo(t.Context(), second, second.RawMessage); err != nil {
		t.Fatal(err)
	}
	var replies []llm.GenerateRequest
	for _, request := range provider.requestsSnapshot() {
		if len(request.Tools) > 0 {
			replies = append(replies, request)
		}
	}
	if len(replies) != 2 {
		t.Fatalf("reply requests = %d, want 2", len(replies))
	}
	before, after := replies[0].Messages, replies[1].Messages
	if len(after) <= len(before)+2 {
		t.Fatalf("second request did not extend the first: %d vs %d", len(after), len(before))
	}
	for index, message := range before {
		got, want := after[index], message
		got.CacheBreakpoint, want.CacheBreakpoint = false, false
		if got.Role != want.Role || appendContextText(got) != appendContextText(want) {
			t.Fatalf("prefix differs at %d:\n got %s: %.200q\nwant %s: %.200q", index, got.Role, appendContextText(got), want.Role, appendContextText(want))
		}
	}
	if call := after[len(before)]; call.Role != llm.RoleAssistant || len(call.ToolCalls) != 1 || call.ToolCalls[0].ID != "s1" {
		t.Fatalf("first reply missing after the prefix: %#v", call)
	}
	if !strings.Contains(appendContextText(after[len(after)-1]), "吃了吗") {
		t.Fatalf("current message is not last: %q", appendContextText(after[len(after)-1]))
	}
}
