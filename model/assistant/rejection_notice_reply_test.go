package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type rejectionRewriteProvider struct {
	rewriteCalls int
	rewrite      llm.GenerateRequest
	rewriteError error
	text         string
}

func (p *rejectionRewriteProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if llmUsagePurposeFromContext(ctx) != "upstream_rejection_notice" {
		return nil, llm.ErrUnverifiedRejection
	}
	p.rewriteCalls++
	p.rewrite = req
	if p.rewriteError != nil {
		return nil, p.rewriteError
	}
	return &llm.GenerateResponse{Text: p.text}, nil
}

func TestRejectionNoticeUsesNaturalModelReplyAndKeepsFailureRecord(t *testing.T) {
	withFastSendTiming(t)
	channel := &recordingChannel{}
	provider := &rejectionRewriteProvider{text: "这次上游没接住，原因还不确定，晚点再试试喵。"}
	runtime := NewRuntime(BotConfig{SystemPrompt: "你叫测试嘉然，语气温和。"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Kind: EventKindPrivate, UserID: "user", MessageID: "notice-rewrite"}
	outcome, err := runtime.replyAndRecord(context.Background(), event, "ORIGINAL_PRIVATE_REQUEST_NOT_FOR_NOTICE", "replied")
	if err != nil || outcome != "error_replied_upstream_rejection" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if len(channel.sent) != 1 || channel.sent[0].Text != provider.text {
		t.Fatalf("sent=%+v", channel.sent)
	}
	if provider.rewriteCalls != 1 || len(provider.rewrite.Tools) != 0 {
		t.Fatalf("rewrite calls=%d tools=%d", provider.rewriteCalls, len(provider.rewrite.Tools))
	}
	var prompt strings.Builder
	for _, m := range provider.rewrite.Messages {
		prompt.WriteString(m.Content)
	}
	if strings.Contains(prompt.String(), "ORIGINAL_PRIVATE_REQUEST_NOT_FOR_NOTICE") || !strings.Contains(prompt.String(), llm.UnverifiedRejectionNotice) || !strings.Contains(prompt.String(), "测试嘉然") {
		t.Fatal("rewrite did not isolate notice or use persona")
	}
	if !strings.Contains(runtime.Status().LastError, llm.ErrUnverifiedRejection.Error()) {
		t.Fatal("original failure diagnostic lost")
	}
}

func TestRejectionRewriteFailureFallsBackOnce(t *testing.T) {
	withFastSendTiming(t)
	for _, tc := range []struct {
		name, text string
		err        error
	}{
		{"error", "", errors.New("rewrite unavailable")},
		{"repeated rejection", llm.UnverifiedRejectionNotice, nil},
		{"empty", "", nil},
		{"internal marker", "[diana-reply:im_message_bad]稍后再试", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &recordingChannel{}
			provider := &rejectionRewriteProvider{text: tc.text, rewriteError: tc.err}
			runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			outcome, err := runtime.replyAndRecord(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "user", MessageID: "fallback"}, "request", "replied")
			if err != nil || outcome != "error_replied_upstream_rejection" {
				t.Fatalf("outcome=%s err=%v", outcome, err)
			}
			if provider.rewriteCalls != 1 || len(channel.sent) != 1 || !strings.Contains(channel.sent[0].Text, "不能据此判断") {
				t.Fatalf("calls=%d sent=%+v", provider.rewriteCalls, channel.sent)
			}
		})
	}
}

func TestRejectionRewriteRespectsDisabledNoticesAndOtherErrors(t *testing.T) {
	provider := &rejectionRewriteProvider{text: "unused"}
	channel := &recordingChannel{}
	disabled := false
	runtime := NewRuntime(BotConfig{ErrorNotifyEnabled: &disabled}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	outcome, err := runtime.replyAndRecord(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "user", MessageID: "disabled"}, "request", "replied")
	if err != nil || outcome != "error_silent" || provider.rewriteCalls != 0 || len(channel.sent) != 0 {
		t.Fatal("disabled notices still rewrote or sent")
	}
	if _, ok := runtime.rewriteRejectionNotice(context.Background(), MessageEvent{}, errors.New("content_policy_violation")); ok || provider.rewriteCalls != 0 {
		t.Fatal("unrelated policy error rewritten")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := runtime.rewriteRejectionNotice(ctx, MessageEvent{}, llm.ErrUnverifiedRejection); ok || provider.rewriteCalls != 0 {
		t.Fatal("canceled request rewritten")
	}
}
