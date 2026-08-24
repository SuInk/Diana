// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

var errTestToolsUnsupported = errors.New("llm: provider request failed: tools are not supported")

// memoryToolProvider answers the gate with a native tool call, the way a
// provider with function calling does.
type memoryToolProvider struct {
	request llm.GenerateRequest
}

func (p *memoryToolProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.request = req
	return &llm.GenerateResponse{
		Provider: llm.ProviderOpenAICompatible,
		Model:    "test",
		ToolCalls: []llm.ToolCall{{ID: "call-1", Name: memorySubmitToolName, Arguments: map[string]any{
			"memories": []any{map[string]any{
				"action": "upsert", "key": "preference.food.spicy", "kind": "preference",
				"topic": "饮食偏好", "content": "Alice不吃辣", "source_type": "explicit",
				"confidence": 0.99, "importance": 0.7, "visibility": "session", "sensitive": false,
			}},
		}}},
	}, nil
}

func TestMemoryGateDecodesNativeToolSubmission(t *testing.T) {
	provider := &memoryToolProvider{}
	raw, err := generateMemoryCandidates(context.Background(), provider,
		[]llm.Message{{Role: llm.RoleUser, Content: "我现在不吃辣了"}}, memoryGateSubmitTool())
	if err != nil {
		t.Fatal(err)
	}
	if provider.request.ToolChoice != memorySubmitToolName || len(provider.request.Tools) != 1 {
		t.Fatalf("request=%#v", provider.request)
	}
	candidates, err := parseMemoryCandidates(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Key != "preference.food.spicy" || candidates[0].Kind != MemoryKindPreference {
		t.Fatalf("candidates=%#v", candidates)
	}
}

// memoryTextProvider rejects tool requests, the way a small model without
// function calling does, and answers the retry with the legacy envelope.
type memoryTextProvider struct {
	calls int
}

func (p *memoryTextProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls++
	if len(req.Tools) > 0 {
		return nil, errTestToolsUnsupported
	}
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test",
		Text: "```json\n{\"memories\":[{\"action\":\"upsert\",\"key\":\"profile.pet.cat\",\"kind\":\"fact\",\"topic\":\"宠物\",\"content\":\"Alice养了一只猫\",\"source_type\":\"explicit\",\"confidence\":0.95,\"importance\":0.6,\"visibility\":\"session\",\"sensitive\":false}]}\n```"}, nil
}

func TestMemoryGateFallsBackWhenToolsAreRejected(t *testing.T) {
	provider := &memoryTextProvider{}
	raw, err := generateMemoryCandidates(context.Background(), provider,
		[]llm.Message{{Role: llm.RoleUser, Content: "我养了一只猫"}}, memoryGateSubmitTool())
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 {
		t.Fatalf("calls=%d, want one tool attempt and one plain retry", provider.calls)
	}
	candidates, err := parseMemoryCandidates(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Key != "profile.pet.cat" {
		t.Fatalf("candidates=%#v", candidates)
	}
}
