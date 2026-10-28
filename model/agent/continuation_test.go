// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func continuationTestMessages(current string) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleSystem, Content: "人设"},
		{Role: llm.RoleUser, Content: "[历史] 甲: 早"},
		{Role: llm.RoleSystem, Content: "本轮尾部"},
		{Role: llm.RoleUser, Content: current},
	}
}

// 第二轮续接时，发出的请求必须以第一轮的记录逐条开头，后面才是新内容，当前消息最后。
func TestRunnerContinuationIsPureAppend(t *testing.T) {
	client := &silentFinalizeClient{arguments: map[string]any{"text": "好"}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Run(context.Background(), Request{Messages: continuationTestMessages("第一问")})
	if err != nil {
		t.Fatal(err)
	}
	if first.Continued || len(first.Transcript) < 2 || first.ConversationKey == "" {
		t.Fatalf("first = %#v", first)
	}
	last := first.Transcript[len(first.Transcript)-1]
	if last.Role != llm.RoleAssistant || last.Content != "好" || len(last.ToolCalls) > 0 {
		t.Fatalf("a send-only finish must be recorded as a plain reply: %#v", last)
	}
	second, err := runner.Run(context.Background(), Request{
		Messages: continuationTestMessages("第二问"),
		Continuation: &Continuation{
			Prefix: first.Transcript,
			Append: []llm.Message{{Role: llm.RoleUser, Content: "[历史] 乙: 在吗"}, {Role: llm.RoleUser, Content: "第二问"}},
			Key:    first.ConversationKey,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Continued {
		t.Fatal("matching key must continue")
	}
	sent := client.requests[1].Messages
	if len(sent) <= len(first.Transcript) {
		t.Fatalf("second request did not grow: %d", len(sent))
	}
	for index, message := range first.Transcript {
		got := sent[index]
		got.CacheBreakpoint = false
		if !reflect.DeepEqual(got, message) {
			t.Fatalf("prefix differs at %d:\n got %#v\nwant %#v", index, got, message)
		}
	}
	if sent[len(sent)-1].Content != "第二问" {
		t.Fatalf("current message is not last: %#v", sent[len(sent)-1])
	}
}

// 指纹对不上（系统提示、契约或工具定义变了）就退回完整组装。
func TestRunnerContinuationFallsBackOnKeyMismatch(t *testing.T) {
	client := &silentFinalizeClient{arguments: map[string]any{"text": "好"}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{
		Messages: continuationTestMessages("问题"),
		Continuation: &Continuation{
			Prefix: []llm.Message{{Role: llm.RoleSystem, Content: "旧的"}},
			Append: []llm.Message{{Role: llm.RoleUser, Content: "问题"}},
			Key:    "stale",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Continued {
		t.Fatal("stale key must not continue")
	}
	for _, message := range client.requests[0].Messages {
		if message.Content == "旧的" {
			t.Fatal("stale prefix leaked into the request")
		}
	}
}

// 只调 send 的收尾记成普通发言：每次 send 一条，不说话的一轮什么都不留。留下 send
// 调用和「已发送」时，模型下一轮会把它读成刚说过，新消息没人接也判「已经回应过」。
func TestTranscriptAfterRecordsSendOnlyFinishAsPlainReplies(t *testing.T) {
	request := llm.GenerateRequest{Messages: continuationTestMessages("问题")}
	cases := []struct {
		name  string
		calls []llm.ToolCall
		want  []string
	}{
		{"one send", []llm.ToolCall{{ID: "s1", Name: finalizeToolName, Arguments: map[string]any{"text": "好"}}}, []string{"好"}},
		{"two sends", []llm.ToolCall{
			{ID: "s1", Name: finalizeToolName, Arguments: map[string]any{"text": "第一条"}},
			{ID: "s2", Name: finalizeToolName, Arguments: map[string]any{"text": "第二条"}},
		}, []string{"第一条", "第二条"}},
		{"silent", []llm.ToolCall{{ID: "s1", Name: finalizeToolName, Arguments: map[string]any{"text": "", "silent": true}}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transcript := transcriptAfter(request, &llm.GenerateResponse{ToolCalls: tc.calls, ResponsesOutput: []json.RawMessage{json.RawMessage(`{"type":"function_call"}`)}}, "")
			added := transcript[len(request.Messages):]
			var got []string
			for _, message := range added {
				if message.Role != llm.RoleAssistant || len(message.ToolCalls) > 0 || len(message.ResponsesOutput) > 0 {
					t.Fatalf("send-only finish left a tool call behind: %#v", message)
				}
				got = append(got, message.Content)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// 带别的工具的一步照旧配对：供应商拒绝没有结果的调用。
func TestTranscriptAfterKeepsMixedCallsPaired(t *testing.T) {
	request := llm.GenerateRequest{Messages: continuationTestMessages("问题")}
	calls := []llm.ToolCall{
		{ID: "s1", Name: finalizeToolName, Arguments: map[string]any{"text": "我去查一下"}},
		{ID: "t1", Name: "web_search", Arguments: map[string]any{"query": "x"}},
	}
	added := transcriptAfter(request, &llm.GenerateResponse{ToolCalls: calls}, "")[len(request.Messages):]
	if len(added) != 3 || len(added[0].ToolCalls) != 2 || added[1].ToolCallID != "s1" || added[2].ToolCallID != "t1" {
		t.Fatalf("mixed step must keep calls paired with results: %#v", added)
	}
}
