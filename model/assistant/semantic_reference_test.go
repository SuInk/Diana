// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestSemanticReferenceCanSelectAnotherUsersOldVideo(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{`{"message_id":"video-old","confidence":0.94,"reason":"用户问的是小明发的视频"}`}}
	runtime := NewRuntime(BotConfig{RecentContextLimit: 20}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.remember(MessageEvent{
		Kind:       EventKindGroup,
		Time:       1,
		GroupID:    "group-1",
		UserID:     "other-user",
		SenderName: "小明",
		MessageID:  "video-old",
		RawMessage: "[视频]",
		Segments:   []MessageSegment{{Type: "video", Data: map[string]string{"file": "old.mp4"}}},
	})
	runtime.remember(MessageEvent{Kind: EventKindGroup, Time: 999999, GroupID: "group-1", UserID: "someone", SenderName: "其他人", MessageID: "text-new", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "中间的聊天"}}}})

	event := runtime.enrichSemanticReference(context.Background(), MessageEvent{
		Kind:       EventKindGroup,
		Time:       1000000,
		GroupID:    "group-1",
		UserID:     "current-user",
		SenderName: "当前用户",
		MessageID:  "question-1",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "小明发的那个视频是什么"}}},
	}, "小明发的那个视频是什么")
	if event.Quoted == nil || event.Quoted.MessageID != "video-old" || event.Quoted.UserID != "other-user" || !event.Quoted.Semantic {
		t.Fatalf("semantic reference = %#v", event.Quoted)
	}
	if len(provider.requests) != 1 || !strings.Contains(provider.requests[0].Messages[1].Content, `"video_count":1`) || !strings.Contains(provider.requests[0].Messages[1].Content, `"age_seconds":999999`) {
		t.Fatalf("routing request = %#v", provider.requests)
	}
}

func TestSemanticReferenceUsesRoutingProfile(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		ActiveID: "main",
		Profiles: []llm.Profile{
			{ID: "main", Group: "default", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "main-model"}},
			{ID: "routing", Group: "routing", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "routing-model"}},
		},
	}}
	usedModels := make([]string, 0, 1)
	runtime := NewRuntime(BotConfig{RecentContextLimit: 20}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(cfg llm.ProviderConfig) (LLMProvider, error) {
		return &semanticReferenceModelProvider{model: cfg.Model, usedModels: &usedModels}, nil
	})
	runtime.remember(MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "image-1",
		Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.com/a.jpg"}}},
	})

	event := runtime.enrichSemanticReference(context.Background(), MessageEvent{
		Kind:      EventKindPrivate,
		UserID:    "user-1",
		MessageID: "question-1",
		Segments:  []MessageSegment{{Type: "text", Data: map[string]string{"text": "这张图是什么"}}},
	}, "这张图是什么")
	if event.Quoted == nil || event.Quoted.MessageID != "image-1" {
		t.Fatalf("semantic reference = %#v", event.Quoted)
	}
	if len(usedModels) != 1 || usedModels[0] != "routing-model" {
		t.Fatalf("used models = %#v, want routing-model", usedModels)
	}
}

func TestSemanticReferenceDecisionCacheSkipsRepeatedRouterTokens(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{
		`{"message_ids":["image-1"],"confidence":0.98,"reason":"同一张图片"}`,
		`{"message_ids":["image-2"],"confidence":0.98,"reason":"媒体集合已变化"}`,
	}}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.remember(MessageEvent{Kind: EventKindPrivate, Time: 100, UserID: "user-1", MessageID: "image-1", Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.com/1.jpg"}}}})
	question := func(messageID string) MessageEvent {
		return MessageEvent{Kind: EventKindPrivate, Time: 110, UserID: "user-1", MessageID: messageID, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "帮我再看一下"}}}}
	}
	first := runtime.enrichSemanticReference(context.Background(), question("question-1"), "帮我再看一下")
	second := runtime.enrichSemanticReference(context.Background(), question("question-2"), "帮我再看一下")
	if first.Quoted == nil || second.Quoted == nil || first.Quoted.MessageID != "image-1" || second.Quoted.MessageID != "image-1" {
		t.Fatalf("cached selections first=%#v second=%#v", first.Quoted, second.Quoted)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("router requests=%d, cache hit should avoid token usage", len(provider.requests))
	}

	runtime.remember(MessageEvent{Kind: EventKindPrivate, Time: 105, UserID: "user-1", MessageID: "image-2", Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.com/2.jpg"}}}})
	third := runtime.enrichSemanticReference(context.Background(), question("question-3"), "帮我再看一下")
	if third.Quoted == nil || third.Quoted.MessageID != "image-2" || len(provider.requests) != 2 {
		t.Fatalf("changed media did not invalidate cache: quoted=%#v requests=%d", third.Quoted, len(provider.requests))
	}
}

func TestSemanticReferenceDecisionCacheExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.now = func() time.Time { return now }
	decision := semanticReferenceDecision{MessageIDs: []string{"image-1"}, Confidence: 0.9}
	runtime.saveSemanticReferenceDecision(context.Background(), "key", decision)
	if _, found := runtime.loadSemanticReferenceDecision(context.Background(), "key"); !found {
		t.Fatal("fresh semantic cache entry missed")
	}
	now = now.Add(semanticReferenceCacheTTL + time.Second)
	if _, found := runtime.loadSemanticReferenceDecision(context.Background(), "key"); found {
		t.Fatal("expired semantic cache entry was used")
	}
}

