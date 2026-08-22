// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
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
