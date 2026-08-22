// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 语义检索:词面检索(FTS/n-gram)只能命中字面重合的消息,「有什么吃的推荐」
// 永远搜不到「凤爪味道不错」。把消息经 embedding 模型转成向量后按余弦相似度
// 找近邻,就能按意思召回。
//
// 接入方式全程可退化:
//   - 需要 dict/semantic 开关(semantic_search_enabled,默认关)和一个
//     embedding 分组的 LLM 配置档,两者缺一功能整体不生效;
//   - 向量化在后台批量做,消息热路径只投一个非阻塞队列,队列满了直接丢
//     (漏索引只影响该条消息的语义召回,词面检索不受影响);
//   - 检索时先走词面,再拿查询向量找近邻,RRF 融合两路排序;embedding 调用
//     失败或超时就只用词面结果,不报错。
//
// 向量检索在存储层是 Go 暴力余弦扫。万级消息不需要 HNSW:近似索引换来的
// 是常驻内存和构建维护成本,量级到十万以上再考虑。

const (
	semanticIndexQueueSize  = 512
	semanticIndexBatchSize  = 16
	semanticIndexFlushEvery = 2 * time.Second
	semanticIndexTimeout    = 20 * time.Second
	semanticQueryTimeout    = 3 * time.Second
	semanticSearchOverfetch = 40
	semanticMinTextRunes    = 4
)

type semanticIndexItem struct {
	session   string
	messageID string
	text      string
}

func (r *Runtime) semanticSearchActive(cfg BotConfig) bool {
	if !boolValue(cfg.SemanticSearchEnabled, false) {
		return false
	}
	_, ok := r.embeddingProviderConfig()
	return ok
}

// embeddingProviderConfig 返回 embedding 分组下的配置档。多个时取第一个。
func (r *Runtime) embeddingProviderConfig() (llm.ProviderConfig, bool) {
	if r.llmStore == nil {
		return llm.ProviderConfig{}, false
	}
	for _, profile := range r.llmStore.Profiles().Profiles {
		if strings.EqualFold(strings.TrimSpace(profile.Group), llm.GroupEmbedding) &&
			strings.TrimSpace(profile.Config.Model) != "" {
			return profile.Config, true
		}
	}
	return llm.ProviderConfig{}, false
}

func (r *Runtime) messageVectorStore() MessageHistoryVectorStore {
	r.mu.RLock()
	store := r.messageStore
	r.mu.RUnlock()
	vectorStore, ok := store.(MessageHistoryVectorStore)
	if !ok {
		return nil
	}
	return vectorStore
}

// enqueueSemanticIndex 把一条已落库的消息投给后台向量化。非阻塞:队列满了
// 直接丢弃,绝不让消息处理路径等 embedding。
func (r *Runtime) enqueueSemanticIndex(event MessageEvent) {
	if strings.TrimSpace(event.MessageID) == "" || event.Kind == EventKindNotice {
		return
	}
	if !r.semanticSearchActive(r.effectiveConfigForEvent(event)) || r.messageVectorStore() == nil {
		return
	}
	text := strings.TrimSpace(historyPlainText(event))
	if len([]rune(text)) < semanticMinTextRunes {
		return
	}
	r.semanticIndexOnce.Do(func() {
		r.semanticIndexQueue = make(chan semanticIndexItem, semanticIndexQueueSize)
		go r.runSemanticIndexer()
	})
	select {
	case r.semanticIndexQueue <- semanticIndexItem{session: sessionKey(event), messageID: event.MessageID, text: text}:
	default:
	}
}

// runSemanticIndexer 攒批调用 embedding 接口,把向量写回存储。
func (r *Runtime) runSemanticIndexer() {
	batch := make([]semanticIndexItem, 0, semanticIndexBatchSize)
	timer := time.NewTimer(semanticIndexFlushEvery)
	defer timer.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		r.flushSemanticIndexBatch(batch)
		batch = batch[:0]
	}
	for {
		select {
		case item := <-r.semanticIndexQueue:
			batch = append(batch, item)
			if len(batch) >= semanticIndexBatchSize {
				flush()
			}
		case <-timer.C:
			flush()
			timer.Reset(semanticIndexFlushEvery)
		}
	}
}