func TestSemanticReferenceCanSelectImageFileOrText(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.remember(MessageEvent{Kind: EventKindPrivate, Time: 100, UserID: "user-1", MessageID: "mixed", Segments: []MessageSegment{
		{Type: "text", Data: map[string]string{"text": "方案说明"}},
		{Type: "image", Data: map[string]string{"url": "https://example.com/a.jpg"}},
		{Type: "file", Data: map[string]string{"name": "report.pdf"}},
	}})
	candidates, _, _ := runtime.semanticReferenceCandidates(context.Background(), MessageEvent{Kind: EventKindPrivate, Time: 130, UserID: "user-1", MessageID: "current"})
	if len(candidates) != 1 || candidates[0].ImageCount != 1 || candidates[0].FileCount != 1 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[0].EventTime != 100 || candidates[0].AgeSeconds == nil || *candidates[0].AgeSeconds != 30 {
		t.Fatalf("candidate timing = %#v", candidates[0])
	}
	for _, want := range []string{"text", "image", "file"} {
		if !containsSemanticString(candidates[0].Content, want) {
			t.Fatalf("candidate missing %q: %#v", want, candidates[0])
		}
	}
}

func TestSemanticReferenceTreatsVoiceAsDurableMedia(t *testing.T) {
	runtime := NewRuntime(BotConfig{RecentContextLimit: 2}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := newSemanticTimelineStore()
	runtime.SetMessageHistoryStore(store)
	runtime.remember(MessageEvent{Kind: EventKindGroup, Time: 100, GroupID: "group-1", UserID: "user-1", MessageID: "voice-old", Segments: []MessageSegment{{Type: "record", Data: map[string]string{"transcript": "持久语音内容"}}}})
	for index := 0; index < 3; index++ {
		runtime.remember(MessageEvent{Kind: EventKindGroup, Time: int64(101 + index), GroupID: "group-1", UserID: "other", MessageID: "filler-" + string(rune('a'+index)), Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "中间消息"}}}})
	}
	current := MessageEvent{Kind: EventKindGroup, Time: 110, GroupID: "group-1", UserID: "user-1", MessageID: "current"}
	if !runtime.hasDurableMediaBeyondRecentContext(context.Background(), current) {
		t.Fatal("voice outside recent context did not enable durable semantic routing")
	}
	candidates, _, _ := runtime.semanticReferenceCandidates(context.Background(), current)
	for _, candidate := range candidates {
		if candidate.MessageID != "voice-old" {
			continue
		}
		if candidate.AudioCount != 1 || !containsSemanticString(candidate.Content, "audio") || !strings.Contains(candidate.Text, "持久语音内容") {
			t.Fatalf("voice candidate=%#v", candidate)
		}
		return
	}
	t.Fatalf("voice candidate missing: %#v", candidates)
}

