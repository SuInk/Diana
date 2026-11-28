// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 收尾退回的是事实，不是工具失败：send 的结果不标 ToolError，下一步请求末尾也不接
// 「工具调用失败」或「写最终回复前」那段提醒。
func TestFinishFeedbackIsNotAToolFailure(t *testing.T) {
	client := &stepsClient{steps: [][]llm.ToolCall{
		{{ID: "send-1", Name: finalizeToolName, Arguments: map[string]any{"text": ""}}},
		{{ID: "send-2", Name: finalizeToolName, Arguments: map[string]any{"text": "早上好"}}},
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, ToolResultReminder: "写最终回复前"}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "早"}}}); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d", len(client.requests))
	}
	retry := client.requests[1].Messages
	last := retry[len(retry)-1]
	if last.Role != llm.RoleTool || last.ToolCallID != "send-1" || last.ToolError || last.Content != emptySendFeedback {
		t.Fatalf("feedback must be the plain send result: %#v", last)
	}
	for _, message := range retry[len(client.requests[0].Messages):] {
		if strings.Contains(message.Content, "工具调用失败") || strings.Contains(message.Content, "写最终回复前") {
			t.Fatalf("feedback followed by a reminder: %q", message.Content)
		}
	}
}

// 纯文本没走 send 和空 send 共用一次格式退回：纯文本退回过，接着的空 send 不再退回，
// 按不说话收尾。
func TestFormatRejectionIsSharedBetweenPlainTextAndEmptySend(t *testing.T) {
	client := &mixedReplyClient{responses: []*llm.GenerateResponse{
		{Text: "早上好呀"},
		{ToolCalls: []llm.ToolCall{{ID: "send-1", Name: finalizeToolName, Arguments: map[string]any{"text": ""}}}},
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 4, PlainTextNotSent: true}, NewToolRegistry(&recordingSearchTool{}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "早"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Silent || len(client.requests) != 2 {
		t.Fatalf("silent=%v requests=%d", resp.Silent, len(client.requests))
	}
}

// 没开 PlainTextNotSent 的调用方（子任务等）写了整段括号动作，退回的是「没有执行任何操作」，
// 不说「没有发出去」。
func TestPlainTextActionFeedbackWithoutPlainTextNotSent(t *testing.T) {
	client := &mixedReplyClient{responses: []*llm.GenerateResponse{
		{Text: "[搜索 WePlay 参展商名单]"},
		{Text: "查不到名单"},
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(&recordingSearchTool{}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "WePlay 有哪些参展商"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "查不到名单" || len(client.requests) != 2 {
		t.Fatalf("text=%q requests=%d", resp.Text, len(client.requests))
	}
	retry := client.requests[1].Messages
	if last := retry[len(retry)-1]; last.Content != plainTextActionPrompt {
		t.Fatalf("feedback = %q", last.Content)
	}
}
