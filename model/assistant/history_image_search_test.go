// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// searchExtraTestStore 在 recallImageTestStore 之上记下补写的检索文本。
type searchExtraTestStore struct {
	*recallImageTestStore
	mu     sync.Mutex
	extras map[string]string
}

type rejectingRevisionSearchExtraStore struct {
	*searchExtraTestStore
	current MessageEvent
	calls   int
}

func (s *rejectingRevisionSearchExtraStore) SaveMessageSearchExtraIfMediaCurrent(_ context.Context, _ string, _ MessageEvent, _ string) (MessageEvent, bool, error) {
	s.calls++
	return s.current, false, nil
}

func newSearchExtraTestStore() *searchExtraTestStore {
	return &searchExtraTestStore{recallImageTestStore: newRecallImageTestStore(), extras: map[string]string{}}
}

func (s *searchExtraTestStore) SaveMessageSearchExtra(_ context.Context, session, messageID, extra string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extras[session+"|"+messageID] = extra
	return nil
}

func (s *searchExtraTestStore) extra(session, messageID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.extras[session+"|"+messageID]
}

// 图片描述生成完之后要补进检索文本，否则纯图片消息在词面检索里永远是空的。
func TestHistoryImageDescriptionBackfillsSearchText(t *testing.T) {
	imagePath, hash := writeRecallImageFixture(t)
	store := newSearchExtraTestStore()
	provider := &recallImageVisionProvider{}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetMessageHistoryStore(store)
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "sticker",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"cached_file":         imagePath,
			imageContentSHA256Key: hash,
		}}},
	}

	runtime.enqueueHistoryImageDescriptions(event)
	waitForCondition(t, 2*time.Second, func() bool {
		return strings.Contains(store.extra(sessionKey(event), "sticker"), "命中率为 63%")
	})
}

// 描述已经落库时，检索文本直接从缓存拼出来，不再触发视觉调用。
func TestMessageImageDescriptionTextReadsCache(t *testing.T) {
	imagePath, hash := writeRecallImageFixture(t)
	store := newSearchExtraTestStore()
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	if err := store.SaveImageDescription(context.Background(), ImageDescriptionRecord{ContentSHA256: hash, Description: "炸毛生气的猫耳少女在哈气"}); err != nil {
		t.Fatal(err)
	}
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "sticker",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"cached_file":         imagePath,
			imageContentSHA256Key: hash,
		}}},
	}
	if got := runtime.messageImageDescriptionText(context.Background(), event); got != "炸毛生气的猫耳少女在哈气" {
		t.Fatalf("description text = %q", got)
	}

	// 纯图片消息的语义索引文本就是描述本身；没有描述时它是空的，进不了向量库。
	if got := semanticIndexText(context.Background(), runtime, event); got != "炸毛生气的猫耳少女在哈气" {
		t.Fatalf("semantic index text = %q", got)
	}
	withText := event
	withText.Segments = append([]MessageSegment{{Type: "text", Data: map[string]string{"text": "你看这个"}}}, event.Segments...)
	if got := semanticIndexText(context.Background(), runtime, withText); !strings.Contains(got, "你看这个") || !strings.Contains(got, "猫耳少女") {
		t.Fatalf("semantic index text with caption = %q", got)
	}
	bare := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "plain",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "只有文字"}}}}
	if got := semanticIndexText(context.Background(), runtime, bare); got != "只有文字" {
		t.Fatalf("plain semantic text = %q", got)
	}
}

