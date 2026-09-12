// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type recordingProvider struct {
	called bool
	req    llm.GenerateRequest
}

func (p *recordingProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.called = true
	p.req = req
	return &llm.GenerateResponse{Text: "ok"}, nil
}

func (p *recordingProvider) Stream(ctx context.Context, req llm.GenerateRequest) (<-chan llm.ChatEvent, error) {
	out := make(chan llm.ChatEvent)
	close(out)
	return out, nil
}

// 压不进预算时不能让整轮失败。线上抓到的多是差几十个 token：一次限额 118656、
// 压完 118710，只超 54，结果整轮报「压缩未能完成」，用户什么也收不到。供应商客户端
// 发请求前本来就会按自己的上下文上限裁剪，放行交给那一层即可。
func TestOverBudgetRequestStillReachesProvider(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	provider := &recordingProvider{}
	budgeted := &imageBudgetProvider{runtime: runtime, provider: provider, group: llm.GroupChat}

	// 一条压不动的超长历史：没有可用的摘要器，压缩必然凑不够。
	huge := strings.Repeat("这是一段很长的历史内容。", 40000)
	_, err := budgeted.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "系统提示"},
		{Role: llm.RoleUser, Content: huge},
		{Role: llm.RoleUser, Content: "那这个怎么办"},
	}})
	if err != nil {
		t.Fatalf("超预算不该让整轮失败：%v", err)
	}
	if !provider.called {
		t.Fatal("请求没有交给供应商，整轮被就地丢掉了")
	}
}
