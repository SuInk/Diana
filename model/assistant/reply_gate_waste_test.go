// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// replyClosedConfig 是「回应提问」和「闲聊」都关掉的机器人配置。
func replyClosedConfig() BotConfig {
	return BotConfig{
		BotAccount:              "90001",
		CrossGroupMemoryEnabled: boolPointer(true),
		Participation:           &ParticipationPreferences{RelevanceLevel: "off", ChatLevel: "off"},
	}
}

func atBotEvent(event MessageEvent, botID string) MessageEvent {
	event.Segments = append([]MessageSegment{{Type: "at", Data: map[string]string{"qq": botID}}}, event.Segments...)
	return event
}

// 同样的消息也不该跑跨群检索：它的结果只给回复用。@ 了机器人的照旧检索。
func TestReplyClosedUndirectedMessageSkipsCrossGroupRetrieval(t *testing.T) {
	store := &crossGroupSearchCounter{memoryMessageHistoryStore: newMemoryMessageHistoryStore()}
	runtime := NewRuntime(replyClosedConfig(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return &capturingLLMProvider{reply: `{}`}, nil
	})
	runtime.SetMessageHistoryStore(store)

	undirected := crossGroupProbeEvent()
	undirected.SelfID = "90001"
	_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), undirected)
	if handled {
		t.Fatalf("两个开关都关、没点名的消息不该进回复，outcome = %s", outcome)
	}
	if store.searches != 0 {
		t.Fatalf("注定不回的消息不该跑跨群检索，实际 %d 次", store.searches)
	}

	directed := atBotEvent(crossGroupProbeEvent(), "90001")
	directed.MessageID, directed.Time, directed.SelfID = "m3", 210, "90001"
	prepared, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), directed)
	if !handled {
		t.Fatalf("@ 机器人的消息应进回复，outcome = %s", outcome)
	}
	// 跨群检索在 replyTo 开头做，路由阶段不查。
	if store.searches != 0 {
		t.Fatalf("路由阶段不该跑跨群检索，实际 %d 次", store.searches)
	}
	prepared = runtime.loadDeferredCrossGroupContext(prepared)
	if store.searches != 1 {
		t.Fatalf("@ 机器人的消息应在回复时检索一次跨群上下文，实际 %d 次", store.searches)
	}
	runtime.loadDeferredCrossGroupContext(prepared)
	if store.searches != 1 {
		t.Fatalf("同一轮回复不该重复检索，实际 %d 次", store.searches)
	}
}

// 路由请求里当前消息只出现一次，标着【当前消息】。
func TestProactiveRouterPayloadCarriesCurrentTextOnce(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"relevance":{"directed":false,"reason":"群友之间"},"chat_in":{"score":0.1,"reason":"没什么可接"}}`,
	}}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	const text = "这个报错应该怎么处理才好"
	runtime.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{
		Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1", SenderName: "Alice", RawMessage: text,
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}},
		Text: text,
	}})
	if len(provider.requests) != 1 {
		t.Fatalf("router calls = %d, want 1", len(provider.requests))
	}
	payload := provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content
	if got := strings.Count(payload, text); got != 1 {
		t.Fatalf("当前消息在路由请求里出现了 %d 次，want 1：%s", got, payload)
	}
	for _, want := range []string{"【当前消息】[刚刚] Alice：" + text} {
		if !strings.Contains(payload, want) {
			t.Fatalf("payload missing %s: %s", want, payload)
		}
	}
}

// 批里更早的候选不是当前消息，正文照带：它们是理解当前这条的上下文。
func TestProactiveRouterPayloadKeepsEarlierCandidateText(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"relevance":{"directed":false,"reason":"群友之间"},"chat_in":{"score":0.1,"reason":"没什么可接"}}`,
	}}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{
		{Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1"}, Text: "前面那句先说的话"},
		{Event: MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-2", MessageID: "message-2"}, Text: "后面这句才是当前"},
	})
	payload := provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content
	if strings.Count(payload, "前面那句先说的话") != 1 || strings.Count(payload, "后面这句才是当前") != 1 {
		t.Fatalf("each candidate text should appear exactly once: %s", payload)
	}
	if strings.Count(payload, "【当前消息】") != 1 || !strings.Contains(payload, "【当前消息】[刚刚] user-2：后面这句才是当前") {
		t.Fatalf("only the latest candidate is current: %s", payload)
	}
}

