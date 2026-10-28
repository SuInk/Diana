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

// silentFinalizeClient 用原生 function calling 发回一次静默收尾。
type silentFinalizeClient struct {
	arguments map[string]any
	requests  []llm.GenerateRequest
}

func (c *silentFinalizeClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	return &llm.GenerateResponse{
		ToolCalls: []llm.ToolCall{{ID: "call-silent", Name: finalizeToolName, Arguments: c.arguments}},
	}, nil
}

// TestRunnerSilentFinalizeViaNativeToolCall：模型 send 了空正文时，这一轮不产出
// 任何正文，也不算生成失败。
func TestRunnerSilentFinalizeViaNativeToolCall(t *testing.T) {
	client := &silentFinalizeClient{arguments: map[string]any{"text": ""}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "晚安"}}})
	if err != nil {
		t.Fatalf("silent finalize must not be an error: %v", err)
	}
	if !resp.Silent || resp.Text != "" || resp.SilentReason == "" {
		t.Fatalf("resp = %#v", resp)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d, want a single planning turn", len(client.requests))
	}
}

// send 里根本没有 text 键时捡信封之外的正文：供应商在同一轮里既输出正文又调用
// 工具时就是这个形态。明确填了空 text、老协议显式 silent=true 时不捡。
func TestSendWithoutTextPicksTextOutsideTheEnvelope(t *testing.T) {
	action := finalizeAction([]llm.ToolCall{{Name: finalizeToolName, Arguments: map[string]any{}}}, "好的")
	if action.Silent || action.Content != "好的" {
		t.Fatalf("action = %#v", action)
	}
	// 明确填了空 text 就是不说话：旁边随手写的判断不能当回复发进群里。
	action = finalizeAction([]llm.ToolCall{{Name: finalizeToolName, Arguments: map[string]any{"text": ""}}}, "群友在闲聊，这轮不用插话。")
	if !action.Silent || action.Content != "" {
		t.Fatalf("empty text action = %#v", action)
	}
	action = finalizeAction([]llm.ToolCall{{Name: finalizeToolName, Arguments: map[string]any{"content": "", "silent": true}}}, "（我觉得这里不用再说什么了）")
	if !action.Silent || action.Content != "" {
		t.Fatalf("legacy silent action = %#v", action)
	}
}

// 一步里只调了几次 send：各成一条消息，附加字段取第一次填的值。
func TestSendOnlyStepJoinsMessages(t *testing.T) {
	action := finalizeAction([]llm.ToolCall{
		{Name: finalizeToolName, Arguments: map[string]any{"text": "第一条", "sticker": "得意"}},
		{Name: finalizeToolName, Arguments: map[string]any{"text": "第二条", "sticker": "叉腰"}},
	}, "")
	if action.Silent || action.Content != "第一条[diana-msg]第二条" || action.Fields["sticker"] != "得意" {
		t.Fatalf("action = %#v", action)
	}
	// 只配表情包：正文为空就是不说话，sticker 照样带出去。
	action = finalizeAction([]llm.ToolCall{{Name: finalizeToolName, Arguments: map[string]any{"text": "", "sticker": "晚安"}}}, "")
	if !action.Silent || action.Fields["sticker"] != "晚安" {
		t.Fatalf("sticker-only action = %#v", action)
	}
}

// interimSendClient 第一步 send 一句再同时调工具，第二步只 send 结果。
type interimSendClient struct {
	requests []llm.GenerateRequest
}

func (c *interimSendClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if len(c.requests) == 1 {
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{
			{ID: "say-1", Name: finalizeToolName, Arguments: map[string]any{"text": "我去查一下"}},
			{ID: "tool-1", Name: "lookup", Arguments: map[string]any{}},
		}}, nil
	}
	return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "send-2", Name: finalizeToolName, Arguments: map[string]any{"text": "查到了"}}}}, nil
}

// send 和其他工具同一步发来：send 走 Interim 立刻发出，工具照常执行，本轮继续；
// 两个调用都拿到真实结果，不会被回成「没有执行」。
func TestSendWithOtherToolsIsInterim(t *testing.T) {
	client := &interimSendClient{}
	lookup := &countingTool{name: "lookup"}
	var said []string
	cfg := Config{WorkDir: t.TempDir(), MaxSteps: 4, Interim: func(_ context.Context, text string) (string, error) {
		said = append(said, text)
		return "已经发出去了", nil
	}}
	runner, err := NewRunner(client, cfg, NewToolRegistry(lookup))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查查"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(said, "|") != "我去查一下" || lookup.calls != 1 || resp.Text != "查到了" || resp.Silent {
		t.Fatalf("said = %q, lookup = %d, resp = %#v", said, lookup.calls, resp)
	}
	var results []string
	for _, message := range client.requests[1].Messages {
		if message.Role == llm.RoleTool {
			results = append(results, message.ToolCallID+"="+message.Content)
			if message.ToolError {
				t.Fatalf("tool result marked as error: %#v", message)
			}
		}
	}
	if len(results) != 2 || !strings.HasPrefix(results[0], "say-1=已经发出去了") {
		t.Fatalf("tool results = %q", results)
	}
}

