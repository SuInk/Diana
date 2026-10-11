// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

type semanticFakeStore struct {
	keyword     []MessageEvent
	semantic    []MessageEvent
	vectorCalls int
	vectorQuery MessageHistoryVectorQuery
	saved       []string
}

type semanticRevisionStore struct {
	mu         sync.Mutex
	vectors    map[string][]float32
	operations []string
	deletedIn  []string
}

func newSemanticRevisionStore() *semanticRevisionStore {
	return &semanticRevisionStore{vectors: map[string][]float32{}}
}

func (s *semanticRevisionStore) AppendMessageEvent(context.Context, string, MessageEvent) error {
	return nil
}

func (s *semanticRevisionStore) ListRecentMessageEvents(context.Context, string, int) ([]MessageEvent, error) {
	return nil, nil
}

func (s *semanticRevisionStore) SaveMessageEventVector(_ context.Context, _ string, messageID string, _ string, vector []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vectors[messageID] = append([]float32(nil), vector...)
	s.operations = append(s.operations, "save:"+messageID)
	return nil
}

func (s *semanticRevisionStore) DeleteMessageEventVector(_ context.Context, session string, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.vectors, messageID)
	s.operations = append(s.operations, "delete:"+messageID)
	s.deletedIn = append(s.deletedIn, session)
	return nil
}

func (s *semanticRevisionStore) lastDeleteSession() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.deletedIn) == 0 {
		return ""
	}
	return s.deletedIn[len(s.deletedIn)-1]
}

func (s *semanticRevisionStore) SearchMessageEventsByVector(context.Context, MessageHistoryVectorQuery) ([]MessageEvent, error) {
	return nil, nil
}

func (s *semanticRevisionStore) vector(messageID string) ([]float32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vector, ok := s.vectors[messageID]
	return append([]float32(nil), vector...), ok
}

func (s *semanticRevisionStore) operationCount(operation string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, current := range s.operations {
		if current == operation {
			count++
		}
	}
	return count
}