func TestSemanticReferenceAggregatesCrossMessageImages(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{`{"message_ids":["image-3","image-1","image-2"],"confidence":0.98,"reason":"用户明确指向连发的三张图"}`}}
	runtime := NewRuntime(BotConfig{RecentContextLimit: 20}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	imageURLs := []string{
		"data:image/png;base64,YQ==",
		"data:image/png;base64,Yg==",
		"data:image/png;base64,Yw==",
	}
	for index, imageURL := range imageURLs {
		runtime.remember(MessageEvent{
			Kind:       EventKindPrivate,
			Time:       int64(100 + index),
			UserID:     "user-1",
			MessageID:  "image-" + string(rune('1'+index)),
			RawMessage: "[图片]",
			Segments:   []MessageSegment{{Type: "image", Data: map[string]string{"url": imageURL}}},
		})
	}

	event := runtime.enrichSemanticReference(context.Background(), MessageEvent{
		Kind:      EventKindPrivate,
		Time:      110,
		UserID:    "user-1",
		MessageID: "question",
		Segments:  []MessageSegment{{Type: "text", Data: map[string]string{"text": "读我连发的三张图"}}},
	}, "读我连发的三张图")
	if got := strings.Join(eventSemanticSourceMessageIDs(event), ","); got != "image-1,image-2,image-3" {
		t.Fatalf("semantic sources = %q", got)
	}
	if event.Quoted == nil || event.Quoted.MessageID != "image-1" || !event.Quoted.Semantic {
		t.Fatalf("primary semantic quote = %#v", event.Quoted)
	}
	if got := runtime.semanticReferenceImageURLs(context.Background(), event); strings.Join(got, ",") != strings.Join(imageURLs, ",") {
		t.Fatalf("semantic images = %#v", got)
	}

	sourceContext := runtime.semanticReferenceContext(context.Background(), event)
	sourceContext.AttachedImageCount = len(imageURLs)
	prompt := currentPromptTextWithSemanticContext(event, "读我连发的三张图", sourceContext)
	message := llmMessageFromEventWithImagesForContext(context.Background(), event, prompt, runtime.semanticReferenceImageURLs(context.Background(), event))
	var actualImages []string
	for _, part := range message.Parts {
		if part.Type == llm.ContentPartImageURL {
			actualImages = append(actualImages, part.ImageURL)
		}
		if strings.Contains(part.Text, "[图片]") {
			t.Fatalf("image placeholder leaked into multimodal message: %#v", message)
		}
	}
	if got := strings.Join(actualImages, ","); got != strings.Join(imageURLs, ",") {
		t.Fatalf("multimodal images = %#v", actualImages)
	}
	if (!strings.Contains(prompt, "3 张来源图片") && !strings.Contains(prompt, "实际附加 3 张可读取图片")) || !strings.Contains(prompt, "逐张查看") || strings.Contains(prompt, "3 条文字来源") || strings.Contains(message.Content, "[图片]") {
		t.Fatalf("current prompt = %q", prompt)
	}
	if text := historyPromptText(MessageEvent{Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": imageURLs[0]}}}, RawMessage: "[图片]"}); text != "" {
		t.Fatalf("pure image history must not become a placeholder: %q", text)
	}
	if item := chatHistoryItem(MessageEvent{Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": imageURLs[0]}}}, RawMessage: "[图片]"}); item.Text != "" || item.ImageCount != 1 {
		t.Fatalf("chat history image metadata = %#v", item)
	}
	if sources := runtime.imageEditSourceImages(context.Background(), event, nil); strings.Join(sources, ",") != strings.Join(imageURLs, ",") {
		t.Fatalf("image edit sources = %#v", sources)
	}
	if len(provider.requests) != 1 || !strings.Contains(provider.requests[0].Messages[0].Content, "message_ids") {
		t.Fatalf("semantic routing requests = %#v", provider.requests)
	}
}

func TestSemanticReferenceContextIncludesAllSelectedTextSources(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	messageIDs := make([]string, 0, 6)
	for index := 1; index <= 6; index++ {
		messageID := fmt.Sprintf("bot-reply-%d", index)
		messageIDs = append(messageIDs, messageID)
		runtime.remember(MessageEvent{
			Kind:       EventKindGroup,
			Time:       int64(100 + index),
			GroupID:    "group-1",
			UserID:     "42",
			SenderName: "Diana",
			MessageID:  messageID,
			Outbound:   true,
			Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": fmt.Sprintf("第 %d 条完整回复正文", index)}}},
		})
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "owner", MessageID: "current"}
	setEventSemanticSourceMessageIDs(&event, messageIDs)

	sourceContext := runtime.semanticReferenceContext(context.Background(), event)
	if sourceContext.RequestedSourceCount != 6 || sourceContext.ResolvedSourceCount != 6 || sourceContext.TextSourceCount != 6 || sourceContext.HistoricalImageCount != 0 || sourceContext.MissingSourceCount != 0 {
		t.Fatalf("source context counts = %#v", sourceContext)
	}
	for index, messageID := range messageIDs {
		for _, want := range []string{messageID, fmt.Sprintf("第 %d 条完整回复正文", index+1)} {
			if !strings.Contains(sourceContext.Content, want) {
				t.Fatalf("source context missing %q: %s", want, sourceContext.Content)
			}
		}
	}
	prompt := currentPromptTextWithSemanticContext(event, "总结 Diana 之前哪些回复有误", sourceContext)
	if !strings.Contains(prompt, "6 条包含文字") || !strings.Contains(prompt, "逐条核对") || strings.Contains(prompt, "逐张查看") || strings.Contains(prompt, "6 张") {
		t.Fatalf("text source prompt = %q", prompt)
	}
	message := semanticReferenceContextMessage(sourceContext)
	if message.Priority != llm.MessagePriorityCurrent || !strings.Contains(message.Content, "bot-reply-6") {
		t.Fatalf("protected source message = %#v", message)
	}
}

func TestReplyRequestProtectsAllSelectedTextSources(t *testing.T) {
	provider := &capturingLLMProvider{reply: "已逐条核对"}
	runtime := NewRuntime(BotConfig{BotAccount: "42", RecentContextLimit: 3}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	messageIDs := make([]string, 0, 6)
	for index := 1; index <= 6; index++ {
		messageID := fmt.Sprintf("historical-reply-%d", index)
		messageIDs = append(messageIDs, messageID)
		runtime.remember(MessageEvent{
			Kind:       EventKindGroup,
			Time:       int64(100 + index),
			SelfID:     "42",
			GroupID:    "group-1",
			UserID:     "42",
			SenderName: "Diana",
			MessageID:  messageID,
			Outbound:   true,
			Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": fmt.Sprintf("历史回复正文 %d", index)}}},
		})
	}
	event := MessageEvent{
		Kind:       EventKindGroup,
		Time:       200,
		SelfID:     "42",
		GroupID:    "group-1",
		UserID:     "owner",
		SenderName: "主人",
		MessageID:  "current-question",
		RawMessage: "总结 Diana 此前哪些回复有误",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "总结 Diana 此前哪些回复有误"}}},
		ToMe:       true,
	}
	setEventSemanticSourceMessageIDs(&event, messageIDs)
	reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
	if err != nil || reply != "已逐条核对" {
		t.Fatalf("reply = %q, err = %v", reply, err)
	}
	request := provider.requestSnapshot()
	for index, messageID := range messageIDs {
		for _, want := range []string{messageID, fmt.Sprintf("历史回复正文 %d", index+1)} {
			if !requestMessagesContain(request.Messages, want) {
				t.Fatalf("final request missing %q: %#v", want, request.Messages)
			}
		}
	}
	if !requestMessagesContain(request.Messages, "6 条包含文字") || requestMessagesContain(request.Messages, "6 张原图") {
		t.Fatalf("final request source instructions = %#v", request.Messages)
	}
}

