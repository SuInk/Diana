// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
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
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4}, NewToolRegistry(&orderTool{name: "lookup", events: &events}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查查"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "查到了" || len(client.requests) != 2 || strings.Join(events, "|") != "run:lookup" {
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

// 只调 send 就是收尾，发完本轮结束。
func TestSendOnlyFinishes(t *testing.T) {
	client := &stepsClient{steps: [][]llm.ToolCall{{{ID: "send-1", Name: finalizeToolName, Arguments: map[string]any{"text": "早"}}}}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "早"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "早" || len(client.requests) != 1 {
		t.Fatalf("text = %q, requests = %d", resp.Text, len(client.requests))
	}
}

func TestEmptySendWithoutSilentIsSentBackOnce(t *testing.T) {
	client := &stepsClient{steps: [][]llm.ToolCall{
		{{ID: "send-1", Name: finalizeToolName, Arguments: map[string]any{"text": ""}}},
		{{ID: "send-2", Name: finalizeToolName, Arguments: map[string]any{"text": "早上好"}}},
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "早"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Silent || resp.Text != "早上好" || len(client.requests) != 2 {
		t.Fatalf("silent = %v, text = %q, requests = %d", resp.Silent, resp.Text, len(client.requests))
	}
}

// 只填了会单独发出去的字段（表情包）、正文留空，是「只回一张图」，不是忘了写正文：
// 不退回去，直接按静默收尾把字段交给调用方。线上 11-16 一张贴纸被这样打回重写成了文字。
// 不会单独发出去的字段（比如只决定先后的 sticker_order）照旧退回一次。
func TestEmptySendWithDeliveredFieldIsNotSentBack(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fields   []FinalizeField
		args     map[string]any
		requests int
	}{
		{"delivers", []FinalizeField{{Name: "sticker", Delivers: true}}, map[string]any{"text": "", "sticker": "害羞 贴贴"}, 1},
		{"blank delivered field", []FinalizeField{{Name: "sticker", Delivers: true}}, map[string]any{"text": "", "sticker": " "}, 2},
		{"field does not deliver", []FinalizeField{{Name: "sticker", Delivers: true}, {Name: "sticker_order"}}, map[string]any{"text": "", "sticker_order": "before"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &stepsClient{steps: [][]llm.ToolCall{
				{{ID: "send-1", Name: finalizeToolName, Arguments: tc.args}},
				{{ID: "send-2", Name: finalizeToolName, Arguments: map[string]any{"text": "", "silent": true}}},
			}}
			runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, FinalizeFields: tc.fields}, NewToolRegistry())
			if err != nil {
				t.Fatal(err)
			}
			resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "[表情包]"}}})
			if err != nil {
				t.Fatal(err)
			}
			if !resp.Silent || len(client.requests) != tc.requests {
				t.Fatalf("silent = %v, requests = %d, want %d", resp.Silent, len(client.requests), tc.requests)
			}
			if tc.requests == 1 && resp.FinalizeFields["sticker"] != "害羞 贴贴" {
				t.Fatalf("finalize fields = %#v", resp.FinalizeFields)
			}
		})
	}
}

func TestEmptySendTwiceEndsSilent(t *testing.T) {
	client := &stepsClient{steps: [][]llm.ToolCall{
		{{ID: "send-1", Name: finalizeToolName, Arguments: map[string]any{"text": ""}}},
		{{ID: "send-2", Name: finalizeToolName, Arguments: map[string]any{"text": ""}}},
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "早"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Silent || len(client.requests) != 2 {
		t.Fatalf("silent = %v, requests = %d", resp.Silent, len(client.requests))
	}
}

func TestExplicitSilentSendFinishesImmediately(t *testing.T) {
	client := &stepsClient{steps: [][]llm.ToolCall{
		{{ID: "send-1", Name: finalizeToolName, Arguments: map[string]any{"text": "", "silent": true, "silent_reason": "不是对我说的"}}},
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "早"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Silent || resp.SilentReason != "不是对我说的" || len(client.requests) != 1 {
		t.Fatalf("silent = %v, reason = %q, requests = %d", resp.Silent, resp.SilentReason, len(client.requests))
	}
}
