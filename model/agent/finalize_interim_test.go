// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// stepsClient 按顺序发回预先写好的原生调用，用完后只 send「好了」。
type stepsClient struct {
	steps    [][]llm.ToolCall
	requests []llm.GenerateRequest
}

func (c *stepsClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if index := len(c.requests) - 1; index < len(c.steps) {
		return &llm.GenerateResponse{ToolCalls: c.steps[index]}, nil
	}
	return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "send-last", Name: finalizeToolName, Arguments: map[string]any{"text": "好了"}}}}, nil
}

// toolResults 取第 n 次请求里的工具结果，按顺序。
func (c *stepsClient) toolResults(n int) []llm.Message {
	var results []llm.Message
	for _, message := range c.requests[n].Messages {
		if message.Role == llm.RoleTool {
			results = append(results, message)
		}
	}
	return results
}

// orderTool 把自己的执行写进共享的事件序列，能按需失败。
type orderTool struct {
	name   string
	err    error
	events *[]string
}

func (t *orderTool) Name() string        { return t.name }
func (t *orderTool) Description() string { return "test tool" }
func (t *orderTool) Run(context.Context, map[string]any) (string, error) {
	*t.events = append(*t.events, "run:"+t.name)
	return `{"ok":true}`, t.err
}

func interimRecorder(events *[]string) func(context.Context, string) (string, error) {
	return func(_ context.Context, text string) (string, error) {
		*events = append(*events, "send:"+text)
		return "已经发出去了", nil
	}
}

// 带副作用的工具失败了，同一步先说的「定好了」不能发出去，模型要知道这句没发。
func TestInterimSendWaitsForSideEffectTool(t *testing.T) {
	var events []string
	client := &stepsClient{steps: [][]llm.ToolCall{{
		{ID: "send-1", Name: finalizeToolName, Arguments: map[string]any{"text": "定好了"}},
		{ID: "remind-1", Name: "remind", Arguments: map[string]any{}},
	}}}
	remind := &orderTool{name: "remind", err: errors.New("提醒额度用完了"), events: &events}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, Interim: interimRecorder(&events)}, NewToolRegistry(remind))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "明早八点提醒我"}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(events, "|") != "run:remind" {
		t.Fatalf("events = %q, the interim send must not go out when the tool failed", events)
	}
	results := client.toolResults(1)
	if len(results) != 2 || !strings.Contains(results[0].Content, "没有发出") || !results[0].ToolError {
		t.Fatalf("tool results = %#v", results)
	}

	// 成功时照常发出，只是排在工具之后。
	events = nil
	remind.err = nil
	client.requests = nil
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "明早八点提醒我"}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(events, "|") != "run:remind|send:定好了" {
		t.Fatalf("events = %q", events)
	}
	if results := client.toolResults(1); results[0].Content != "已经发出去了" || results[0].ToolError {
		t.Fatalf("tool results = %#v", results)
	}
}

// 同一步只有检索时「我去查一下」赶在检索之前发出。
func TestInterimSendGoesBeforeSearch(t *testing.T) {
	var events []string
	client := &stepsClient{steps: [][]llm.ToolCall{{
		{ID: "send-1", Name: finalizeToolName, Arguments: map[string]any{"text": "我去查一下"}},
		{ID: "search-1", Name: webSearchToolName, Arguments: map[string]any{"query": "天气"}},
	}}}
	search := &orderTool{name: webSearchToolName, events: &events}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, Interim: interimRecorder(&events)}, NewToolRegistry(search))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "今天天气"}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(events, "|") != "send:我去查一下|run:"+webSearchToolName {
		t.Fatalf("events = %q", events)
	}
}

// 供应商回的调用 ID 是空串时，中途发言的结果不能串到同一步的工具上。
func TestInterimSendWithEmptyCallIDs(t *testing.T) {
	var events []string
	client := &stepsClient{steps: [][]llm.ToolCall{{
		{Name: finalizeToolName, Arguments: map[string]any{"text": "我去查一下"}},
		{Name: "lookup", Arguments: map[string]any{}},
	}}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, Interim: interimRecorder(&events)}, NewToolRegistry(&orderTool{name: "lookup", events: &events}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查查"}}}); err != nil {
		t.Fatal(err)
	}
	results := client.toolResults(1)
	if len(results) != 2 || results[0].Content != "已经发出去了" || strings.Contains(results[1].Content, "已经发出去了") || !strings.Contains(results[1].Content, `"ok":true`) {
		t.Fatalf("tool results = %#v", results)
	}
}

