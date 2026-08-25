package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestParseProactiveReplyQualityDecision(t *testing.T) {
	decision, ok := parseProactiveReplyQualityDecision("```json\n{\"should_send\":true,\"confidence\":0.96,\"reason\":\"直接回答\"}\n```")
	if !ok || !decision.ShouldSend || decision.Confidence != 0.96 || decision.Reason != "直接回答" {
		t.Fatalf("decision = %#v, ok = %v", decision, ok)
	}
	if _, ok := parseProactiveReplyQualityDecision(`{"should_send":true,"confidence":1.2}`); ok {
		t.Fatal("out-of-range confidence should be rejected")
	}
}

func TestJudgeProactiveReplyQualityRejectsLowConfidence(t *testing.T) {
	provider := &qualityTestProvider{reply: `{"should_send":true,"confidence":0.72,"reason":"回答方向不够确定"}`}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		ProactiveReplyThreshold: 0.9,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	err := runtime.judgeProactiveReplyQuality(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u"}, "这个怎么处理？", "可以试试看。", runtime.Config())
	if err == nil || !strings.Contains(err.Error(), "置信度 72%") {
		t.Fatalf("quality error = %v", err)
	}
}

func TestJudgeProactiveReplyQualityAllowsQualifiedReply(t *testing.T) {
	provider := &qualityTestProvider{reply: `{"should_send":true,"confidence":0.95,"reason":"回答直接且有依据"}`}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		ProactiveReplyThreshold: 0.9,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	if err := runtime.judgeProactiveReplyQuality(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u"}, "这个怎么处理？", "先检查错误日志。", runtime.Config()); err != nil {
		t.Fatalf("qualified reply rejected: %v", err)
	}
	if len(provider.requests) != 1 || len(provider.requests[0].Messages) != 2 {
		t.Fatalf("quality request = %#v", provider.requests)
	}
	if !strings.Contains(provider.requests[0].Messages[1].Content, "candidate_reply") {
		t.Fatalf("quality payload missing candidate reply: %q", provider.requests[0].Messages[1].Content)
	}
}

type qualityTestProvider struct {
	mu       sync.Mutex
	reply    string
	requests []llm.GenerateRequest
}

func (p *qualityTestProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: p.reply}, nil
}

func TestNormalizeReplyTruncatesAtSentenceBoundary(t *testing.T) {
	first := strings.Repeat("甲", 19) + "。"
	reply := first + strings.Repeat("乙", 30) + "。"
	got := normalizeReply(reply, 30)
	// 这条测的是「在句尾收束而不是硬切」；收尾那个句号由 normalizeReply 一并去掉，
	// 聊天消息不带句号收尾（见 trimChatTrailingPeriod）。
	if want := strings.TrimSuffix(first, "。"); got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
	if strings.HasSuffix(got, "...") {
		t.Fatalf("boundary truncation should not append an ellipsis: %q", got)
	}
	// 句号被去掉之后，「没切在半句上」要换个方式验证：截断点后面紧跟的就该是句号，
	// 说明这一刀正好落在句尾。
	if !strings.HasPrefix(reply, got+"。") {
		t.Fatalf("reply was cut mid-sentence: %q", got)
	}
	if len([]rune(got)) > 30 {
		t.Fatalf("reply exceeds the limit: %q", got)
	}
}

func TestNormalizeReplyFallsBackToHardTruncation(t *testing.T) {
	reply := strings.Repeat("字", 100)
	got := normalizeReply(reply, 20)
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("reply without any sentence boundary should keep the ellipsis: %q", got)
	}
}

