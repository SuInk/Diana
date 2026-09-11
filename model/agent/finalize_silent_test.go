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

// TestRunnerSilentFinalizeViaNativeToolCall：模型调用 agent.finalize 填 silent=true
// 时，这一轮不产出任何正文，也不算生成失败。
func TestRunnerSilentFinalizeViaNativeToolCall(t *testing.T) {
	client := &silentFinalizeClient{arguments: map[string]any{
		"content":       "",
		"silent":        true,
		"silent_reason": "双方已经互相道过别，没有要补的",
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "晚安"}}})
	if err != nil {
		t.Fatalf("silent finalize must not be an error: %v", err)
	}
	if !resp.Silent {
		t.Fatalf("resp.Silent = false: %#v", resp)
	}
	if resp.Text != "" {
		t.Fatalf("silent finalize produced text %q", resp.Text)
	}
	if resp.SilentReason != "双方已经互相道过别，没有要补的" {
		t.Fatalf("SilentReason = %q", resp.SilentReason)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d, want a single planning turn", len(client.requests))
	}
}

// 静默收尾不捡信封之外的正文：模型同一轮里随口写的话不是这一轮的回复。
func TestSilentFinalizeIgnoresTextOutsideTheEnvelope(t *testing.T) {
	action := finalizeAction(llm.ToolCall{
		Name:      finalizeToolName,
		Arguments: map[string]any{"content": "", "silent": true},
	}, "（我觉得这里不用再说什么了）")
	if !action.Silent || action.Content != "" {
		t.Fatalf("action = %#v", action)
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
		`{"action":"tool","tool":"diana.image","input":{"prompt":"画一只奶鼠"}}`,
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

// 工具定义和 Agent 系统消息都要讲清楚这条出路，否则模型无从知道它存在。
func TestFinalizeToolDefinitionDeclaresSilent(t *testing.T) {
	definition := finalizeToolDefinition(newClaimEvidenceLedger(), false)
	properties, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v", definition.Parameters)
	}
	if _, ok := properties["silent"]; !ok {
		t.Fatalf("finalize tool has no silent field: %#v", properties)
	}
	if _, ok := properties["silent_reason"]; !ok {
		t.Fatalf("finalize tool has no silent_reason field: %#v", properties)
	}
	// silent 是可选参数：它不进 required，由 provider 边界改写成可为 null。
	required := map[string]bool{}
	for _, name := range definition.Parameters["required"].([]string) {
		required[name] = true
	}
	if required["silent"] || !required["content"] {
		t.Fatalf("required = %#v", definition.Parameters["required"])
	}
}

func TestAgentSystemMessageExplainsSilentFinish(t *testing.T) {
	runner, err := NewRunner(&scriptedClient{}, Config{WorkDir: t.TempDir()}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	message := runner.systemPrompt()
	for _, want := range []string{"silent=true", "silent_reason", "不是拒答"} {
		if !strings.Contains(message, want) {
			t.Fatalf("agent system message missing %q:\n%s", want, message)
		}
	}
}
