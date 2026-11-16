// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type keepaliveTestProvider struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
	purposes []string
}

func (p *keepaliveTestProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	p.purposes = append(p.purposes, llmUsagePurposeFromContext(ctx))
	return &llm.GenerateResponse{Text: "好"}, nil
}

func (p *keepaliveTestProvider) snapshot() ([]llm.GenerateRequest, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.GenerateRequest(nil), p.requests...), append([]string(nil), p.purposes...)
}

// 主回复之后空闲时按间隔原样重发那次请求，只改输出上限、用途记成 reply_keepalive；
// 窗口过了就停，会话也忘掉。别的用途的调用不保活。
func TestReplyKeepalivePingsLastReplyUntilWindowEnds(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.replyKeepalives.interval, runtime.replyKeepalives.window = 20*time.Millisecond, 70*time.Millisecond
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "m1"}
	inner := &keepaliveTestProvider{}
	provider := &replyKeepaliveLLMProvider{runtime: runtime, event: event, provider: inner}
	request := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "头部"}, {Role: llm.RoleUser, Content: "在吗"}},
		Tools: []llm.ToolDefinition{{Name: "send"}}, PromptCacheKey: "diana-k", MaxOutputTokens: 4096}

	ctx := withLLMUsagePurpose(withLLMUsageContext(context.Background(), event), PurposeMemoryExtract)
	if _, err := provider.Generate(ctx, request); err != nil {
		t.Fatal(err)
	}
	ctx = withLLMUsagePurpose(withLLMUsageContext(context.Background(), event), PurposeReply)
	if _, err := provider.Generate(ctx, request); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	requests, purposes := inner.snapshot()
	pings := requests[2:]
	// 70ms 窗口、20ms 间隔：20、40、60ms 各一次，下一次就过窗口了。
	if len(pings) != 3 {
		t.Fatalf("pings = %d, want 3 (purposes %v)", len(pings), purposes)
	}
	for index, ping := range pings {
		if purposes[index+2] != PurposeReplyKeepalive || ping.MaxOutputTokens != replyKeepaliveMaxOutput {
			t.Fatalf("ping %d purpose=%q max_output=%d", index, purposes[index+2], ping.MaxOutputTokens)
		}
		want := request
		want.MaxOutputTokens = replyKeepaliveMaxOutput
		if !reflect.DeepEqual(ping, want) {
			t.Fatalf("ping %d must resend the exact reply request: %#v", index, ping)
		}
	}
	runtime.replyKeepalives.mu.Lock()
	left := len(runtime.replyKeepalives.sessions)
	runtime.replyKeepalives.mu.Unlock()
	if left != 0 {
		t.Fatalf("an expired session must be forgotten, %d left", left)
	}
}

// 新的主回复把计时和要重发的请求都换掉。
func TestReplyKeepaliveRestartsOnNewReply(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.replyKeepalives.interval, runtime.replyKeepalives.window = 40*time.Millisecond, 100*time.Millisecond
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "m1"}
	inner := &keepaliveTestProvider{}
	first := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "第一轮"}}}
	second := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "第二轮"}}}
	runtime.replyKeepalives.note(runtime, "k", event, first, inner)
	time.Sleep(25 * time.Millisecond)
	runtime.replyKeepalives.note(runtime, "k", event, second, inner)
	time.Sleep(55 * time.Millisecond)
	requests, _ := inner.snapshot()
	if len(requests) != 1 || requests[0].Messages[0].Content != "第二轮" {
		t.Fatalf("the newer reply must reset the timer and be the one resent: %#v", requests)
	}
}
