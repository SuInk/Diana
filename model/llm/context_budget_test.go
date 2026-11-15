// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultContextWindowIs128K(t *testing.T) {
	if DefaultContextWindowTokens != 128000 || DefaultMaxContextTokens != 128000 {
		t.Fatalf("default context window/max = %d/%d, want 128000/128000", DefaultContextWindowTokens, DefaultMaxContextTokens)
	}
}

// 发送层不再丢消息、截正文：再大的请求也原样交给校验，由上层压缩。
func TestApplyContextLimitNeverChangesMessages(t *testing.T) {
	var messages []Message
	messages = append(messages, Message{Role: RoleSystem, Content: strings.Repeat("规则", 4000)})
	for index := 0; index < 200; index++ {
		messages = append(messages, Message{Role: RoleUser, Content: fmt.Sprintf("历史 %d %s", index, strings.Repeat("字", 400)), Priority: MessagePriorityHistory})
	}
	messages = append(messages, Message{Role: RoleUser, Content: "当前问题"})
	req := GenerateRequest{Model: "test", Messages: append([]Message(nil), messages...)}
	got := applyContextLimit(req, ProviderConfig{MaxContextTokens: 16000, ContextWindowTokens: 16000})
	if !reflect.DeepEqual(got.Messages, messages) {
		t.Fatalf("messages changed: %d -> %d", len(messages), len(got.Messages))
	}
	if got.MaxContextTokens != 16000 {
		t.Fatalf("MaxContextTokens = %d, want 16000", got.MaxContextTokens)
	}
}

func TestApplyContextLimitOnlyTightens(t *testing.T) {
	cfg := ProviderConfig{ContextWindowTokens: 64000}
	if got := applyContextLimit(GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}, MaxContextTokens: 200000}, cfg); got.MaxContextTokens != 64000 {
		t.Fatalf("caller widened limit to %d", got.MaxContextTokens)
	}
	if got := applyContextLimit(GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}, MaxContextTokens: 32000}, cfg); got.MaxContextTokens != 32000 {
		t.Fatalf("caller tighten ignored: %d", got.MaxContextTokens)
	}
}

func TestValidateGenerateRequestReportsContextOverflow(t *testing.T) {
	req := GenerateRequest{
		Model:            "test",
		MaxContextTokens: 2048,
		MaxOutputTokens:  1024,
		Messages:         []Message{{Role: RoleUser, Content: "current"}},
		Tools:            []ToolDefinition{{Name: "huge", Description: strings.Repeat("schema ", 3000), Parameters: map[string]any{"type": "object"}}},
	}
	err := validateGenerateRequest(req)
	if !errors.Is(err, ErrContextOverflow) || !IsContextOverflowError(err) {
		t.Fatalf("validation error = %v, want ErrContextOverflow", err)
	}
}