func TestSemanticReferencePromptCountsMixedTextAndAttachedImages(t *testing.T) {
	event := MessageEvent{SemanticSourceMessageIDs: []string{"text-1", "image-1", "image-2"}}
	prompt := currentPromptTextWithSemanticContext(event, "一起分析", semanticReferenceContext{
		RequestedSourceCount: 3,
		TextSourceCount:      1,
		AttachedImageCount:   2,
	})
	for _, want := range []string{"1 条文字来源", "实际附加 2 张", "逐条核对", "逐张查看"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("mixed source prompt missing %q: %s", want, prompt)
		}
	}
}

func TestSemanticReferenceRejectsUnknownCandidate(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{`{"message_id":"invented","confidence":1,"reason":"bad"}`}}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.remember(MessageEvent{Kind: EventKindPrivate, UserID: "user-1", MessageID: "real", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "真实消息"}}}})
	event := runtime.enrichSemanticReference(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "user-1", MessageID: "current"}, "那个呢")
	if event.Quoted != nil {
		t.Fatalf("invented candidate accepted: %#v", event.Quoted)
	}
}

func TestSemanticReferenceSkipsExplicitQuoteAndCurrentMedia(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{`{"message_id":"old","confidence":1}`}}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.remember(MessageEvent{Kind: EventKindPrivate, UserID: "user-1", MessageID: "old", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "旧消息"}}}})
	explicit := &QuotedMessage{MessageID: "quoted"}
	withQuote := runtime.enrichSemanticReference(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "user-1", MessageID: "current", Quoted: explicit}, "这是什么")
	withImage := runtime.enrichSemanticReference(context.Background(), MessageEvent{Kind: EventKindPrivate, UserID: "user-1", MessageID: "current-2", Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": "https://example.com/a.jpg"}}}}, "这是什么")
	if withQuote.Quoted != explicit || withImage.Quoted != nil || len(provider.requests) != 0 {
		t.Fatalf("resolver should have been skipped: quote=%#v image=%#v requests=%d", withQuote.Quoted, withImage.Quoted, len(provider.requests))
	}
}

func TestSemanticReferenceCanResolveMediaBehindTextQuote(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{`{"message_id":"recent-image","confidence":0.96,"reason":"用户说发了，指向刚发送的图片"}`}}
	runtime := NewRuntime(BotConfig{RecentContextLimit: 20}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.remember(MessageEvent{
		Kind:       EventKindGroup,
		Time:       100,
		GroupID:    "group-1",
		UserID:     "other-user",
		SenderName: "群友",
		MessageID:  "recent-image",
		Segments:   []MessageSegment{{Type: "image", Data: map[string]string{"cached_file": "/tmp/cached.jpg"}}},
	})
	event := runtime.enrichSemanticReference(context.Background(), MessageEvent{
		Kind:      EventKindGroup,
		Time:      105,
		GroupID:   "group-1",
		UserID:    "owner",
		MessageID: "question",
		Quoted: &QuotedMessage{
			MessageID: "bot-text",
			UserID:    "bot",
			Segments:  []MessageSegment{{Type: "text", Data: map[string]string{"text": "把版本号发我"}}},
		},
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "发了"}}},
	}, "发了")
	if event.Quoted == nil || event.Quoted.MessageID != "recent-image" || !event.Quoted.Semantic {
		t.Fatalf("semantic media reference = %#v", event.Quoted)
	}
}

