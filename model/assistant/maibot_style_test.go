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
		"<diana-quote id=\"12345\"/><diana-at id=\"10002\"/> 在海边基站会来回切<diana-message/>别优化我，我少说两句",
		"<diana-quote id=\"99999\"/><diana-at id=\"99999\"/>别优化我，我少说两句呜呜<diana-message/>",
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
	if !strings.Contains(reply, "<diana-quote id=\"12345\"/>") || !strings.Contains(reply, "<diana-at id=\"10002\"/>") || !strings.Contains(reply, "别优化我，我少说两句呜呜") {
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

// 生产回放：草稿分成清单、结论、来源三条，改写稿用空行分段。改写只换措辞，三条
// 还得是三条，句子之间的句号也不能被换成空格。
func TestMaiBotReplyerKeepsDraftMessagesAndLayout(t *testing.T) {
	cfg := (BotConfig{ReplyComposer: "maibot", SystemPrompt: "身份", BotAccount: "bot"}).WithDefaults()
	cfg.AgentEnabled = false
	draft := "Mac Studio 各代内存带宽：<diana-br/>- 2022 款：M1 Max 400GB/s<diana-br/>- 2023 款：M2 Max 400GB/s<diana-message/>规律很明显。<diana-message/>来源：<diana-br/>https://support.apple.com/en-us/111900"
	rendered := "查了下 Apple 官方规格页，Mac Studio 各代内存带宽：\n\n- 2022 款：M1 Max 400GB/s\n- 2023 款：M2 Max 400GB/s\n\n规律挺明显，Max 档一直卡在 400 上下，Ultra 双芯拼接直接翻倍到 800 档；M4 Max 高配涨到 546，还是被丐版 M3 Ultra 的 819 压着。跑大模型吃带宽的话 Ultra 依然独一档\n\n来源：\nhttps://support.apple.com/en-us/111900"
	provider := &maiBotStyleTestProvider{responses: []string{draft, rendered}}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Kind: EventKindPrivate, MessageID: "71", UserID: "10001", SenderName: "群友", RawMessage: "查下mac studio各个版本的内存带宽"}
	messages := []llm.Message{{Role: llm.RoleUser, Content: event.RawMessage, Priority: llm.MessagePriorityCurrent}}
	reply, err := runtime.generateReply(context.Background(), cfg, event, RelationshipPolicy{}, messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	var reference string
	for _, message := range provider.requests[1].Messages {
		if strings.Contains(message.Content, "规律很明显") {
			reference = message.Content
		}
	}
	if strings.Contains(reference, "[diana-") || !strings.Contains(reference, "400GB/s\n\n规律很明显") || !strings.Contains(reference, "\n- 2023 款") {
		t.Fatalf("replyer reference kept raw markers or lost layout: %q", reference)
	}
	bubbles := splitChatReply(reply, chatSplitLimits{})
	if len(bubbles) != 3 {
		t.Fatalf("bubbles = %q, want list, conclusion and sources", bubbles)
	}
	if !strings.Contains(bubbles[0], "内存带宽：\n• 2022 款") || !strings.Contains(bubbles[1], "压着。跑大模型") || !strings.Contains(bubbles[2], "来源：\nhttps://") {
		t.Fatalf("bubbles lost layout: %q", bubbles)
	}
}

// 草稿只有一条时，改写稿的空行不能凭空多拆出几条。
func TestMaiBotReplyLinesKeepsSingleDraftInOneMessage(t *testing.T) {
	got := maiBotReplyLines("第一段\n\n第二段", DefaultBotConfig(), MessageEvent{}, replyControlIntent{}, []string{"第一段第二段"})
	if got != "第一段"+notificationLineMarker+"第二段" {
		t.Fatalf("maiBotReplyLines() = %q", got)
	}
}

// swe-2 生产回放的真实输出：清单每项之间都空一行。按草稿对齐，清单仍是一条，
// 结论和来源各一条，不能拆成五个气泡。
func TestMaiBotReplyLinesAlignsParagraphsToDraftMessages(t *testing.T) {
	draft := []string{
		"Mac Studio 各代内存带宽（Apple 官方规格页）：<diana-br/>2022 款：M1 Max 400GB/s，M1 Ultra 800GB/s<diana-br/>2023 款：M2 Max 400GB/s，M2 Ultra 800GB/s<diana-br/>2025 款：M4 Max 410GB/s（14 核 CPU 版）/ 546GB/s（16 核 CPU 高配版），M3 Ultra 819GB/s",
		"规律很明显：Max 档一直卡在 400 上下，Ultra 靠双芯拼接直接翻倍到 800 档；M4 Max 高配总算涨到 546，但还是被丐版 M3 Ultra 的 819 压着打，跑大模型吃带宽的话 Ultra 依然是独一档。",
		"来源：Apple Support 技术规格页<diana-br/>https://support.apple.com/en-us/111900<diana-br/>https://support.apple.com/en-us/111835",
	}
	rendered := "查了苹果官网的规格页，各代 Mac Studio 内存带宽：\n\n**2022 款**：M1 Max 400GB/s，M1 Ultra 800GB/s\n\n**2023 款**：M2 Max 400GB/s，M2 Ultra 800GB/s\n\n**2025 款**：M4 Max 410GB/s（14 核 CPU 版）/ 546GB/s（16 核高配版），M3 Ultra 819GB/s\n\n规律很明显，Max 档一直卡在 400 上下，Ultra 靠双芯拼接直接翻倍到 800 档。M4 Max 高配涨到 546，但还是被丐版 M3 Ultra 的 819 压着\n\n来源是 Apple Support 规格页：\nhttps://support.apple.com/en-us/111900\nhttps://support.apple.com/en-us/111835"
	got := maiBotReplyLines(rendered, DefaultBotConfig(), MessageEvent{}, replyControlIntent{}, draft)
	messages := strings.Split(got, notificationSplitMarker)
	if len(messages) != 3 || strings.Count(messages[0], notificationLineMarker) != 3 || !strings.HasPrefix(messages[1], "规律") || !strings.HasPrefix(messages[2], "来源") {
		t.Fatalf("maiBotReplyLines() = %q", messages)
	}
}
