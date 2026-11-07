// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// asSendReply 把假模型脚本里的纯文本回复改成一次 send 调用。主回复只认 send（见
// agent.Config.PlainTextNotSent）：请求带着 send 工具时纯文本会被退回、再写就不发，
// 这些用例测的是别的行为，脚本里写纯文本只是图省事。不带 send 的请求（意图路由、
// 审核、改写这类决策调用）和 JSON 脚本原样返回。
func asSendReply(req llm.GenerateRequest, resp *llm.GenerateResponse) *llm.GenerateResponse {
	text := strings.TrimSpace(resp.Text)
	if len(resp.ToolCalls) > 0 || text == "" || strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") || !requestOffersSend(req) {
		return resp
	}
	out := *resp
	out.Text = ""
	out.ToolCalls = []llm.ToolCall{{ID: "fake_send", Name: agent.FinalizeToolName, Arguments: map[string]any{"text": resp.Text}}}
	return &out
}

func requestOffersSend(req llm.GenerateRequest) bool {
	for _, tool := range req.Tools {
		if tool.Name == agent.FinalizeToolName {
			return true
		}
	}
	return false
}

// sendWrappingProvider 给只会回纯文本的假模型套一层 asSendReply。
type sendWrappingProvider struct{ LLMProvider }

func (p sendWrappingProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	resp, err := p.LLMProvider.Generate(ctx, req)
	if err != nil || resp == nil {
		return resp, err
	}
	return asSendReply(req, resp), nil
}