func (s *semanticRevisionStore) operationsFor(messageID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	suffix := ":" + messageID
	operations := make([]string, 0, 2)
	for _, operation := range s.operations {
		if strings.HasSuffix(operation, suffix) {
			operations = append(operations, operation)
		}
	}
	return operations
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
	result, err := tool.search(context.Background(), map[string]any{"query": "有什么吃的推荐", "order": "relevance"})
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

func TestSemanticIndexerDistinguishesFirstSeenAndKnownEdits(t *testing.T) {
	store := newSemanticRevisionStore()
	embedCalls := 0
	runtime := newSemanticRuntime(t, true, store, &embedCalls)
	// Keep enqueue deterministic: this test drains and flushes the queue itself.
	runtime.semanticIndexQueue = make(chan semanticIndexItem, 2)
	runtime.semanticIndexOnce.Do(func() {})

	firstSeen := semanticTestEvent(10, "edited-first-seen", "第一次看到的就是编辑后正文")
	firstSeen.Platform = PlatformTelegram
	firstSeen.Edited = true
	firstSeen.EditTime = 20
	firstSeen.TelegramUpdateID = 100
	runtime.enqueueSemanticIndex(firstSeen)
	firstItem := <-runtime.semanticIndexQueue
	if firstItem.invalidate || firstItem.text == "" {
		t.Fatalf("first-seen edit queued as tombstone: %+v", firstItem)
	}
	runtime.flushSemanticIndexBatch([]semanticIndexItem{firstItem})
	if embedCalls != 1 {
		t.Fatalf("first-seen edit embedding calls=%d, want 1", embedCalls)
	}
	if _, ok := store.vector(firstSeen.MessageID); !ok {
		t.Fatal("first-seen edit did not create a semantic vector")
	}

	known := firstSeen
	known.EditRefreshOnly = true
	known.EditTime = 21
	known.TelegramUpdateID = 101
	known.RawMessage = "已知消息的新编辑正文"
	known.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": known.RawMessage}}}
	runtime.enqueueSemanticIndex(known)
	knownItem := <-runtime.semanticIndexQueue
	if !knownItem.invalidate || knownItem.text != "" {
		t.Fatalf("known edit did not queue a pure tombstone: %+v", knownItem)
	}
	runtime.flushSemanticIndexBatch([]semanticIndexItem{knownItem})
	if embedCalls != 1 {
		t.Fatalf("known edit started another embedding: calls=%d", embedCalls)
	}
	if _, ok := store.vector(firstSeen.MessageID); ok {
		t.Fatal("known edit left the prior semantic vector in place")
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

// 编辑的 tombstone 必须和普通向量写入共用同一个串行队列。这里让旧正文的
// embedding 卡在半路，再送入编辑；旧向量先落库也会被后续编辑删除。已知编辑
// 无论新正文长短都只更新词面索引，不新增模型调用，最终保持无向量。
func TestSemanticIndexerEditWinsAfterOlderEmbeddingFinishes(t *testing.T) {
	for _, test := range []struct {
		name       string
		editedText string
	}{
		{name: "long text leaves tombstone", editedText: "这是编辑后的新版本正文"},
		{name: "short text leaves tombstone", editedText: "短"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newSemanticRevisionStore()
			runtime := newSemanticRuntime(t, true, store, nil)
			runtime.semanticIndexQueue = make(chan semanticIndexItem, semanticIndexQueueSize)
			runtime.semanticIndexOnce.Do(func() {})
			indexerDone := make(chan struct{})
			go func() {
				runtime.runSemanticIndexer()
				close(indexerDone)
			}()
			t.Cleanup(func() {
				close(runtime.semanticIndexQueue)
				select {
				case <-indexerDone:
				case <-time.After(2 * time.Second):
					t.Error("语义索引协程没有退出")
				}
			})
			firstEmbedStarted := make(chan struct{})
			releaseFirstEmbed := make(chan struct{})
			var embedCalls atomic.Int32
			runtime.embedTexts = func(_ context.Context, _ llm.ProviderConfig, texts []string) ([][]float32, error) {
				if embedCalls.Add(1) == 1 {
					close(firstEmbedStarted)
					<-releaseFirstEmbed
				}
				vectors := make([][]float32, len(texts))
				for index, text := range texts {
					if strings.Contains(text, "编辑后的新版本") {
						vectors[index] = []float32{0, 1}
					} else {
						vectors[index] = []float32{1, 0}
					}
				}
				return vectors, nil
			}

			original := semanticTestEvent(10, "target", "这是编辑之前的旧版本正文")
			runtime.enqueueSemanticIndex(original)
			// 填满首批，让 embedding 立刻开始，避免测试依赖两秒定时器。
			for index := 1; index < semanticIndexBatchSize; index++ {
				runtime.enqueueSemanticIndex(semanticTestEvent(int64(10+index), "filler-"+strconv.Itoa(index), "用于填满语义索引批次的正文"))
			}
			select {
			case <-firstEmbedStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("旧正文 embedding 未开始")
			}

			edited := semanticTestEvent(10, "target", test.editedText)
			edited.Edited = true
			edited.EditRefreshOnly = true
			runtime.enqueueSemanticIndexForSession(edited, "legacy:group:g1")
			close(releaseFirstEmbed)

			waitForCondition(t, 4*time.Second, func() bool {
				if store.operationCount("delete:target") == 0 {
					return false
				}
				_, exists := store.vector("target")
				return !exists
			})
			if got := embedCalls.Load(); got != 1 {
				t.Fatalf("edit 新增了 embedding 调用：calls=%d", got)
			}
			if got := store.lastDeleteSession(); got != "legacy:group:g1" {
				t.Fatalf("tombstone session=%q, want durable legacy session", got)
			}
			if got := strings.Join(store.operationsFor("target"), ","); got != "save:target,delete:target" {
				t.Fatalf("旧向量与 tombstone 顺序错误：%s", got)
			}
		})
	}
}