func TestSemanticReferenceFindsPersistedImageBeyondShortContext(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{`{"message_id":"target-image","confidence":0.98,"reason":"错误回复之前的图片是原任务来源"}`}}
	runtime := NewRuntime(BotConfig{BotAccount: "42", RecentContextLimit: 20, ContextSummaryThreshold: 20}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	store := newSemanticTimelineStore()
	runtime.SetMessageHistoryStore(store)
	runtime.remember(MessageEvent{
		Kind:       EventKindGroup,
		Time:       100,
		GroupID:    "group-1",
		UserID:     "owner",
		SenderName: "TestOwner",
		MessageID:  "target-image",
		Segments:   []MessageSegment{{Type: "image", Data: map[string]string{"cached_file": "/tmp/target.jpg"}}},
	})
	runtime.remember(MessageEvent{
		Kind:       EventKindGroup,
		Time:       101,
		GroupID:    "group-1",
		UserID:     "owner",
		SenderName: "TestOwner",
		MessageID:  "task-text",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "我把图片发出来了"}}},
	})
	runtime.remember(MessageEvent{
		Kind:                    EventKindGroup,
		Time:                    102,
		GroupID:                 "group-1",
		UserID:                  "42",
		SenderName:              "Diana",
		MessageID:               "bot-timeout",
		SemanticSourceMessageID: "target-image",
		Segments: []MessageSegment{
			{Type: "reply", Data: map[string]string{"id": "task-text"}},
			{Type: "text", Data: map[string]string{"text": "出错了：请求处理超时，请稍后重试。"}},
		},
	})
	for index := 0; index < 25; index++ {
		runtime.remember(MessageEvent{
			Kind:      EventKindGroup,
			Time:      int64(103 + index),
			GroupID:   "group-1",
			UserID:    "other",
			MessageID: "filler-" + string(rune('a'+index)),
			Segments:  []MessageSegment{{Type: "text", Data: map[string]string{"text": "中间群聊"}}},
		})
	}
	if history := runtime.contextHistory(MessageEvent{Kind: EventKindGroup, GroupID: "group-1"}); semanticHistoryContainsMessage(history, "target-image") {
		t.Fatal("target image unexpectedly remained in the 20-message short context")
	}

	event := runtime.enrichSemanticReference(context.Background(), MessageEvent{
		Kind:      EventKindGroup,
		Time:      140,
		SelfID:    "42",
		GroupID:   "group-1",
		UserID:    "owner",
		MessageID: "retry-request",
		Quoted: &QuotedMessage{
			MessageID:               "bot-timeout",
			UserID:                  "42",
			SenderName:              "Diana",
			SemanticSourceMessageID: "target-image",
			Segments: []MessageSegment{
				{Type: "reply", Data: map[string]string{"id": "task-text"}},
				{Type: "text", Data: map[string]string{"text": "出错了：请求处理超时，请稍后重试。"}},
			},
		},
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "重试这个"}}},
	}, "重试这个")
	if event.Quoted == nil || event.Quoted.MessageID != "target-image" || !event.Quoted.Semantic {
		t.Fatalf("semantic reference = %#v", event.Quoted)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("routing requests = %d", len(provider.requests))
	}
	prompt := provider.requests[0].Messages[1].Content
	for _, want := range []string{`"message_id":"target-image"`, `"semantic_source_message_id":"target-image"`, `"is_error_wrapper":true`, "我把图片发出来了"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("routing prompt missing %q: %s", want, prompt)
		}
	}
}

