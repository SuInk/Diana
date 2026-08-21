package assistant

import (
	"context"
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
	if got != first {
		t.Fatalf("reply = %q, want %q", got, first)
	}
	if strings.HasSuffix(got, "...") {
		t.Fatalf("boundary truncation should not append an ellipsis: %q", got)
	}
	if !strings.HasSuffix(got, "。") {
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
