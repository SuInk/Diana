// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type subtaskCaptureProvider struct {
	requests []llm.GenerateRequest
	reply    string
}

func (p *subtaskCaptureProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	return &llm.GenerateResponse{Text: p.reply}, nil
}

func subtaskRuntime(reply string) (*Runtime, *subtaskCaptureProvider) {
	provider := &subtaskCaptureProvider{reply: reply}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	return runtime, provider
}

func TestSubtaskToolAnswersFromMaterialOnly(t *testing.T) {
	runtime, provider := subtaskRuntime("材料里提到三处不一致：单价、税率、合计。")
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})

	out, err := tool.Run(context.Background(), map[string]any{
		"question": "这份对账单有哪些不一致",
		"material": "单价 12 与 13 不符；税率写了 6% 和 13%；合计对不上。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "三处不一致") {
		t.Fatalf("subtask answer = %q", out)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider calls = %d", len(provider.requests))
	}
	// 子调用只该看到调用方给的素材：没有系统人格提示、没有聊天历史。
	messages := provider.requests[0].Messages
	if len(messages) != 2 {
		t.Fatalf("subtask carried %d messages, want a system rule plus one user turn", len(messages))
	}
	if !strings.Contains(messages[1].Content, "单价 12 与 13 不符") {
		t.Fatalf("material was not passed through: %q", messages[1].Content)
	}
}

func TestSubtaskToolRejectsIncompleteInput(t *testing.T) {
	runtime, provider := subtaskRuntime("ok")
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})

	for _, input := range []map[string]any{
		{"question": "", "material": "素材"},
		{"question": "问题", "material": "  "},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("incomplete input was accepted: %#v", input)
		}
	}
	if len(provider.requests) != 0 {
		t.Fatalf("rejected input still reached the provider: %d calls", len(provider.requests))
	}
}

func TestSubtaskToolRejectsOversizedMaterial(t *testing.T) {
	runtime, provider := subtaskRuntime("ok")
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})

	// 素材大到这个地步说明这件事该留在主回复里，拆出去反而多付一次 prefill。
	oversized := strings.Repeat("材", maximumSubtaskMaterialRunes+1)
	if _, err := tool.Run(context.Background(), map[string]any{"question": "总结", "material": oversized}); err == nil {
		t.Fatal("oversized material was accepted")
	}
	if len(provider.requests) != 0 {
		t.Fatalf("oversized material reached the provider: %d calls", len(provider.requests))
	}
}

func TestSubtaskToolCapsFanOutPerReply(t *testing.T) {
	runtime, provider := subtaskRuntime("结论")
	tool := newDianaSubtaskTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1"})
	input := map[string]any{"question": "总结这段", "material": "一段材料"}

	for index := 0; index < maximumSubtaskCallsPerReply; index++ {
		if _, err := tool.Run(context.Background(), input); err != nil {
			t.Fatalf("call %d failed: %v", index+1, err)
		}
	}
	// 子调用是同步的，放开扇出会把一次回复拖成一串串行往返。
	if _, err := tool.Run(context.Background(), input); err == nil {
		t.Fatal("fan-out cap was not enforced")
	}
	if len(provider.requests) != maximumSubtaskCallsPerReply {
		t.Fatalf("provider calls = %d, want %d", len(provider.requests), maximumSubtaskCallsPerReply)
	}
}

func TestSubtaskToolIsAvailableBelowOwnerTier(t *testing.T) {
	// 它不碰文件、命令和浏览器，只把素材压成一句话，所以普通等级也该有。
	allowed := RelationshipPolicy{}.allowedAgentToolNames()
	if !allowed[dianaSubtaskToolName] {
		t.Fatalf("subtask tool is gated above the ordinary tier: %#v", allowed)
	}
}