func TestOutgoingHistoryPreservesReplyAndSemanticSource(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	source := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "owner", MessageID: "request"}
	remembered := source
	setEventSemanticSourceMessageIDs(&remembered, []string{"target-image", "target-image-2"})
	runtime.remember(remembered)

	outgoing := runtime.outgoingHistoryEvent(source, OutgoingMessage{
		Text:           "出错了：请求处理超时，请稍后重试。",
		ReplyMessageID: "request",
		MentionUserID:  "owner",
	})
	if outgoing.SemanticSourceMessageID != "target-image" {
		t.Fatalf("semantic source = %q", outgoing.SemanticSourceMessageID)
	}
	if got := strings.Join(outgoing.SemanticSourceMessageIDs, ","); got != "target-image,target-image-2" {
		t.Fatalf("semantic sources = %q", got)
	}
	if len(outgoing.Segments) < 3 || outgoing.Segments[0].Type != "reply" || outgoing.Segments[0].Data["id"] != "request" || outgoing.Segments[1].Type != "at" {
		t.Fatalf("outgoing segments = %#v", outgoing.Segments)
	}
}

func TestSemanticReferencePromptLabel(t *testing.T) {
	got := quotedPromptText(&QuotedMessage{Semantic: true, SenderName: "Alice", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "目标内容"}}}})
	if !strings.Contains(got, "指代判断选中的历史消息") {
		t.Fatalf("quotedPromptText() = %q", got)
	}
}

func containsSemanticString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func semanticHistoryContainsMessage(events []MessageEvent, messageID string) bool {
	for _, event := range events {
		if event.MessageID == messageID {
			return true
		}
	}
	return false
}

type semanticTimelineStore struct {
	events map[string][]MessageEvent
}

func newSemanticTimelineStore() *semanticTimelineStore {
	return &semanticTimelineStore{events: map[string][]MessageEvent{}}
}

func (s *semanticTimelineStore) AppendMessageEvent(_ context.Context, session string, event MessageEvent) error {
	for index := range s.events[session] {
		if s.events[session][index].MessageID == event.MessageID {
			s.events[session][index] = event
			return nil
		}
	}
	s.events[session] = append(s.events[session], event)
	return nil
}

func (s *semanticTimelineStore) ListRecentMessageEvents(_ context.Context, session string, limit int) ([]MessageEvent, error) {
	events := append([]MessageEvent(nil), s.events[session]...)
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

func (s *semanticTimelineStore) ListMessageEventsBetween(_ context.Context, session string, fromTime, throughTime int64) ([]MessageEvent, error) {
	var events []MessageEvent
	for _, event := range s.events[session] {
		if event.Time >= fromTime && event.Time <= throughTime {
			events = append(events, event)
		}
	}
	return events, nil
}

func (s *semanticTimelineStore) FindMessageEvent(_ context.Context, session, messageID string) (MessageEvent, bool, error) {
	for _, event := range s.events[session] {
		if event.MessageID == messageID {
			return event, true, nil
		}
	}
	return MessageEvent{}, false, nil
}

var _ LLMProvider = (*semanticReferenceTestProvider)(nil)

type semanticReferenceTestProvider struct{}

func (*semanticReferenceTestProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return &llm.GenerateResponse{}, nil
}

type semanticReferenceModelProvider struct {
	model      string
	usedModels *[]string
}

func (p *semanticReferenceModelProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	*p.usedModels = append(*p.usedModels, p.model)
	return &llm.GenerateResponse{Text: `{"message_id":"image-1","confidence":0.99,"reason":"当前问题指向图片"}`}, nil
}
