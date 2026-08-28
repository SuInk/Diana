// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type semanticFakeStore struct {
	keyword     []MessageEvent
	semantic    []MessageEvent
	vectorCalls int
	vectorQuery MessageHistoryVectorQuery
	saved       []string
}

func (s *semanticFakeStore) AppendMessageEvent(context.Context, string, MessageEvent) error {
	return nil
}

func (s *semanticFakeStore) ListRecentMessageEvents(context.Context, string, int) ([]MessageEvent, error) {
	return nil, nil
}

func (s *semanticFakeStore) SearchMessageEvents(_ context.Context, _ MessageHistorySearchQuery) ([]MessageEvent, int, error) {
	return append([]MessageEvent(nil), s.keyword...), len(s.keyword), nil
}

func (s *semanticFakeStore) SaveMessageEventVector(_ context.Context, _ string, messageID string, _ string, _ []float32) error {
	s.saved = append(s.saved, messageID)
	return nil
}

func (s *semanticFakeStore) SearchMessageEventsByVector(_ context.Context, query MessageHistoryVectorQuery) ([]MessageEvent, error) {
	s.vectorCalls++
	s.vectorQuery = query
	return append([]MessageEvent(nil), s.semantic...), nil
}

func semanticTestEvent(at int64, messageID, text string) MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, Platform: "onebot", GroupID: "g1", UserID: "u1", SenderName: "甲",
		MessageID: messageID, RawMessage: text,
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
		Time:     at,
	}
}

func newSemanticRuntime(t *testing.T, enabled bool, store MessageHistoryStore, embeds *int) *Runtime {
	t.Helper()
	profiles := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "chat", Name: "chat", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "gpt-x", APIKey: "k"}},
			{ID: "embed", Name: "embed", Group: llm.GroupEmbedding, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "embed-v1", APIKey: "k"}},
		},
	}}
	runtime := NewRuntime(BotConfig{SemanticSearchEnabled: boolPointer(enabled)}, nilChannel{}, NewPluginManager(), profiles, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	runtime.embedTexts = func(_ context.Context, cfg llm.ProviderConfig, texts []string) ([][]float32, error) {
		if embeds != nil {
			*embeds += len(texts)
		}
		if cfg.Model != "embed-v1" {
			t.Fatalf("embedding 应当用 embedding 分组的配置档,实际 %q", cfg.Model)
		}
		vectors := make([][]float32, len(texts))
		for index := range vectors {
			vectors[index] = []float32{1, 0}
		}
		return vectors, nil
	}
	return runtime
}

// 开着语义检索时,历史检索的结果是词面与语义两路融合;语义那路必须带着
// embedding 模型名和会话范围去查。
func TestHistorySearchMergesSemanticResults(t *testing.T) {
	store := &semanticFakeStore{
		keyword:  []MessageEvent{semanticTestEvent(30, "kw", "字面命中")},
		semantic: []MessageEvent{semanticTestEvent(20, "sem", "语义命中"), semanticTestEvent(30, "kw", "字面命中")},
	}
	embeds := 0
	runtime := newSemanticRuntime(t, true, store, &embeds)
	event := semanticTestEvent(100, "trigger", "有什么吃的推荐")

	tool := &dianaChatHistoryTool{runtime: runtime, event: event}
	result, err := tool.search(context.Background(), map[string]any{"query": "有什么吃的推荐"})
	if err != nil {
		t.Fatal(err)
	}
	if embeds != 1 {
		t.Fatalf("查询应向量化一次,实际 %d", embeds)
	}
	if store.vectorCalls != 1 || store.vectorQuery.Model != "embed-v1" {
		t.Fatalf("向量检索没有按 embedding 模型执行:%+v", store.vectorQuery)
	}
	joined := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		joined = append(joined, item.Text)
	}
	all := strings.Join(joined, "|")
	if !strings.Contains(all, "字面命中") || !strings.Contains(all, "语义命中") {
		t.Fatalf("两路结果应当融合,实际:%s", all)
	}
	// 两路都命中的消息只出现一次。
	if strings.Count(all, "字面命中") != 1 {
		t.Fatalf("去重失败:%s", all)
	}
}

// 开关关着时不许发起任何 embedding 调用。
func TestHistorySearchSkipsSemanticWhenDisabled(t *testing.T) {
	store := &semanticFakeStore{keyword: []MessageEvent{semanticTestEvent(30, "kw", "字面命中")}}
	embeds := 0
	runtime := newSemanticRuntime(t, false, store, &embeds)
	event := semanticTestEvent(100, "trigger", "随便问问")

	tool := &dianaChatHistoryTool{runtime: runtime, event: event}
	if _, err := tool.search(context.Background(), map[string]any{"query": "随便问问"}); err != nil {
		t.Fatal(err)
	}
	if embeds != 0 || store.vectorCalls != 0 {
		t.Fatalf("未启用时不该有语义调用:embeds=%d vector=%d", embeds, store.vectorCalls)
	}
}

// RRF 融合:两路都靠前的排最前,单路独有的按各自排名跟上,重复只留一份。
func TestMergeSearchResultsRRF(t *testing.T) {
	both := semanticTestEvent(10, "both", "两路都有")
	kwOnly := semanticTestEvent(20, "kw", "只在词面")
	semOnly := semanticTestEvent(30, "sem", "只在语义")
	merged := mergeSearchResultsRRF(
		[]MessageEvent{both, kwOnly},
		[]MessageEvent{both, semOnly},
		10,
	)
	if len(merged) != 3 {
		t.Fatalf("应有 3 条,实际 %d", len(merged))
	}
	if merged[0].MessageID != "both" {
		t.Fatalf("两路都命中的应排最前,实际 %s", merged[0].MessageID)
	}
}