func (r *Runtime) flushSemanticIndexBatch(batch []semanticIndexItem) {
	cfg, ok := r.embeddingProviderConfig()
	if !ok {
		return
	}
	store := r.messageVectorStore()
	if store == nil {
		return
	}
	texts := make([]string, len(batch))
	for index, item := range batch {
		texts[index] = item.text
	}
	ctx, cancel := context.WithTimeout(context.Background(), semanticIndexTimeout)
	defer cancel()
	vectors, err := r.embedTextsFunc()(ctx, cfg, texts)
	if err != nil {
		log.Printf("semantic index embed failed (%d items dropped): %v", len(batch), err)
		return
	}
	if len(vectors) != len(batch) {
		return
	}
	for index, item := range batch {
		if err := store.SaveMessageEventVector(ctx, item.session, item.messageID, cfg.Model, vectors[index]); err != nil {
			log.Printf("semantic index save failed: %v", err)
			return
		}
	}
}

func (r *Runtime) embedTextsFunc() func(context.Context, llm.ProviderConfig, []string) ([][]float32, error) {
	if r.embedTexts != nil {
		return r.embedTexts
	}
	return func(ctx context.Context, cfg llm.ProviderConfig, texts []string) ([][]float32, error) {
		return llm.EmbedTexts(ctx, cfg, texts)
	}
}

// semanticSearchEvents 把查询转成向量后按相似度召回历史消息。
// 任一环节失败都返回 nil,调用方只用词面结果。
func (r *Runtime) semanticSearchEvents(ctx context.Context, event MessageEvent, query string, fromTime, throughTime int64, crossSession bool) []MessageEvent {
	if !r.semanticSearchActive(r.effectiveConfigForEvent(event)) {
		return nil
	}
	store := r.messageVectorStore()
	if store == nil {
		return nil
	}
	cfg, ok := r.embeddingProviderConfig()
	if !ok {
		return nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	embedCtx, cancel := context.WithTimeout(ctx, semanticQueryTimeout)
	vectors, err := r.embedTextsFunc()(embedCtx, cfg, []string{query})
	cancel()
	if err != nil || len(vectors) != 1 {
		return nil
	}
	matched, err := store.SearchMessageEventsByVector(ctx, MessageHistoryVectorQuery{
		Session:       sessionKey(event),
		SessionPrefix: groupHistorySessionPrefix(event),
		Vector:        vectors[0],
		Model:         cfg.Model,
		FromTime:      fromTime,
		ThroughTime:   throughTime,
		Limit:         semanticSearchOverfetch,
		CrossSession:  crossSession,
	})
	if err != nil {
		return nil
	}
	return matched
}

// mergeSearchResultsRRF 用 Reciprocal Rank Fusion 融合词面与语义两路结果:
// 每条消息的得分是它在各路排名的 1/(60+rank) 之和,两路都靠前的排最前。
// 60 是 RRF 的常用平滑常数,防止单路第一名碾压另一路的稳定命中。
func mergeSearchResultsRRF(keyword, semantic []MessageEvent, limit int) []MessageEvent {
	if len(semantic) == 0 {
		if limit > 0 && len(keyword) > limit {
			return keyword[:limit]
		}
		return keyword
	}
	type entry struct {
		event MessageEvent
		score float64
	}
	key := func(event MessageEvent) string {
		if id := strings.TrimSpace(event.MessageID); id != "" {
			return event.GroupID + ":" + id
		}
		return event.GroupID + ":" + event.UserID + ":" + historyPlainText(event)
	}
	merged := make(map[string]*entry, len(keyword)+len(semantic))
	order := make([]string, 0, len(keyword)+len(semantic))
	accumulate := func(events []MessageEvent) {
		for rank, event := range events {
			id := key(event)
			item, ok := merged[id]
			if !ok {
				item = &entry{event: event}
				merged[id] = item
				order = append(order, id)
			}
			item.score += 1.0 / float64(60+rank+1)
		}
	}
	accumulate(keyword)
	accumulate(semantic)
	results := make([]MessageEvent, 0, len(order))
	for _, id := range order {
		results = append(results, merged[id].event)
	}
	sortStableByScore(results, func(event MessageEvent) float64 { return merged[key(event)].score })
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

func sortStableByScore(events []MessageEvent, score func(MessageEvent) float64) {
	for left := 1; left < len(events); left++ {
		for right := left; right > 0 && score(events[right]) > score(events[right-1]); right-- {
			events[right], events[right-1] = events[right-1], events[right]
		}
	}
}
