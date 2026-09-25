// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"strings"
	"sync/atomic"
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

// 两个接话开关都关、这句话又没在叫机器人：它注定不回，相邻媒体指代判断的模型
// 调用是白花的。线上抓到的就是这条：gemini 636 token 判完指代，最后照样被丢掉。
func TestMediaReferenceSkipsLLMWhenReplyClosedAndUndirected(t *testing.T) {
	var llmCalls atomic.Int32
	runtime := NewRuntime(replyClosedConfig(), nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		llmCalls.Add(1)
		return &capturingLLMProvider{reply: `{"refers_to_media":false,"confidence":0.9}`}, nil
	})
	owner := textEvent("owner-1", "99999", "这是我们的 API Key：sk-xxxx", 990)
	sticker := stickerEvent("sticker-1", "10001", 1000)
	question := textEvent("q-1", "10001", "这是什么", 1006)
	runtime.remember(owner)
	runtime.remember(sticker)

	outcome := runtime.shouldMergeAdjacentMedia(context.Background(), question, "这是什么", []MessageEvent{sticker})
	if outcome.Merge || outcome.Method != "reply_closed" {
		t.Fatalf("outcome = %#v, want reply_closed without merge", outcome)
	}
	if got := llmCalls.Load(); got != 0 {
		t.Fatalf("注定不回的消息不该问指代模型，llm calls = %d", got)
	}

	// 文本明说在讲图时照旧直接并：这一档本来就不花钱，和开关无关。
	if outcome := runtime.shouldMergeAdjacentMedia(context.Background(), question, "这张图什么意思", []MessageEvent{sticker}); !outcome.Merge || outcome.Method != "explicit_media_reference" {
		t.Fatalf("explicit outcome = %#v", outcome)
	}

	// @ 了机器人的消息和以前完全一样：有竞争指代对象就问模型。
	directed := atBotEvent(question, "90001")
	outcome = runtime.shouldMergeAdjacentMedia(context.Background(), directed, "这是什么", []MessageEvent{sticker})
	if outcome.Method != "llm_declined" {
		t.Fatalf("directed outcome = %#v, want llm_declined", outcome)
	}
	if got := llmCalls.Load(); got != 1 {
		t.Fatalf("@ 机器人的消息应照常问一次指代模型，llm calls = %d", got)
	}
}

// 开关没全关时不受影响：不点名的消息仍可能被接话，指代判断照做。
func TestMediaReferenceStillJudgesWhenChatInOpen(t *testing.T) {
	var llmCalls atomic.Int32
	cfg := replyClosedConfig()
	cfg.Participation = &ParticipationPreferences{RelevanceLevel: "off", ChatLevel: "low"}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		llmCalls.Add(1)
		return &capturingLLMProvider{reply: `{"refers_to_media":false,"confidence":0.9}`}, nil
	})
	owner := textEvent("owner-1", "99999", "这是我们的 API Key：sk-xxxx", 990)
	sticker := stickerEvent("sticker-1", "10001", 1000)
	runtime.remember(owner)
	runtime.remember(sticker)

	outcome := runtime.shouldMergeAdjacentMedia(context.Background(), textEvent("q-1", "10001", "这是什么", 1006), "这是什么", []MessageEvent{sticker})
	if outcome.Method != "llm_declined" || llmCalls.Load() != 1 {
		t.Fatalf("outcome = %#v calls = %d, want one llm judgment", outcome, llmCalls.Load())
	}
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
	_, _, handled, outcome = runtime.prepareMessageEvent(context.Background(), directed)
	if !handled {
		t.Fatalf("@ 机器人的消息应进回复，outcome = %s", outcome)
	}
	if store.searches == 0 {
		t.Fatal("@ 机器人的消息应照常检索跨群上下文")
	}
}

// 引用还没解析出来时不知道引的是不是机器人，不能当成「注定不回」。
func TestReplyClosedKeepsUnresolvedQuoteOnOldPath(t *testing.T) {
	runtime := NewRuntime(replyClosedConfig(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := textEvent("q-1", "10001", "这是什么", 1006)
	if !runtime.replyClosedForUndirectedEvent(event, "这是什么") {
		t.Fatal("plain undirected message should be recognized as reply-closed")
	}
	event.Segments = append([]MessageSegment{{Type: "reply", Data: map[string]string{"id": "777"}}}, event.Segments...)
	if runtime.replyClosedForUndirectedEvent(event, "这是什么") {
		t.Fatal("unresolved quote must stay on the old path")
	}
	private := event
	private.Kind, private.GroupID = EventKindPrivate, ""
	if runtime.replyClosedForUndirectedEvent(private, "这是什么") {
		t.Fatal("private chat is never reply-closed by the group switches")
	}
}

// 路由请求里当前消息只出现一次：current_text 已经带了正文，候选里那条只留标识。
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
	for _, want := range []string{`"current_text":"` + text + `"`, `"message_id":"message-1"`, `"is_current":true`} {
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
	if strings.Count(payload, `"is_current":true`) != 1 {
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
