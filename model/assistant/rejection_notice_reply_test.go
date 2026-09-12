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

// 账号安全拦截的内部理由里写着「候选回复评价了什么」——那正是被拦下来的内容摘要。
// 对外只能给中性说法，内部理由留在事件记录和日志里。
func TestAccountSafetyNoticeHidesInternalReason(t *testing.T) {
	internal := "候选回复直接评价某敏感政治事件并作立场性判断"
	err := &replyAccountSafetyRejectedError{reason: "回复未通过账号安全审核（politics）：" + internal}

	public := publicChatErrorMessage(err)
	if public != accountSafetyPublicNotice {
		t.Fatalf("public = %q", public)
	}
	if strings.Contains(public, internal) || strings.Contains(public, "politics") {
		t.Fatalf("内部理由或风险类别泄漏到了聊天文案里：%q", public)
	}

	source, prompt, purpose, ok := rejectionNoticeRewriteSource(err)
	if !ok || purpose != "account_safety_notice" {
		t.Fatalf("source ok=%v purpose=%q", ok, purpose)
	}
	// 交给改写模型的同样只有中性文案，不给它被拦内容的任何线索。
	if source != accountSafetyPublicNotice || strings.Contains(source, internal) {
		t.Fatalf("改写来源带上了内部理由：%q", source)
	}
	if !strings.Contains(prompt, "不要复述、概括或暗示被拦下的内容") {
		t.Fatalf("改写提示词没有禁止复述被拦内容：%q", prompt)
	}
}

// 改写只是换个说法。模型调用失败时必须退回固定文案，而不是把这条提示丢掉——
// 用户那边的观感是「说了话没反应」，比一句生硬的提示糟得多。
func TestAccountSafetyNoticeFallsBackToFixedTextWhenRewriteFails(t *testing.T) {
	provider := &rejectionRewriteProvider{rewriteError: errors.New("rewrite model down")}
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	err := &replyAccountSafetyRejectedError{reason: "回复未通过账号安全审核（politics）：内部理由"}

	rewritten, ok := runtime.rewriteRejectionNotice(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "u"}, err)
	if ok || rewritten != "" {
		t.Fatalf("改写失败时不该给出结果：rewritten=%q ok=%v", rewritten, ok)
	}
	// 调用点据此保留原文案，用户仍然会收到一句说明。
	if publicChatErrorMessage(err) == "" {
		t.Fatal("退回的固定文案不能为空")
	}
}

// 上游拒绝那条路径的用途标签不能被这次改动串掉：运行日志靠它区分两种改写。
func TestUpstreamRejectionRewriteKeepsItsOwnPurpose(t *testing.T) {
	source, prompt, purpose, ok := rejectionNoticeRewriteSource(llm.ErrUnverifiedRejection)
	if !ok || purpose != "upstream_rejection_notice" || source != llm.UnverifiedRejectionNotice {
		t.Fatalf("source=%q purpose=%q ok=%v", source, purpose, ok)
	}
	if prompt != rejectionNoticeRewritePrompt {
		t.Fatal("上游拒绝用错了改写提示词")
	}
	// 普通错误现在也交给改写：给模型的是 publicChatErrorMessage 的结果，
	// 也就是不改写时会原样发进聊天的那句，经手改写不多暴露任何东西。
	source, _, purpose, ok = rejectionNoticeRewriteSource(errors.New("普通错误"))
	if !ok || purpose != PurposeErrorNotice || source == "" {
		t.Fatalf("普通错误应当走通用改写：source=%q purpose=%q ok=%v", source, purpose, ok)
	}
	// 模型本身用不了时不做无谓的尝试：那只会等满超时再退回原文。
	for _, down := range []error{
		errors.New("llm: provider request failed: Error 429, All accounts exhausted"),
		errors.New("context deadline exceeded"),
		errors.New("502 bad gateway"),
	} {
		if _, _, _, ok := rejectionNoticeRewriteSource(down); ok {
			t.Fatalf("模型不可用时不该再调改写：%v", down)
		}
	}
}