func TestMessageSearchMediaRevisionTracksImagesOnly(t *testing.T) {
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	event := MessageEvent{
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "原说明"}},
			{Type: "image", Data: map[string]string{
				imageContentSHA256Key:           hashA,
				"file_unique_id":                "stable-telegram-image",
				"file_id":                       "volatile-file-id",
				recallImageDescriptionKey:       "旧描述",
				recallImageDescriptionSourceKey: "vision",
			}},
		},
	}
	original := MessageSearchMediaRevision(event)
	if original == "" {
		t.Fatal("有图片的消息应有媒体版本")
	}
	textEdited := cloneRecallEvents([]MessageEvent{event})[0]
	textEdited.Segments[0].Data["text"] = "编辑后的说明"
	textEdited.Segments[1].Data["file_id"] = "renewed-file-id"
	textEdited.Segments[1].Data[recallImageDescriptionKey] = "新描述"
	if got := MessageSearchMediaRevision(textEdited); got != original {
		t.Fatalf("同一内容哈希的图片不应因正文或派生字段变化而换版本: %q != %q", got, original)
	}
	withQuotedImage := cloneRecallEvents([]MessageEvent{event})[0]
	withQuotedImage.Quoted = &QuotedMessage{
		MessageID: "quoted-image",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"file_unique_id": "quoted-stable-image",
		}}},
	}
	if got := MessageSearchMediaRevision(withQuotedImage); got != original {
		t.Fatalf("引用图片不应改变当前消息的媒体版本: %q != %q", got, original)
	}
	// 后台任务拿到的是已下载、带哈希的轻量副本；caption edit 可能还是只带
	// Telegram file_unique_id 的 raw payload。稳定平台 ID 相同就仍是同一张图。
	queued := historyImageDescriptionQueueEvent(event)
	rawEdit := MessageEvent{Segments: []MessageSegment{
		{Type: "text", Data: map[string]string{"text": "raw caption edit"}},
		{Type: "image", Data: map[string]string{"file_unique_id": "stable-telegram-image", "file_id": "new-file-id"}},
	}}
	if got, want := MessageSearchMediaRevision(queued), MessageSearchMediaRevision(rawEdit); got != want {
		t.Fatalf("同一 Telegram 图片的 enriched/raw 版本不一致: %q != %q", got, want)
	}
	replaced := cloneRecallEvents([]MessageEvent{event})[0]
	replaced.Segments[1].Data[imageContentSHA256Key] = hashB
	replaced.Segments[1].Data["file_unique_id"] = "replacement-telegram-image"
	if got := MessageSearchMediaRevision(replaced); got == original {
		t.Fatal("替换图片后媒体版本没有变化")
	}
	removed := event
	removed.Segments = removed.Segments[:1]
	if got := MessageSearchMediaRevision(removed); got != "" {
		t.Fatalf("移除图片后的媒体版本应为空，实际 %q", got)
	}
}

// 旧图片任务完成时，如果持久化消息已换图或删图，条件写入会拒绝它；调用方
// 不能再退回旧接口，也不能继续用旧事件重排语义索引。
func TestRefreshMessageImageSearchTextStopsWhenMediaRevisionIsStale(t *testing.T) {
	hash := strings.Repeat("a", 64)
	base := newSearchExtraTestStore()
	store := &rejectingRevisionSearchExtraStore{searchExtraTestStore: base}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	if err := store.SaveImageDescription(context.Background(), ImageDescriptionRecord{
		ContentSHA256: hash,
		Description:   "不应写回的旧图片描述",
	}); err != nil {
		t.Fatal(err)
	}
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "edited-image",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{imageContentSHA256Key: hash}}},
	}

	runtime.refreshMessageImageSearchText(context.Background(), event)
	if store.calls != 1 {
		t.Fatalf("revision-aware save calls = %d, want 1", store.calls)
	}
	if got := store.extra(sessionKey(event), event.MessageID); got != "" {
		t.Fatalf("stale description fell back to unguarded save: %q", got)
	}
}

