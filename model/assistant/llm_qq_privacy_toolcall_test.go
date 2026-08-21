// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 提示词告诉模型「原样复制别名，本地代理会在执行工具前自动恢复真实标识」。以前只
// 还原了回复正文，别名原封不动地进了工具，提醒工具收到 qq_user_xxx 只能报「必须是
// 有效 QQ 号」。工具参数必须一起还原。
func TestQQPrivacyRestoresToolCallArguments(t *testing.T) {
	scope := newQQPrivacyScope()
	alias := scope.register("10001234", "user")
	if alias == "10001234" {
		t.Fatal("id should have been aliased")
	}

	calls := []llm.ToolCall{{
		ID:   "call-1",
		Name: "diana.reminder",
		Arguments: map[string]any{
			"operation":      "create",
			"delay":          "5m",
			"target_user_id": alias,
			// 嵌套结构里的别名同样要还原。
			"targets": []any{alias, map[string]any{"user_id": alias}},
			// 非字符串保持原样。
			"count": float64(3),
		},
	}}
	restored := scope.restoreToolCalls(calls)
	if got := restored[0].Arguments["target_user_id"]; got != "10001234" {
		t.Fatalf("target_user_id = %v", got)
	}
	nested, _ := restored[0].Arguments["targets"].([]any)
	if len(nested) != 2 || nested[0] != "10001234" {
		t.Fatalf("nested slice not restored: %#v", nested)
	}
	inner, _ := nested[1].(map[string]any)
	if inner["user_id"] != "10001234" {
		t.Fatalf("nested map not restored: %#v", inner)
	}
	if restored[0].Arguments["count"] != float64(3) {
		t.Fatalf("non-string argument mutated: %#v", restored[0].Arguments["count"])
	}
	// 不能就地改坏调用方的那份。
	if calls[0].Arguments["target_user_id"] != alias {
		t.Fatal("original tool call was mutated")
	}
}

// 反向的同一个漏洞：Agent 循环会把上一轮的工具调用回放进历史，那些参数已经被还原成
// 真实 QQ 号，不重新替换回别名就会漏回模型，隐私代理等于白做。
func TestQQPrivacyProtectsReplayedToolCallArguments(t *testing.T) {
	scope := newQQPrivacyScope()
	alias := scope.register("10001234", "user")

	request := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "系统"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID: "call-1", Name: "diana.reminder",
			Arguments: map[string]any{"target_user_id": "10001234"},
		}}},
	}}
	protected := scope.protectRequest(request)

	var assistant llm.Message
	for _, message := range protected.Messages {
		if message.Role == llm.RoleAssistant {
			assistant = message
		}
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant tool calls missing: %#v", protected.Messages)
	}
	if got := assistant.ToolCalls[0].Arguments["target_user_id"]; got != alias {
		t.Fatalf("real id leaked back to the model: %v", got)
	}
	// 原请求不能被就地改写。
	if request.Messages[1].ToolCalls[0].Arguments["target_user_id"] != "10001234" {
		t.Fatal("original request was mutated")
	}
}