// 路由带原图，但只是是非题，用 low 档；正式回复那一路仍是 high。
func TestProactiveRouterAttachesImageAtLowDetail(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"relevance":{"directed":false,"reason":"群友之间"},"chat_in":{"score":0.1,"reason":"没什么可接"}}`,
	}}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(aiImageTestPNG(t))
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "message-1",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": dataURL}}}}
	runtime.routeProactiveReplyBatch(context.Background(), []proactiveReplyCandidate{{Event: event, Text: "[图片]"}})
	if len(provider.requests) != 1 {
		t.Fatalf("router calls = %d, want 1", len(provider.requests))
	}
	routeMessage := provider.requests[0].Messages[len(provider.requests[0].Messages)-1]
	images := 0
	for _, part := range routeMessage.Parts {
		if part.Type != llm.ContentPartImageURL {
			continue
		}
		images++
		if part.Detail != "low" {
			t.Fatalf("router image detail = %q, want low", part.Detail)
		}
	}
	if images == 0 {
		t.Fatalf("router should still attach the original image: %#v", routeMessage.Parts)
	}

	reply, failures := llmMessageFromEventWithImagesForContextDiagnostics(context.Background(), event, "看看这张", nil)
	if len(failures) != 0 {
		t.Fatalf("load failures: %v", failures)
	}
	for _, part := range reply.Parts {
		if part.Type == llm.ContentPartImageURL && part.Detail != "high" {
			t.Fatalf("formal reply image detail = %q, want high", part.Detail)
		}
	}
}

// 主动接话和闲聊插话说明逐条变化，必须落在 tail：同一个群里「主动回应提问」和
// 「闲聊插话」两种轮次的 head 要逐字节相同，历史的前缀缓存才接得上。
func TestProactiveAndChatInRulesLandInTail(t *testing.T) {
	base := BotConfig{}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	relationship := RelationshipPolicyFor(UserMemoryProfile{}, base.OwnerID, "1")

	related := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "1", proactiveReply: true}
	chatIn := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "1", proactiveReply: true, chatInReply: true}
	relatedHead, relatedTail := runtime.systemPromptPartsWithRelationshipAndAgentTools(related, nil, true, relationship, true, nil)
	chatInHead, chatInTail := runtime.systemPromptPartsWithRelationshipAndAgentTools(chatIn, nil, true, relationship, true, nil)

	proactive := strings.TrimSpace(base.prompt(promptProactiveReplySpec))
	toolResult := base.prompt(promptProactiveToolResultSpec)
	pacing := base.prompt(promptChatInPacingSpec)
	chatInReply := base.prompt(promptChatInReplySpec)
	noAgreement := base.prompt(promptChatInNoAgreementSpec)
	for _, section := range []string{proactive, toolResult, pacing, chatInReply, noAgreement} {
		if strings.Contains(relatedHead, section) || strings.Contains(chatInHead, section) {
			t.Fatalf("per-turn section leaked into cached head: %q", section)
		}
	}
	if relatedHead != chatInHead {
		t.Fatal("head differs between proactive-related and chat-in turns; prefix cache breaks")
	}
	if !strings.Contains(relatedTail, proactive) || !strings.Contains(relatedTail, toolResult) || strings.Contains(relatedTail, chatInReply) {
		t.Fatalf("related tail = %q", relatedTail)
	}
	// 相对顺序保持：主动说明 → 工具结果 → 插话节奏 → 插话说明 → 别空口附和，语气锚点仍在最后。
	order := []string{proactive, toolResult, pacing, chatInReply, noAgreement, personaClosingAnchor(base)}
	last := -1
	for _, section := range order {
		index := strings.Index(chatInTail, strings.TrimSpace(section))
		if index <= last {
			t.Fatalf("tail order broken at %q (index %d, previous %d): %q", section, index, last, chatInTail)
		}
		last = index
	}

	// 被点名的那一轮一段都不带。
	_, triggeredTail := runtime.systemPromptPartsWithRelationshipAndAgentTools(MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "1"}, nil, false, relationship, true, nil)
	if strings.Contains(triggeredTail, proactive) || strings.Contains(triggeredTail, chatInReply) {
		t.Fatalf("triggered turn carries proactive sections: %q", triggeredTail)
	}
}
