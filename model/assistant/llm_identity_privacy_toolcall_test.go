// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 提示词告诉模型「原样复制别名，本地代理会在执行工具前自动恢复真实标识」。以前只
// 还原了回复正文，别名原封不动地进了工具，提醒工具收到 im_user_xxx 只能报「必须是
// 有效 QQ 号」。工具参数必须一起还原。
func TestIdentityPrivacyRestoresToolCallArguments(t *testing.T) {
	scope := newIdentityPrivacyScope()
	alias := scope.register("10001", "user")
	if alias == "10001" {
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
	if got := restored[0].Arguments["target_user_id"]; got != "10001" {
		t.Fatalf("target_user_id = %v", got)
	}
	nested, _ := restored[0].Arguments["targets"].([]any)
	if len(nested) != 2 || nested[0] != "10001" {
		t.Fatalf("nested slice not restored: %#v", nested)
	}
	inner, _ := nested[1].(map[string]any)
	if inner["user_id"] != "10001" {
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
func TestIdentityPrivacyProtectsReplayedToolCallArguments(t *testing.T) {
	scope := newIdentityPrivacyScope()
	alias := scope.register("10001", "user")

	request := llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "系统"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID: "call-1", Name: "diana.reminder",
			Arguments: map[string]any{"target_user_id": "10001"},
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
	if request.Messages[1].ToolCalls[0].Arguments["target_user_id"] != "10001" {
		t.Fatal("original request was mutated")
	}
}

// 消息 ID 也要脱敏，否则模型手里握着一批真实 ID。难点在于它可以是负数，而且必须能
// 原路还原——不然模型写的 [diana-reply:别名] 会因为不是纯数字而被丢弃，引用悄悄失效。
func TestIdentityPrivacyMasksMessageIDsRoundTrip(t *testing.T) {
	scope := newIdentityPrivacyScope()
	scope.registerEvent(MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: "10001", MessageID: "1145141919",
		Quoted: &QuotedMessage{MessageID: "-810975", UserID: "10002"},
	})

	for _, sample := range []string{
		"[diana-reply:1145141919] 收到",
		"[diana-reply:-810975] 负数 ID",
		`{"message_id":"1145141919","quoted_message_id":"-810975"}`,
		// 事件里没登记、只在历史文本里出现过的也要认出来。
		"[diana-reply:2233445566] 旧消息",
	} {
		masked := scope.protectText(sample)
		if strings.Contains(masked, "1145141919") || strings.Contains(masked, "810975") || strings.Contains(masked, "2233445566") {
			t.Fatalf("message id leaked: %q -> %q", sample, masked)
		}
		if !strings.Contains(masked, "im_message_") {
			t.Fatalf("message id not aliased: %q -> %q", sample, masked)
		}
		if restored := scope.restoreText(masked); restored != sample {
			t.Fatalf("round trip broken: %q -> %q -> %q", sample, masked, restored)
		}
	}
}

// 端到端：模型原样复制别名标记，代理还原后必须能解析出真正的回复目标。
func TestIdentityPrivacyReplyMarkerSurvivesMasking(t *testing.T) {
	scope := newIdentityPrivacyScope()
	alias := scope.registerMessageID("1145141919")
	if alias == "1145141919" {
		t.Fatal("message id should have been aliased")
	}
	// 别名直接交给出站解析会被拒（不是纯数字），这正是必须先还原的原因。
	if _, _, ok := extractOutgoingReplyMarker("[diana-reply:" + alias + "] 看到了"); ok {
		t.Fatal("alias should not parse as a reply target before restoration")
	}
	id, rest, ok := extractOutgoingReplyMarker(scope.restoreText("[diana-reply:" + alias + "] 看到了"))
	if !ok || id != "1145141919" || rest != "看到了" {
		t.Fatalf("reply marker broken after restoration: id=%q rest=%q ok=%v", id, rest, ok)
	}
}

// 消息 ID 允许负号，QQ 号不允许；两者的判定不能混用。
func TestIsLikelyMessageID(t *testing.T) {
	for _, valid := range []string{"1145141919", "-810975", "1234"} {
		if !isLikelyMessageID(valid) {
			t.Fatalf("%q should be a message id", valid)
		}
	}
	for _, invalid := range []string{"", "123", "abc", "-", "12a45", "-12a45"} {
		if isLikelyMessageID(invalid) {
			t.Fatalf("%q should not be a message id", invalid)
		}
	}
	// QQ 号判定仍然拒绝负数。
	if isLikelyChatIdentifier("-810975") {
		t.Fatal("negative value must not be treated as a QQ id")
	}
}
