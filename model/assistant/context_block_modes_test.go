// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 不认识的块、不认识的档位、不支持按需的块设了按需，都丢掉按总是带；配置往返不丢。
func TestContextBlockModesNormalizeAndRoundTrip(t *testing.T) {
	cfg := BotConfig{ContextBlockModes: map[string]string{
		ContextBlockNotebook:  ContextBlockOnDemand,
		ContextBlockMood:      ContextBlockOff,
		ContextBlockWorldBook: ContextBlockOnDemand,
		"unknown":             ContextBlockOff,
		ContextBlockWallet:    "sometimes",
	}}
	got := ConfigFromPayload(PayloadFromConfig(cfg), BotConfig{}).ContextBlockModes
	if len(got) != 2 || got[ContextBlockNotebook] != ContextBlockOnDemand || got[ContextBlockMood] != ContextBlockOff {
		t.Fatalf("modes = %v", got)
	}
	if contextBlockIncluded(cfg, ContextBlockNotebook) || contextBlockIncluded(cfg, ContextBlockMood) {
		t.Fatal("notebook on demand and mood off must not be included")
	}
	if !contextBlockIncluded(cfg, ContextBlockWorldBook) || !contextBlockIncluded(cfg, ContextBlockWallet) || !contextBlockIncluded(BotConfig{}, ContextBlockCrossGroup) {
		t.Fatal("invalid or unset modes must fall back to always")
	}
}

// 设成按需的块在头部写一句用哪个工具查；没有按需的块就不写这一段。
func TestContextBlockOnDemandPromptInHead(t *testing.T) {
	provider := &appendOnlyProvider{reply: "嗯"}
	runtime := NewRuntime(BotConfig{AgentEnabled: true, BotAccount: "42", ContextBlockModes: map[string]string{
		ContextBlockNotebook: ContextBlockOnDemand, ContextBlockCrossGroup: ContextBlockOnDemand, ContextBlockMood: ContextBlockOff,
	}}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Kind: EventKindGroup, SelfID: "42", GroupID: "20001", UserID: "10101", SenderName: "Alice", MessageID: "m-1",
		Time: interimTestEvent().Time, RawMessage: "Diana 早", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "Diana 早"}}}, proactiveReply: true}
	if _, err := runtime.replyTo(t.Context(), event, "Diana 早"); err != nil {
		t.Fatal(err)
	}
	var head string
	for _, message := range provider.requests[0].Messages {
		if strings.Contains(message.Content, "【按需查的内容】") {
			head = message.Content
		}
		if strings.Contains(message.Content, "【本轮语气】") {
			t.Fatal("mood is off but the tone section was sent")
		}
	}
	if !strings.Contains(head, "chat_history（scope=all_groups）") || !strings.Contains(head, "notebook") || strings.Contains(head, "wallet") {
		t.Fatalf("on-demand prompt = %q", head)
	}
	if contextBlockOnDemandPrompt(BotConfig{}) != "" {
		t.Fatal("no on-demand blocks should write nothing")
	}
}
