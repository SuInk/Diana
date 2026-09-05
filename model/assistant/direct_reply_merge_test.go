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

type directReplyMergeProvider struct {
	mu           sync.Mutex
	calls        int
	firstStarted chan struct{}
	releaseFirst chan struct{}
	requests     []llm.GenerateRequest
}

func (p *directReplyMergeProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	for _, message := range req.Messages {
		if strings.Contains(message.Content, directReplyTopicPrompt) {
			return &llm.GenerateResponse{Text: `{"relation":"supplement","confidence":0.99}`}, nil
		}
		if strings.Contains(message.Content, "你是机器人回复的发送前审核器") {
			return &llm.GenerateResponse{Text: `{"should_send":true,"confidence":0.99,"account_safe":true,"count_refusal":false}`}, nil
		}
	}
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	if call == 1 || call == 3 {
		return &llm.GenerateResponse{Text: `{"action":"none","prompt":""}`}, nil
	}
	if call == 2 {
		close(p.firstStarted)
		select {
		case <-p.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &llm.GenerateResponse{Text: "只回答第一条"}, nil
	}
	return &llm.GenerateResponse{Text: "两条一起回答"}, nil
}

func (p *directReplyMergeProvider) requestsSnapshot() []llm.GenerateRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.GenerateRequest(nil), p.requests...)
}

func TestDirectReplyMergesSameUserFollowUpAndRegenerates(t *testing.T) {
	disabled := false
	provider := &directReplyMergeProvider{firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		BotAccount: "42", AgentEnabled: false, BotReplyLoopDetectionEnabled: &disabled,
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })

	root := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "root", ToMe: true,
		RawMessage: "中午听嘉然的", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "中午听嘉然的"}}},
	}
	runtime.remember(root)
	done := make(chan error, 1)
	go func() {
		_, err := runtime.replyAndRecord(withOutboundTurn(context.Background(), "root-turn"), root, root.RawMessage, "replied")
		done <- err
	}()
	select {
	case <-provider.firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first reply generation did not start")
	}

	followUp := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "follow-up",
		RawMessage: "吃个干炒牛河好了", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "吃个干炒牛河好了"}}},
	}
	_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), followUp)
	if handled || outcome != "merged_into_reply" {
		t.Fatalf("follow-up handled=%v outcome=%q", handled, outcome)
	}
	close(provider.releaseFirst)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("merged reply did not finish")
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || sent[0].Text != "两条一起回答" {
		t.Fatalf("sent=%#v", sent)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 4 {
		t.Fatalf("provider calls=%d, want first draft plus one regeneration", len(requests))
	}
	joined := ""
	for _, message := range requests[3].Messages {
		joined += "\n" + message.Content
	}
	if !strings.Contains(joined, "当前同轮补充消息") || !strings.Contains(joined, "吃个干炒牛河好了") {
		t.Fatalf("regenerated prompt missed follow-up: %s", joined)
	}
}

func TestDirectReplyMergesNewDirectedFollowUpWithoutDroppingRegeneratedReply(t *testing.T) {
	disabled := false
	provider := &directReplyMergeProvider{firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		BotAccount: "42", AgentEnabled: false, BotReplyLoopDetectionEnabled: &disabled,
	}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })

	root := directedGroupMessage("root", "user-1", "解释下 stdout")
	runtime.noteDirectedInbound(root)
	runtime.remember(root)
	done := make(chan error, 1)
	go func() {
		_, err := runtime.replyAndRecord(withOutboundTurn(context.Background(), "root-turn"), root, root.RawMessage, "replied")
		done <- err
	}()
	select {
	case <-provider.firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first reply generation did not start")
	}

	followUp := directedGroupMessage("follow-up", "user-1", "再举一个 stdout 的例子")
	runtime.noteDirectedInbound(followUp)
	_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), followUp)
	if handled || outcome != "merged_into_reply" {
		t.Fatalf("directed follow-up handled=%v outcome=%q", handled, outcome)
	}
	close(provider.releaseFirst)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("merged directed reply did not finish")
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || sent[0].Text != "两条一起回答" {
		t.Fatalf("sent=%#v", sent)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 4 {
		t.Fatalf("provider calls=%d, want first draft plus one regeneration", len(requests))
	}
	joined := ""
	for _, message := range requests[3].Messages {
		joined += "\n" + message.Content
	}
	if !strings.Contains(joined, "当前同轮补充消息") || !strings.Contains(joined, "再举一个 stdout 的例子") {
		t.Fatalf("regenerated prompt missed directed follow-up: %s", joined)
	}
}

func TestDescribeMergedIntoReplyOutcome(t *testing.T) {
	decision, reason, handled := DescribeEventOutcome("merged_into_reply")
	if decision != "not_replied" || handled || !strings.Contains(reason, "已并入") {
		t.Fatalf("decision=%q reason=%q handled=%v", decision, reason, handled)
	}
}
