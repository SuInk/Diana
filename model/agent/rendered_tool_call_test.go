// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 生产事故原文（2026-09-09 群聊）：供应商没把 agent.finalize 放进 tool_calls，
// 而是渲染成一行给人看的字塞进正文，整条内部协议被当成回复发进了群。
const leakedRenderedFinalize = `调用工具：agent.finalize，参数：{"content":"对，确实会跳！\nWARP 用的本来就是 Cloudflare 的动态共享 IP 池，纯纯是负优化喵～"}`

const leakedRenderedFinalizeBody = "对，确实会跳！\nWARP 用的本来就是 Cloudflare 的动态共享 IP 池，纯纯是负优化喵～"

// 渲染出来的收尾调用必须救回 content，而不是把整行协议当正文发出去。
func TestRunnerSalvagesRenderedFinalizeInsteadOfLeakingIt(t *testing.T) {
	client := &scriptedClient{responses: []string{leakedRenderedFinalize}}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(&countingTool{name: "lookup"}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "warp 会跳 ip 吗"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Text, "调用工具") || strings.Contains(resp.Text, "agent.finalize") {
		t.Fatalf("渲染出来的工具调用泄漏进了回复：%q", resp.Text)
	}
	if resp.Text != leakedRenderedFinalizeBody {
		t.Fatalf("没有救回收尾正文：%q", resp.Text)
	}
	if resp.FinishReason != "final" {
		t.Fatalf("FinishReason = %q，应按正常收尾结束", resp.FinishReason)
	}
}

// 收尾阶段（工具预算耗尽后的最后一轮）拿到同样的渲染文本时也不能泄漏。
func TestFinalizationSalvagesRenderedFinalize(t *testing.T) {
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"lookup","input":{"query":"warp"}}`,
		leakedRenderedFinalize,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 1}, NewToolRegistry(&countingTool{name: "lookup"}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "warp 会跳 ip 吗"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Text, "调用工具") || strings.Contains(resp.Text, "agent.finalize") {
		t.Fatalf("收尾阶段泄漏了渲染出来的工具调用：%q", resp.Text)
	}
	if resp.Text != leakedRenderedFinalizeBody {
		t.Fatalf("收尾阶段没有救回正文：%q", resp.Text)
	}
}

// 渲染出来的普通工具调用不执行、也不当正文：回退到协议修复，让模型改用原生调用。
func TestRenderedNonFinalizeToolCallNeverBecomesReply(t *testing.T) {
	rendered := `调用工具：lookup，参数：{"query":"warp"}`
	client := &scriptedClient{responses: []string{rendered, rendered, `{"action":"final","content":"查过了"}`}}
	tool := &countingTool{name: "lookup"}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查一下"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Text, "调用工具") {
		t.Fatalf("渲染出来的工具调用泄漏进了回复：%q", resp.Text)
	}
	if resp.Text != "查过了" {
		t.Fatalf("Text = %q", resp.Text)
	}
	repaired := false
	for _, req := range client.requests[1:] {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "Agent 动作无法解析") {
				repaired = true
			}
		}
	}
	if !repaired {
		t.Fatal("渲染出来的工具调用应该走协议修复，让模型改用原生 function calling")
	}
}

// agent.finalize 被当成普通工具写进 JSON 动作时也要收尾，而不是撞「工具不存在」。
func TestFinalizeWrittenAsJSONToolActionIsFinal(t *testing.T) {
	action, ok := parseAction(`{"action":"tool","tool":"agent.finalize","input":{"content":"好的喵～"}}`)
	if !ok || action.Action != "final" {
		t.Fatalf("action = %#v, ok = %v", action, ok)
	}
	if action.Content != "好的喵～" {
		t.Fatalf("Content = %q", action.Content)
	}
}

func TestParseActionRecognizesRenderedFinalize(t *testing.T) {
	action, ok := parseAction(leakedRenderedFinalize)
	if !ok {
		t.Fatalf("渲染出来的收尾调用应该被识别，实际 action = %#v", action)
	}
	if action.Action != "final" || action.Content != leakedRenderedFinalizeBody {
		t.Fatalf("action = %#v", action)
	}
	if !action.Salvaged {
		t.Fatal("救回来的正文应标记 Salvaged，跳过信封换行约定校验")
	}
}

// 参数被网关截断时，救不回正文也绝不能把这行协议当正文。
func TestParseActionRejectsTruncatedRenderedFinalize(t *testing.T) {
	action, ok := parseAction(`调用工具：agent.finalize，参数：{"content":"对，确实会`)
	if !ok || action.Action != "final" {
		t.Fatalf("action = %#v, ok = %v", action, ok)
	}
	if action.Content != "" {
		t.Fatalf("救不回正文时不能带出任何内容：%q", action.Content)
	}
}

func TestLooksLikeRenderedToolCall(t *testing.T) {
	rendered := []string{
		leakedRenderedFinalize,
		"调用工具：agent.finalize，参数：{}",
		"工具调用: web_search.search, arguments: {\"query\":\"warp\"}",
		"Tool call: agent.finalize",
		"  调用工具：diana.image，参数：{\"prompt\":\"猫\"}",
	}
	for _, text := range rendered {
		if !LooksLikeRenderedToolCall(text) {
			t.Fatalf("应识别为被渲染的工具调用：%q", text)
		}
	}
	prose := []string{
		"",
		"我先调用工具查一下再回你",
		"调用工具这件事我做不到喵～",
		"接下来我会调用工具：先搜一下 WARP 的 IP 池，再看看有没有别的说法",
		`{"action":"final","content":"调用工具：这词今天出现得有点多"}`,
	}
	for _, text := range prose {
		if LooksLikeRenderedToolCall(text) {
			t.Fatalf("正常表达被误判成渲染的工具调用：%q", text)
		}
	}
}
