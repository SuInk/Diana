// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 提示词必须把「规划步」和「用户消息」区分开,并写明本条回复内的工具预算。
// 曾经的措辞「每轮最多调用一个工具」被模型理解成「用户每发一条消息只能调
// 一次」,多步任务每调一次就收口,让用户发「继续」才肯继续。
func TestSystemPromptStatesPerReplyToolBudget(t *testing.T) {
	runner, err := NewRunner(&scriptedClient{}, Config{MaxSteps: 8}, NewToolRegistry(&countingTool{name: "lookup"}))
	if err != nil {
		t.Fatal(err)
	}
	prompt := runner.systemPrompt()
	if !strings.Contains(prompt, "8 次") {
		t.Fatalf("提示词应写明本条回复内的工具预算,实际:%s", prompt)
	}
	if !strings.Contains(prompt, "不要停下来向用户要求「继续」") {
		t.Fatalf("提示词应明确禁止中途向用户要「继续」,实际:%s", prompt)
	}
	if strings.Contains(prompt, "每轮最多调用一个工具") {
		t.Fatalf("会被误读的旧措辞不该再出现:%s", prompt)
	}
}

type parallelToolClient struct {
	requests []llm.GenerateRequest
}

func (c *parallelToolClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if len(c.requests) == 1 {
		return &llm.GenerateResponse{
			ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: "lookup", Arguments: map[string]any{"query": "one"}},
				{ID: "call-2", Name: "lookup", Arguments: map[string]any{"query": "two"}},
				{ID: "call-3", Name: "lookup", Arguments: map[string]any{"query": "three"}},
			},
		}, nil
	}
	return &llm.GenerateResponse{Text: `{"action":"final","content":"done"}`}, nil
}

// 并行工具调用当前只执行第一个,其余被丢弃——这必须在观察消息里明说,
// 否则模型以为剩下的执行过了,或者得出「一次只能调一个」的错误经验。
func TestParallelToolCallsDropIsReportedToModel(t *testing.T) {
	client := &parallelToolClient{}
	tool := &countingTool{name: "lookup"}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查三个"}}}); err != nil {
		t.Fatal(err)
	}
	if tool.calls != 1 {
		t.Fatalf("当前实现应只执行第一个调用,实际 %d", tool.calls)
	}
	notified := false
	for _, req := range client.requests[1:] {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "并行请求了 3 个工具调用") && strings.Contains(msg.Content, "没有执行") {
				notified = true
			}
		}
	}
	if !notified {
		t.Fatal("被丢弃的并行调用没有告知模型")
	}
}