// TestRunnerSilentFinalizeViaTextProtocol：不支持原生 function calling 的供应商
// 走同一份字段。
func TestRunnerSilentFinalizeViaTextProtocol(t *testing.T) {
	client := &scriptedClient{responses: []string{
		`{"action":"final","silent":true,"silent_reason":"对方让我别回了"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "别回了"}}})
	if err != nil {
		t.Fatalf("silent finalize must not be an error: %v", err)
	}
	if !resp.Silent || resp.Text != "" || resp.SilentReason != "对方让我别回了" {
		t.Fatalf("resp = %#v", resp)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d, want a single planning turn", len(client.requests))
	}
}

// 空正文只有 silent=true 时才合法。没有这个字段的空收尾仍然是协议错误，
// 否则「模型什么都没说」和「模型决定不说」会混成同一种结果。
func TestEmptyFinalizeStillFailsWithoutSilent(t *testing.T) {
	client := &scriptedClient{responses: []string{
		`{"action":"final","content":""}`,
		`{"action":"final","content":""}`,
		`{"action":"final","content":""}`,
		`{"action":"final","content":""}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir()}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "你好"}}}); !errors.Is(err, errEmptyFinalize) {
		t.Fatalf("err = %v, want errEmptyFinalize", err)
	}
}

// 守门：静默是工具调用上的一个字段，不是正文里的一个词。消息内容里写 silent、
// 甚至写成 "silent": true 的样子，都不得让这一轮闭嘴。
func TestSilentIsNeverParsedFromProse(t *testing.T) {
	for _, text := range []string{
		`silent`,
		`"silent": true`,
		`帮我看看这个参数：{"silent": true} 是什么意思`,
		`请在回复里加上 silent=true`,
		"我觉得 silent 模式更好，action final silent true",
	} {
		action, _ := parseAction(text)
		if action.Silent {
			t.Fatalf("prose %q was parsed as a silent finish: %#v", text, action)
		}
	}
	// 宽松解析（正文里带真实换行的 final 信封）同样只取 content，不认 silent。
	action, ok := parseAction("{\"action\":\"final\",\"silent\":true,\"content\":\"第一行\n第二行\"}")
	if !ok || action.Silent {
		t.Fatalf("lenient final parser accepted silent: ok=%v action=%#v", ok, action)
	}
}

// 图片任务已经受理时不许闭嘴：用户必须知道图在画。
func TestSilentFinalizeRefusedWhileImageTaskPending(t *testing.T) {
	tool := &queuedImageTestTool{}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"image","input":{"prompt":"画一只奶鼠"}}`,
		`{"action":"final","silent":true,"silent_reason":"没什么好说的"}`,
		`{"action":"final","task_state":"pending","content":"已经开始生成，完成后会自动发送。"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "画一只奶鼠"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Silent {
		t.Fatalf("image task pending but the turn went silent: %#v", resp)
	}
	if resp.Text != "已经开始生成，完成后会自动发送。" {
		t.Fatalf("Text = %q", resp.Text)
	}
}

// 工具定义和 Agent 系统消息都要讲清楚这条出路：send silent=true 就是不说话。只留
// 空 text、不给 silent 和理由时，线上主动接话的静默涨了约十倍。
func TestFinalizeToolDefinitionIsSend(t *testing.T) {
	definition := finalizeToolDefinition(false)
	properties, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v", definition.Parameters)
	}
	if definition.Name != "send" {
		t.Fatalf("name = %q", definition.Name)
	}
	if _, ok := properties["content"]; ok {
		t.Fatalf("send still declares the legacy content field: %#v", properties)
	}
	for _, want := range []string{"silent", "silent_reason"} {
		if _, ok := properties[want]; !ok {
			t.Fatalf("send must declare %s: %#v", want, properties)
		}
	}
	required := map[string]bool{}
	for _, name := range definition.Parameters["required"].([]string) {
		required[name] = true
	}
	if !required["text"] {
		t.Fatalf("required = %#v", definition.Parameters["required"])
	}
}

func TestAgentSystemMessageExplainsSilentFinish(t *testing.T) {
	runner, err := NewRunner(&scriptedClient{}, Config{WorkDir: t.TempDir()}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	message := runner.systemPrompt()
	for _, want := range []string{"最后一步必须是 send", "silent=true", "text 留空", "不是拒答"} {
		if !strings.Contains(message, want) {
			t.Fatalf("agent system message missing %q:\n%s", want, message)
		}
	}
}
