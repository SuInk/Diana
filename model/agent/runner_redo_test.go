// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 同一轮重来时，上一版执行过的工具原样带进下一版：下一版看得到调用和结果、步骤
// 记录也算本轮已做，不会再查一遍（11-22 GTA6 发售日：上一版搜到已发售，重来那版
// 没再搜，说成明年）。
func TestRunnerRedoCarriesExecutedTools(t *testing.T) {
	search := &recordingSearchTool{output: `{"results":[{"title":"GTA6 11 月 19 日发售"}]}`}
	first := &mixedReplyClient{responses: []*llm.GenerateResponse{
		{ToolCalls: []llm.ToolCall{{ID: "w1", Name: WebSearchToolName, Arguments: map[string]any{"query": "GTA6 发售日"}}}},
		{ToolCalls: []llm.ToolCall{{ID: "s1", Name: finalizeToolName, Arguments: map[string]any{"text": "11 月 19 号已经发售了"}}}},
	}}
	runner, err := NewRunner(first, Config{MaxSteps: 4}, NewToolRegistry(search))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "GTA6 算是烂了"}}})
	if err != nil {
		t.Fatal(err)
	}
	executed := resp.Executed
	if len(executed.Steps) != 1 || executed.Steps[0].Tool != WebSearchToolName {
		t.Fatalf("executed steps = %#v", executed.Steps)
	}
	if len(executed.Messages) < 2 || len(executed.Messages[0].ToolCalls) != 1 || executed.Messages[0].ToolCalls[0].ID != "w1" || executed.Messages[1].ToolCallID != "w1" {
		t.Fatalf("executed messages = %#v", executed.Messages)
	}
	for _, message := range executed.Messages {
		for _, call := range message.ToolCalls {
			if call.Name == finalizeToolName {
				t.Fatalf("send leaked into executed tools: %#v", executed.Messages)
			}
		}
	}

	second := &mixedReplyClient{responses: []*llm.GenerateResponse{
		{ToolCalls: []llm.ToolCall{{ID: "s2", Name: finalizeToolName, Arguments: map[string]any{"text": "11 月 19 号已经上了，说烂还早"}}}},
	}}
	runner, err = NewRunner(second, Config{MaxSteps: 4}, NewToolRegistry(search))
	if err != nil {
		t.Fatal(err)
	}
	redo, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "GTA6 算是烂了\n游戏科学将替代 R 星"}},
		Redo:     &executed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if search.calls != 1 {
		t.Fatalf("search ran again: %d", search.calls)
	}
	request := second.requests[0].Messages
	noteAt := -1
	for index, message := range request {
		if message.Content == redoExecutedToolsNote {
			noteAt = index
		}
	}
	if noteAt < 0 || noteAt+2 >= len(request) || request[noteAt+1].ToolCalls[0].ID != "w1" || request[noteAt+2].ToolCallID != "w1" {
		t.Fatalf("redo request lacks carried tools: %#v", request)
	}
	if request[noteAt-1].Content != "GTA6 算是烂了\n游戏科学将替代 R 星" {
		t.Fatalf("carried tools not after the current message: %#v", request[noteAt-1])
	}
	if len(redo.Steps) != 1 || len(redo.Executed.Messages) != len(executed.Messages) {
		t.Fatalf("redo steps=%#v executed=%d", redo.Steps, len(redo.Executed.Messages))
	}
	// 带进来的上一版工具只在这一轮有效，不进下一轮的续接前缀。
	for _, message := range redo.Transcript {
		if message.Content == redoExecutedToolsNote || message.ToolCallID == "w1" {
			t.Fatalf("carried tools leaked into the next turn's prefix: %#v", message)
		}
		for _, call := range message.ToolCalls {
			if call.ID == "w1" {
				t.Fatalf("carried tool call leaked into the next turn's prefix: %#v", message)
			}
		}
	}
}
