// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

type crossGroupSearchCounter struct {
	*memoryMessageHistoryStore
	searches int
}

func (s *crossGroupSearchCounter) SearchMessageEvents(context.Context, MessageHistorySearchQuery) ([]MessageEvent, int, error) {
	s.searches++
	return nil, 0, nil
}

func crossGroupProbeEvent() MessageEvent {
	text := "上次说的那个转发合并功能后来修好了吗"
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "user", MessageID: "m2", Time: 200,
		RawMessage: text,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

// 回复链路上历史只在开头加载一次，之后 contextHistory 会被调用好几次（生成
// 回复、语义指代、视觉意图、图片来源……）。这些调用命中的都是 replyHistoryLoaded
// 缓存分支，不该顺带再做跨群全文检索——跨群上下文在首次加载时已经并进去了。
func TestContextHistorySkipsCrossGroupSearchOnCachedHistory(t *testing.T) {
	store := &crossGroupSearchCounter{memoryMessageHistoryStore: newMemoryMessageHistoryStore()}
	runtime := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)

	event := crossGroupProbeEvent()
	event.replyHistoryLoaded = true
	event.replyHistory = []MessageEvent{{Kind: EventKindGroup, GroupID: "123", UserID: "other", MessageID: "m1", RawMessage: "在的"}}

	for i := 0; i < 5; i++ {
		runtime.contextHistory(event)
	}
	if store.searches != 0 {
		t.Fatalf("缓存历史上不该发起跨群检索，实际 %d 次", store.searches)
	}
}

// 首次加载仍然要把跨群上下文并进来，别把功能一起关掉了。
func TestContextHistoryStillSearchesCrossGroupOnFirstLoad(t *testing.T) {
	store := &crossGroupSearchCounter{memoryMessageHistoryStore: newMemoryMessageHistoryStore()}
	runtime := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)

	runtime.contextHistory(crossGroupProbeEvent())
	if store.searches != 1 {
		t.Fatalf("首次加载应当检索一次跨群上下文，实际 %d 次", store.searches)
	}
}

func TestPromptHistoryPreservesCachedCrossGroupIdentityWithoutSearching(t *testing.T) {
	store := &crossGroupSearchCounter{memoryMessageHistoryStore: newMemoryMessageHistoryStore()}
	cfg := BotConfig{OwnerID: "100001", CrossGroupMemoryEnabled: boolPointer(true)}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	event := crossGroupProbeEvent()
	event.replyHistoryLoaded = true
	event.replyHistory = []MessageEvent{{Kind: EventKindGroup, GroupID: "other", UserID: "100001", SenderName: "old owner card", RawMessage: "相关历史", Time: 100, crossGroupContext: true}}
	history := runtime.promptContextHistory(event, cfg)
	if len(history) != 1 || !history[0].crossGroupContext || history[0].UserID != cfg.OwnerID || store.searches != 0 {
		t.Fatalf("cached reference lost or searched again: history=%+v searches=%d", history, store.searches)
	}
	cfg.CrossGroupMemoryEnabled = boolPointer(false)
	if history := runtime.promptContextHistory(event, cfg); len(history) != 0 {
		t.Fatal("disabled cross-group memory retained cached reference")
	}
}

// 线上那次：群里刚聊完 issue，主人说「我又忘了」，跨群检索捞到他在另一个群问的
// 「我又忘了滚木啥意思了」。接话评分的对话稿不标来源，这句被当成本群 38 分钟前说的。
// 判断类的调用只看本会话；跨群参考仍留给回复正文，那里带「[跨群历史]」标记。
func TestJudgementPayloadsExcludeCrossGroupReferences(t *testing.T) {
	cfg := BotConfig{BotAccount: "10001", CrossGroupMemoryEnabled: boolPointer(true)}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	text := "我又忘了"
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "123", UserID: "20001", SenderName: "Winter", MessageID: "m3", Time: 300,
		RawMessage: text, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
	event.replyHistoryLoaded = true
	event.replyHistory = []MessageEvent{
		{Kind: EventKindGroup, GroupID: "456", UserID: "20001", SenderName: "Winter", RawMessage: "我又忘了滚木啥意思了", Time: 100, crossGroupContext: true},
		{Kind: EventKindGroup, GroupID: "123", UserID: "10001", SenderName: "Diana", MessageID: "m2", RawMessage: "草稿还在，等你看看后台", Time: 290},
	}
	leaked := func(texts ...string) bool {
		for _, value := range texts {
			if strings.Contains(value, "滚木") {
				return true
			}
		}
		return false
	}

	proactive := runtime.proactiveReplyPayload(event, text)
	var proactiveTexts []string
	for _, item := range proactive.RecentMessages {
		proactiveTexts = append(proactiveTexts, item.Text)
	}
	if leaked(proactiveTexts...) || leaked(proactiveReplyTranscript(proactive)) || len(proactive.RecentMessages) != 1 {
		t.Fatalf("接话评分混入了跨群消息：%+v", proactive.RecentMessages)
	}
	var visualTexts []string
	for _, item := range runtime.visualIntentPayload(event, text).RecentMessages {
		visualTexts = append(visualTexts, item.Text)
	}
	if leaked(visualTexts...) {
		t.Fatalf("识图意图混入了跨群消息：%v", visualTexts)
	}
	evidence := runtime.collectBotReplyLoopEvidence(event, sessionOnlyHistory(runtime.contextHistory(event)))
	if leaked(evidence.RecentSameSenderMessages...) {
		t.Fatalf("空转证据把别的群的发言算成了同一发送者的近期消息：%v", evidence.RecentSameSenderMessages)
	}
	if leaked(runtime.pokeRecentChat(event)) {
		t.Fatal("戳一戳的最近聊天混入了跨群消息")
	}
	// 回复正文那一路照旧拿得到跨群参考。
	if history := runtime.contextHistory(event); len(history) != 2 || !history[0].crossGroupContext {
		t.Fatalf("回复历史丢了跨群参考：%+v", history)
	}
}
