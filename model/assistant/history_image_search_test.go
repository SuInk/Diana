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

	body, err := marshalDianaChatHistoryResult(dianaChatHistoryResult{OK: true, Action: "search", Message: "已完成检索。"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, chatHistoryIDNotice) {
		t.Fatalf("tool result is missing the identifier notice: %s", body)
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
