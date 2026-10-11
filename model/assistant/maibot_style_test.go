package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/SuInk/diana/internal/maibotreply"
	"github.com/SuInk/diana/model/llm"
)

func TestMaiBotStyleLivesInIsolatedReplyerRequest(t *testing.T) {
	cfg := (BotConfig{ReplyComposer: "maibot", SystemPrompt: "# 测试人设\n保留完整身份", AgentEnabled: false}).WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	head, tail := runtime.systemPromptPartsWithRelationshipAndAgentTools(MessageEvent{Kind: EventKindGroup}, nil, false, RelationshipPolicy{}, false, nil)
	joined := head + "\n" + tail
	if !strings.Contains(joined, "保留完整身份") {
		t.Fatal("content pass lost identity")
	}
	for _, forbidden := range []string{maibotreply.DefaultReplyStyle, maiBotCleanVocabulary, maiBotNoParentheticalLaugh, maiBotTrailingPeriodPreference, cfg.prompt(promptChineseSlangSpec)} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("content pass unexpectedly contains replyer style %q", forbidden)
		}
	}
	event := MessageEvent{Kind: EventKindGroup, MessageID: "m2", UserID: "u2", SenderName: "群友", RawMessage: "太啰嗦了"}
	request := runtime.maiBotReplyerMessages(cfg, event, []llm.Message{
		{Role: llm.RoleSystem, Content: "large-content-system", Priority: llm.MessagePrioritySystem},
		{Role: llm.RoleUser, Content: "真实群聊", Priority: llm.MessagePriorityHistory},
		{Role: llm.RoleUser, Content: event.RawMessage, Priority: llm.MessagePriorityCurrent},
	}, "别优化我，我少说两句")
	if len(request) < 5 || request[0].Role != llm.RoleSystem {
		t.Fatalf("unexpected replyer request: %#v", request)
	}
	for _, required := range []string{"保留完整身份", maibotreply.DefaultReplyStyle, "最好一次对一个话题进行回复", maiBotCleanVocabulary, maiBotNoParentheticalLaugh, maiBotTrailingPeriodPreference} {
		if !strings.Contains(request[0].Content, required) {
			t.Fatalf("replyer prompt lost %q", required)
		}
		if strings.Count(request[0].Content, required) != 1 {
			t.Fatalf("replyer prompt contains %q more than once", required)
		}
	}
	all := ""
	for _, message := range request {
		all += message.Content
	}
	if strings.Contains(all, "large-content-system") || !strings.Contains(all, "真实群聊") || !strings.Contains(all, "别优化我，我少说两句") || !strings.Contains(all, maiBotReferenceBoundary) {
		t.Fatalf("replyer boundary is wrong: %s", all)
	}
}

type maiBotStyleTestProvider struct {
	requests  []llm.GenerateRequest
	responses []string
}

func (p *maiBotStyleTestProvider) Generate(_ context.Context, request llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.requests = append(p.requests, request)
	index := len(p.requests) - 1
	if index >= len(p.responses) {
		return nil, fmt.Errorf("unexpected model call %d", index+1)
	}
	return &llm.GenerateResponse{Text: p.responses[index]}, nil
}

func TestMaiBotReplyerRunsAfterContentPassAndKeepsDeliveryMarkers(t *testing.T) {
	cfg := (BotConfig{ReplyComposer: "maibot", SystemPrompt: "身份", BotAccount: "bot"}).WithDefaults()
	cfg.AgentEnabled = false
	provider := &maiBotStyleTestProvider{responses: []string{
		"[diana-reply:12345][diana-at:10002] 在海边基站会来回切[diana-msg]别优化我，我少说两句",
		"[diana-reply:99999][diana-at:99999]别优化我，我少说两句呜呜[diana-msg]",
	}}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Kind: EventKindGroup, MessageID: "12345", UserID: "10001", SenderName: "群友", RawMessage: "你太啰嗦了"}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "content-system-marker", Priority: llm.MessagePrioritySystem},
		{Role: llm.RoleUser, Content: "前面的真实群聊", Priority: llm.MessagePriorityHistory},
		{Role: llm.RoleUser, Content: event.RawMessage, Priority: llm.MessagePriorityCurrent},
	}
	reply, err := runtime.generateReply(context.Background(), cfg, event, RelationshipPolicy{}, messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("model calls=%d, want content + replyer", len(provider.requests))
	}
	second := provider.requests[1]
	if len(second.Tools) != 0 || len(second.Messages) == 0 || strings.Contains(second.Messages[0].Content, "content-system-marker") {
		t.Fatalf("replyer was not isolated: %#v", second)
	}
	if !strings.Contains(reply, "[diana-reply:12345]") || !strings.Contains(reply, "[diana-at:10002]") || !strings.Contains(reply, "别优化我，我少说两句呜呜") {
		t.Fatalf("final reply lost content or delivery markers: %q", reply)
	}
	if strings.Contains(reply, "99999") {
		t.Fatalf("replyer changed delivery targets: %q", reply)
	}
}

func TestMaiBotStyleConfigRoundTripAndCustomStyle(t *testing.T) {
	cfg := (BotConfig{ReplyComposer: "maibot", MaiBotReplyStyle: "  像熟人一样轻松说话  "}).WithDefaults()
	if cfg.MaiBotReplyStyle != "像熟人一样轻松说话" {
		t.Fatalf("style=%q", cfg.MaiBotReplyStyle)
	}
	payload := PayloadFromConfig(cfg)
	got := ConfigFromPayload(payload, BotConfig{}).WithDefaults()
	if got.ReplyComposer != "maibot" || got.MaiBotReplyStyle != cfg.MaiBotReplyStyle {
		t.Fatalf("round trip lost MaiBot config: %#v", got)
	}
	if payload.ReplyToneDefault != DefaultReplyTone {
		t.Fatalf("payload default tone=%q", payload.ReplyToneDefault)
	}
	if maiBotStyle(got) != cfg.MaiBotReplyStyle || maiBotStyle(BotConfig{}) != maibotreply.DefaultReplyStyle {
		t.Fatal("custom or default style was not selected")
	}
}
