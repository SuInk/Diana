// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// parallelToneProvider 让改写和审核互相等对方开始：两者串行时谁都等不到，只能超时。
type parallelToneProvider struct {
	mu           sync.Mutex
	toneStarted  chan struct{}
	auditStarted chan struct{}
	auditReply   string
	toneCanceled bool
	purposes     []string
}

func newParallelToneProvider(auditReply string) *parallelToneProvider {
	return &parallelToneProvider{toneStarted: make(chan struct{}), auditStarted: make(chan struct{}), auditReply: auditReply}
}

func (p *parallelToneProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	purpose := llmUsagePurposeFromContext(ctx)
	p.mu.Lock()
	p.purposes = append(p.purposes, purpose)
	p.mu.Unlock()
	isAudit := false
	for _, message := range req.Messages {
		if strings.Contains(message.Content, "你是机器人回复的发送前审核器") {
			isAudit = true
		}
	}
	switch {
	case isAudit:
		close(p.auditStarted)
		select {
		case <-p.toneStarted:
		case <-time.After(2 * time.Second):
		}
		return &llm.GenerateResponse{Text: p.auditReply}, nil
	case purpose == PurposeReplyTone:
		close(p.toneStarted)
		select {
		case <-p.auditStarted:
		case <-time.After(2 * time.Second):
			return &llm.GenerateResponse{Text: "审核没有并行"}, nil
		}
		// 审核拦下时这条改写会被取消；没被取消就照常交稿。
		select {
		case <-ctx.Done():
			p.mu.Lock()
			p.toneCanceled = true
			p.mu.Unlock()
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		return &llm.GenerateResponse{Text: "改写后的回答"}, nil
	case purpose == PurposeReply:
		return &llm.GenerateResponse{Text: "草稿回答"}, nil
	}
	return &llm.GenerateResponse{Text: `{"action":"none","prompt":""}`}, nil
}

func runParallelToneReply(t *testing.T, provider *parallelToneProvider) ([]OutgoingMessage, error) {
	t.Helper()
	disabled := false
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		BotAccount: "42", AgentEnabled: false, BotReplyLoopDetectionEnabled: &disabled, ReplyComposer: "maibot",
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "m1", ToMe: true,
		RawMessage: "今天吃什么", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "今天吃什么"}}},
	}
	runtime.remember(event)
	_, err := runtime.replyAndRecord(withOutboundTurn(context.Background(), "m1-turn"), event, event.RawMessage, "replied")
	return channel.sentSnapshot(), err
}

func TestMaiBotToneRewriteRunsAlongsideSendAudit(t *testing.T) {
	provider := newParallelToneProvider(`{"send_confidence":0.99,"account_safe":true,"count_refusal":false}`)
	start := time.Now()
	sent, err := runParallelToneReply(t, provider)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("tone rewrite and send audit ran one after another: %s", elapsed)
	}
	if len(sent) != 1 || sent[0].Text != "改写后的回答" {
		t.Fatalf("sent=%#v, want the rewritten reply", sent)
	}
}

func TestMaiBotToneRewriteIsCanceledWhenAuditBlocksReply(t *testing.T) {
	provider := newParallelToneProvider(`{"send_confidence":0.99,"account_safe":false,"account_risk":"explicit"}`)
	// 拦下后另有一条账号安全提示（account_safety_notice）照常发出，这里只管草稿和改写稿都不能发。
	sent, _ := runParallelToneReply(t, provider)
	for _, message := range sent {
		if message.Text == "改写后的回答" || message.Text == "草稿回答" {
			t.Fatalf("blocked reply was sent: %#v", sent)
		}
	}
	provider.mu.Lock()
	canceled := provider.toneCanceled
	provider.mu.Unlock()
	if !canceled {
		t.Fatal("pending tone rewrite should be canceled once the audit blocks the reply")
	}
}
