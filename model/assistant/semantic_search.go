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
//     embedding 分组的提供商配置档,两者缺一功能整体不生效;
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
	// semanticIndexDescriptionTimeout 限制入库路径上读图片描述的那次点查：
	// 索引是尽力而为的后台活，不该拖住消息处理。
	semanticIndexDescriptionTimeout = 2 * time.Second
)

type semanticIndexItem struct {
	session   string
	messageID string
	text      string
	// invalidate is ordered on the same queue as vector writes. An edited
	// message must delete the previous vector only after every older embedding
	// job has finished, otherwise that older job can write the stale vector back.
	invalidate bool
}

// messageHistoryVectorDeleteStore is an optional extension implemented by the
// durable vector store. Keeping it separate preserves compatibility with small
// in-memory MessageHistoryVectorStore implementations.
type messageHistoryVectorDeleteStore interface {
	DeleteMessageEventVector(ctx context.Context, session, messageID string) error
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

// enqueueSemanticIndex 把一条已落库的消息投给后台向量化。普通消息在队列满时
// 直接丢弃；编辑不能丢 tombstone，因此会等待队列接收。
func (r *Runtime) enqueueSemanticIndex(event MessageEvent) {
	r.enqueueSemanticIndexForSession(event, sessionKey(event))
}

// enqueueSemanticIndexForSession lets durable edit handling use the session of
// the row it actually refreshed. That matters for legacy rows whose stored
// session predates ContextNamespace: invalidating the live session would leave
// an older in-flight vector able to recreate the legacy row's stale embedding.
func (r *Runtime) enqueueSemanticIndexForSession(event MessageEvent, session string) {
	// 戳一戳没有可检索的内容，正文只是一句固定的「戳了戳某某」。
	if strings.TrimSpace(event.MessageID) == "" || event.Kind == EventKindNotice || isPokeHistoryEvent(event) {
		return
	}
	session = strings.TrimSpace(session)
	if session == "" {
		session = sessionKey(event)
	}
	store := r.messageVectorStore()
	if store == nil {
		return
	}
	item := semanticIndexItem{
		session:    session,
		messageID:  event.MessageID,
		invalidate: event.Edited && event.EditRefreshOnly,
	}
	active := r.semanticSearchActive(r.effectiveConfigForEvent(event))
	// 已知编辑只刷新历史和词面索引，不触发新的模型调用。语义侧只排一个
	// tombstone；无论新正文长短，都不会为这次 edit 再请求 embedding。
	if !item.invalidate && active && !r.groupDormant(event) && !r.statusCommandActive(event, PlainText(event.Segments)) {
		// 纯图片消息的正文是空的，只有图片描述能代表它。描述是后台异步生成的：
		// 第一次入库时通常还没有，等描述落库后 refreshMessageImageSearchText 会把
		// 这条消息重新排一次索引，那时才真正进向量库。
		lookupCtx, cancel := context.WithTimeout(context.Background(), semanticIndexDescriptionTimeout)
		text := semanticIndexText(lookupCtx, r, event)
		cancel()
		if len([]rune(text)) >= semanticMinTextRunes {
			item.text = text
		}
	}
	// 普通短消息从来没有向量，不必排队。已知消息的编辑无论新正文长短
	// 都必须排 tombstone，清掉旧版本已经生成的向量。
	if item.text == "" && !item.invalidate {
		return
	}
	if item.invalidate {
		if _, ok := store.(messageHistoryVectorDeleteStore); !ok && item.text == "" {
			return
		}
	}
	r.semanticIndexOnce.Do(func() {
		r.semanticIndexQueue = make(chan semanticIndexItem, semanticIndexQueueSize)
		go func() {
			defer recoverGoroutinePanic("semanticSearch.indexer")
			r.runSemanticIndexer()
		}()
	})
	if item.invalidate {
		// 编辑不能像普通消息一样在队列满时丢弃。等出一个位置，才能保证它排在
		// 所有已接收的旧向量任务之后，最终状态一定对应最新正文。
		r.semanticIndexQueue <- item
		return
	}
	select {
	case r.semanticIndexQueue <- item:
	default:
	}
}

// semanticIndexText 是一条消息进向量库时的文本：正文加上已经生成的图片描述。
func semanticIndexText(ctx context.Context, r *Runtime, event MessageEvent) string {
	text := strings.TrimSpace(historyPlainText(event))
	descriptions := r.messageImageDescriptionText(ctx, event)
	if descriptions == "" {
		return text
	}
	if text == "" {
		return descriptions
	}
	return text + "\n" + descriptions
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
		case item, ok := <-r.semanticIndexQueue:
			if !ok {
				flush()
				return
			}
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
	store := r.messageVectorStore()
	if store == nil {
		return
	}
	cfg, hasConfig := r.embeddingProviderConfig()
	texts := make([]string, 0, len(batch))
	textIndexes := make([]int, 0, len(batch))
	for index, item := range batch {
		if item.text == "" {
			continue
		}
		texts = append(texts, item.text)
		textIndexes = append(textIndexes, index)
	}

	vectorsByItem := make(map[int][]float32, len(texts))
	if hasConfig && len(texts) > 0 {
		embedCtx, cancel := context.WithTimeout(context.Background(), semanticIndexTimeout)
		embedCtx = withLLMUsagePurpose(embedCtx, "semantic_index")
		vectors, err := r.embedTextsFunc()(embedCtx, cfg, texts)
		cancel()
		if err != nil {
			log.Printf("semantic index embed failed (%d items dropped): %v", len(texts), err)
			r.recordBackgroundFailure("semantic_index_failed", "语义检索的向量生成失败，这批消息以后按语义搜不到", "", err,
				map[string]any{"dropped": len(texts), "model": cfg.Model})
		} else if len(vectors) == len(texts) {
			for index, vector := range vectors {
				vectorsByItem[textIndexes[index]] = vector
			}
		}
	}

	// embedding 可能吃满自己的超时，删除旧向量不能复用那个已取消的 ctx。
	// 按原始队列顺序执行 save/delete/save，才能让同批里的编辑覆盖旧任务。
	writeCtx, cancel := context.WithTimeout(context.Background(), semanticIndexTimeout)
	defer cancel()
	deleteStore, _ := store.(messageHistoryVectorDeleteStore)
	for index, item := range batch {
		if item.invalidate && deleteStore != nil {
			if err := deleteStore.DeleteMessageEventVector(writeCtx, item.session, item.messageID); err != nil {
				log.Printf("semantic index invalidate failed: %v", err)
				r.recordBackgroundFailure("semantic_index_failed", "语义检索的旧向量删除失败", "semantic_index_delete_failed", err,
					map[string]any{"message_id": item.messageID})
			}
		}
		vector, ok := vectorsByItem[index]
		if !ok {
			continue
		}
		if err := store.SaveMessageEventVector(writeCtx, item.session, item.messageID, cfg.Model, vector); err != nil {
			log.Printf("semantic index save failed: %v", err)
			r.recordBackgroundFailure("semantic_index_failed", "语义检索的向量写入失败，这条消息以后按语义搜不到", "semantic_index_save_failed", err,
				map[string]any{"message_id": item.messageID, "model": cfg.Model})
		}
	}
}

func (r *Runtime) embedTextsFunc() func(context.Context, llm.ProviderConfig, []string) ([][]float32, error) {
	if r.embedTexts != nil {
		return r.embedTexts
	}
	return func(ctx context.Context, cfg llm.ProviderConfig, texts []string) ([][]float32, error) {
		started := time.Now()
		// embedding 也占着同一份供应商配额，并发计数和下面的用量记账一样要在这里补。
		release := r.llmConcurrency.begin(string(cfg.Provider), cfg.Model, started)
		vectors, usage, err := llm.EmbedTextsWithUsage(ctx, cfg, texts)
		release()
		if err == nil {
			// embedding 不走文本 provider 链，装饰器记不到，在这里补记。
			var event MessageEvent
			if state := llmUsageFromContext(ctx); state != nil {
				event = state.event
			}
			r.recordLLMUsage(ctx, event, cfg.Provider, cfg.Model, usage, llmCallPurpose(ctx), time.Since(started), 0)
		}
		return vectors, err
	}
}

type semanticSearchPurposeKey struct{}

// withSemanticSearchPurpose 让调用方给这次检索的 embedding 标上自己的用途（比如
// 表情包搜索）。不能直接沿用 ctx 里的用量用途：主回复的 ctx 带着 reply，照抄的话
// embedding 模型会被当成主对话模型。
func withSemanticSearchPurpose(ctx context.Context, purpose string) context.Context {
	return context.WithValue(ctx, semanticSearchPurposeKey{}, strings.TrimSpace(purpose))
}

func semanticSearchPurpose(ctx context.Context) string {
	if purpose, _ := ctx.Value(semanticSearchPurposeKey{}).(string); purpose != "" {
		return purpose
	}
	return "semantic_search"
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
	embedCtx = withLLMUsagePurpose(withLLMUsageContext(embedCtx, event), semanticSearchPurpose(ctx))
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
