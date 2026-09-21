// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type decisionOnlyStubProvider struct{ err error }

func (p decisionOnlyStubProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &llm.GenerateResponse{Text: "ok"}, nil
}

func TestDecisionOnlyNoticeNamesThePurpose(t *testing.T) {
	ctx := withLLMUsagePurpose(context.Background(), PurposeMemorySummary)
	run := withDecisionOnlyNoticeRun(ctx, func(provider LLMProvider) (string, error) {
		resp, err := provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "总结一下"}}})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	_, err := run(decisionOnlyStubProvider{err: llm.ErrDecisionRequired})
	if err == nil {
		t.Fatal("expected the refusal to surface")
	}
	if !strings.Contains(err.Error(), PurposeMemorySummary) {
		t.Fatalf("expected the purpose in the message, got %v", err)
	}
	if !errors.Is(err, llm.ErrDecisionRequired) {
		t.Fatalf("expected the cause to stay wrapped, got %v", err)
	}
}

func TestDecisionOnlyNoticeLeavesOtherErrorsAlone(t *testing.T) {
	ctx := withLLMUsagePurpose(context.Background(), PurposeProactiveReplyRouter)
	upstream := errors.New("llm: provider request failed")
	run := withDecisionOnlyNoticeRun(ctx, func(provider LLMProvider) (string, error) {
		resp, err := provider.Generate(ctx, llm.GenerateRequest{})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if _, err := run(decisionOnlyStubProvider{err: upstream}); !errors.Is(err, upstream) {
		t.Fatalf("expected the original error, got %v", err)
	}
	if text, err := run(decisionOnlyStubProvider{}); err != nil || text != "ok" {
		t.Fatalf("expected a clean pass-through, got %q %v", text, err)
	}
}
