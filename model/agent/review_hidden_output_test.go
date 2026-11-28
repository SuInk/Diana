// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type hiddenOutputRecordingClient struct{ hidden []bool }

func (c *hiddenOutputRecordingClient) Generate(ctx context.Context, _ llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.hidden = append(c.hidden, llm.HiddenOutput(ctx))
	return &llm.GenerateResponse{Text: `{}`}, nil
}

// 复核和主回复共用一个 ctx，ctx 上挂着草稿预览；复核的 JSON 不能被推成 TG 草稿。
func TestReviewCallsMarkOutputHidden(t *testing.T) {
	client := &hiddenOutputRecordingClient{}
	runner := &Runner{client: client, registry: NewToolRegistry()}
	history := []llm.Message{{Role: llm.RoleUser, Content: "查一下"}}
	steps := []Step{{Tool: "lookup", Output: "工具原文"}}
	runner.reviewUnsupportedDetails(context.Background(), history, "查到了", steps)
	if len(client.hidden) != 1 || !client.hidden[0] {
		t.Fatalf("review calls hidden = %v", client.hidden)
	}
}