// message_id 是内部标识：既要在系统提示词里禁掉，也要在工具结果里就近提醒。
func TestHistoryToolKeepsMessageIDsOutOfReplies(t *testing.T) {
	base := BotConfig{}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	relationship := RelationshipPolicyFor(UserMemoryProfile{}, base.OwnerID, "1")
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1"}
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false, relationship, true, nil)
	if !strings.Contains(prompt, promptInternalIdentifiers) {
		t.Fatalf("system prompt is missing the internal identifier rule: %q", prompt)
	}

	if !strings.Contains(prompt, promptQuoteHistoryMessage) {
		t.Fatalf("system prompt is missing the quote-instead-of-ID rule: %q", prompt)
	}

	// 工具结果里就近再提醒一次：禁令 + 该怎么做。
	tool := newDianaChatHistoryTool(runtime, event)
	notice := tool.idNotice()
	if !strings.Contains(notice, chatHistoryIDNotice) || !strings.Contains(notice, replyMarkerPrefix) {
		t.Fatalf("tool notice = %q", notice)
	}

	// 管理员把引用关掉时不教引用，只留禁令——那个动作会被配置丢掉。
	off := NewRuntime(BotConfig{ReplyReferenceMode: ReplyDecorationOff}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if notice := newDianaChatHistoryTool(off, event).idNotice(); notice != chatHistoryIDNotice {
		t.Fatalf("reply-off notice = %q", notice)
	}
	offPrompt := off.systemPromptWithRelationshipAndAgentTools(event, nil, false, relationship, true, nil)
	if strings.Contains(offPrompt, promptQuoteHistoryMessage) {
		t.Fatalf("reply-off prompt should not teach quoting: %q", offPrompt)
	}
}

// 描述在这台机器上早就生成过（或是升级前留下的记录）时，检索文本同样要补上，
// 否则老历史永远搜不到。
func TestExistingImageDescriptionStillBackfillsSearchText(t *testing.T) {
	imagePath, hash := writeRecallImageFixture(t)
	store := newSearchExtraTestStore()
	provider := &recallImageVisionProvider{}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetMessageHistoryStore(store)
	if err := store.SaveImageDescription(context.Background(), ImageDescriptionRecord{ContentSHA256: hash, Description: "早就描述过的截图"}); err != nil {
		t.Fatal(err)
	}
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "old-image",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"cached_file":         imagePath,
			imageContentSHA256Key: hash,
		}}},
	}

	runtime.enqueueHistoryImageDescriptions(event)
	waitForCondition(t, 2*time.Second, func() bool {
		return strings.Contains(store.extra(sessionKey(event), "old-image"), "早就描述过的截图")
	})
	if provider.callCount() != 0 {
		t.Fatalf("cached description should not trigger vision calls, got %d", provider.callCount())
	}
}

// 引用历史消息要真的落成引用：模型写在开头的标记指向一条旧消息时，出站消息
// 的 ReplyMessageID 必须是那条旧消息，正文里不能留下标记。
func TestOutgoingMarkerQuotesHistoryMessage(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "2001"}
	old := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "user-1", MessageID: "-1659533658",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "上下文压缩比例截图"}}}}
	runtime.mu.Lock()
	runtime.history[sessionKey(event)] = []MessageEvent{old}
	runtime.mu.Unlock()

	msg := runtime.applyOutgoingReplyMarker(context.Background(), event,
		OutgoingMessage{Text: replyMarkerPrefix + "-1659533658] 就是这张喵", ReplyMessageID: event.MessageID})
	if msg.ReplyMessageID != "-1659533658" {
		t.Fatalf("reply target = %q, want the history message", msg.ReplyMessageID)
	}
	if msg.Text != "就是这张喵" {
		t.Fatalf("body = %q, marker must not survive", msg.Text)
	}

	// 指向本会话里不存在的消息时只去掉标记，正文照发，不会发送失败。
	missing := runtime.applyOutgoingReplyMarker(context.Background(), event,
		OutgoingMessage{Text: replyMarkerPrefix + "999999] 找不到了", ReplyMessageID: event.MessageID})
	if missing.ReplyMessageID != "" || missing.Text != "找不到了" {
		t.Fatalf("missing target = %#v", missing)
	}
}