// 审核器拿不到群聊历史,却被要求判断「有没有依据」——线上真实误杀:群里问
// 「评价一下群友的 gay 度」,回复按前面的发言逐个点评,审核器看不到那些发言,
// 就以「原消息未提供群友名单」为由拒发。提示词必须把事实核查明确划出职责,
// 只留下看得见的表达维度。
func TestProactiveReplyQualityPromptJudgesOnlyObservableDimensions(t *testing.T) {
	prompt := proactiveReplyQualityPrompt
	for _, must := range []string{"你看不到群聊历史", "严禁以", "无法核实", "判断事实真伪不是你的职责", "倾向放行"} {
		if !strings.Contains(prompt, must) {
			t.Fatalf("提示词缺少 %q:%s", must, prompt)
		}
	}
	// 事实核查类的判据不该再作为拒绝理由留在提示词里。
	for _, forbidden := range []string{"明显幻觉", "无依据断言"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("提示词仍把 %q 当拒绝理由:%s", forbidden, prompt)
		}
	}
	// 看得见的维度要留着,否则截断和空洞回复会被放行。
	for _, must := range []string{"答非所问", "被截断", "空洞", "说话方式"} {
		if !strings.Contains(prompt, must) {
			t.Fatalf("提示词丢了可判断维度 %q:%s", must, prompt)
		}
	}
}

// 账号安全是一票否决：表达质量再高、置信度再高也拦。
func TestJudgeProactiveReplyRejectsAccountUnsafeContent(t *testing.T) {
	provider := &qualityTestProvider{reply: `{"should_send":true,"confidence":0.99,"reason":"口吻自然","account_safe":false,"account_risk":"politics"}`}
	runtime := NewRuntime(BotConfig{
		BotAccount:              "42",
		ProactiveReplyThreshold: 0.9,
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	err := runtime.judgeProactiveReplyQuality(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u"}, "怎么看这事？", "（涉政内容）", runtime.Config())
	if err == nil {
		t.Fatal("account-unsafe reply must be rejected even at high confidence")
	}
	if !strings.Contains(err.Error(), "账号安全") || !strings.Contains(err.Error(), "涉政内容") {
		t.Fatalf("error should name the account-safety reason: %v", err)
	}
	var safetyErr *replyAccountSafetyRejectedError
	if !errors.As(err, &safetyErr) {
		t.Fatalf("error type = %T, want a dedicated account-safety error", err)
	}
}

// 模型没返回 account_safe 时按安全处理：缺字段就拦会让机器人集体哑火。
func TestReplyAuditTreatsMissingAccountSafeAsSafe(t *testing.T) {
	decision, ok := parseProactiveReplyQualityDecision(`{"should_send":true,"confidence":0.95}`)
	if !ok {
		t.Fatal("decision should still parse without the account fields")
	}
	if !decision.AccountSafe {
		t.Fatal("missing account_safe must default to safe")
	}
	if err := accountSafetyError(decision); err != nil {
		t.Fatalf("safe decision produced an error: %v", err)
	}
}

// 直接回复的安全审核默认关闭：主动回复那次审核是顺带的，直接回复要额外一次调用。
func TestAuditReplyAccountSafetyIsOptInForDirectReplies(t *testing.T) {
	provider := &qualityTestProvider{reply: `{"should_send":true,"confidence":0.99,"reason":"ok","account_safe":false,"account_risk":"explicit"}`}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u"}

	if err := runtime.auditReplyAccountSafety(context.Background(), event, "在吗", "任何内容", runtime.Config()); err != nil {
		t.Fatalf("audit must be off by default: %v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("disabled audit must not call the model: %d requests", len(provider.requests))
	}

	cfg := runtime.Config()
	cfg.ReplyAccountSafetyAuditEnabled = boolPointer(true)
	err := runtime.auditReplyAccountSafety(context.Background(), event, "在吗", "任何内容", cfg)
	if err == nil || !strings.Contains(err.Error(), "露骨内容") {
		t.Fatalf("enabled audit should reject explicit content: %v", err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("enabled audit should make exactly one call: %d", len(provider.requests))
	}
}

// 审核本身失败时放行：模型不可用不该让机器人整个哑掉。
func TestAuditReplyAccountSafetyFailsOpen(t *testing.T) {
	provider := &qualityTestProvider{reply: "not json at all"}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	cfg := runtime.Config()
	cfg.ReplyAccountSafetyAuditEnabled = boolPointer(true)
	if err := runtime.auditReplyAccountSafety(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u"}, "在吗", "在的", cfg); err != nil {
		t.Fatalf("unparsable audit result must fail open: %v", err)
	}
}