// 模型照老历史用原生调用发 agent_finalize、say：按 send 处理，不花修复轮。
func TestLegacySendToolNamesAreAccepted(t *testing.T) {
	var events []string
	client := &stepsClient{steps: [][]llm.ToolCall{
		{
			{ID: "say-1", Name: legacyInterimToolName, Arguments: map[string]any{"text": "我去查一下"}},
			{ID: "lookup-1", Name: "lookup", Arguments: map[string]any{}},
		},
		{{ID: "final-1", Name: legacyFinalizeToolName, Arguments: map[string]any{"content": "查到了", "silent": false}}},
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, Interim: interimRecorder(&events)}, NewToolRegistry(&orderTool{name: "lookup", events: &events}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查查"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "查到了" || len(client.requests) != 2 || strings.Join(events, "|") != "run:lookup|send:我去查一下" {
		t.Fatalf("resp = %#v, requests = %d, events = %q", resp, len(client.requests), events)
	}

	// 老收尾的 silent=true 就是不说话，content 里写了什么都不发。
	client = &stepsClient{steps: [][]llm.ToolCall{{{ID: "final-1", Name: legacyFinalizeToolName, Arguments: map[string]any{"content": "已经说过了", "silent": true}}}}}
	runner, err = NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err = runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "嗯"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Silent || resp.Text != "" || len(client.requests) != 1 {
		t.Fatalf("resp = %#v, requests = %d", resp, len(client.requests))
	}
}

// 只调 send 却填了 continue：这句当中途发言发出，本轮接着做，承诺的工具真的执行，
// 最后再收尾。以前只调 send 一律收尾，「我查一下今天的新闻」发完这一轮就结束了。
func TestSendOnlyWithContinueKeepsTheTurnGoing(t *testing.T) {
	var events []string
	client := &stepsClient{steps: [][]llm.ToolCall{
		{{ID: "send-1", Name: finalizeToolName, Arguments: map[string]any{"text": "我查一下今天的新闻", "continue": true}}},
		{{ID: "search-1", Name: "news", Arguments: map[string]any{}}},
	}}
	news := &orderTool{name: "news", events: &events}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, Interim: interimRecorder(&events)}, NewToolRegistry(news))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查一下今天的新闻"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, "|"); got != "send:我查一下今天的新闻|run:news" {
		t.Fatalf("events = %q", got)
	}
	if resp.Text != "好了" || len(client.requests) != 3 {
		t.Fatalf("text = %q after %d requests", resp.Text, len(client.requests))
	}
	if results := client.toolResults(1); len(results) != 1 || results[0].ToolCallID != "send-1" || results[0].ToolError || results[0].Content != "已经发出去了" {
		t.Fatalf("the continued send must be paired with its real result: %#v", results)
	}
}

// 一直只说不做：续两次就不再续，第三次按收尾发出，不会无限转圈。
func TestSendOnlyContinueIsCapped(t *testing.T) {
	var events []string
	again := []llm.ToolCall{{ID: "send", Name: finalizeToolName, Arguments: map[string]any{"text": "马上", "continue": true}}}
	client := &stepsClient{steps: [][]llm.ToolCall{again, again, again, again}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, Interim: interimRecorder(&events)}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "在吗"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != maxContinueOnlySends+1 || len(events) != maxContinueOnlySends || resp.Text != "马上" {
		t.Fatalf("requests = %d, interim = %q, text = %q", len(client.requests), events, resp.Text)
	}
}

// 没填 continue 的只调 send 照旧是收尾。
func TestSendOnlyWithoutContinueStillFinishes(t *testing.T) {
	var events []string
	client := &stepsClient{steps: [][]llm.ToolCall{{{ID: "send-1", Name: finalizeToolName, Arguments: map[string]any{"text": "早"}}}}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, Interim: interimRecorder(&events)}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "早"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "早" || len(client.requests) != 1 || len(events) != 0 {
		t.Fatalf("text = %q, requests = %d, interim = %q", resp.Text, len(client.requests), events)
	}
}
