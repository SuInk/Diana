// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
)

const crossGroupSemanticMinSimilarity = 0.65

func (r *Runtime) crossGroupSemanticEvents(event MessageEvent, store MessageHistoryStore, query string, through int64) ([]MessageEvent, string) {
	if !boolValue(r.effectiveConfigForEvent(event).SemanticSearchEnabled, false) {
		return nil, "未开启语义检索"
	}
	cfg, ok := r.embeddingProviderConfig()
	if !ok {
		return nil, "未配置 embedding 模型"
	}
	vectors, ok := store.(MessageHistoryVectorStore)
	if !ok {
		return nil, "存储不支持向量检索"
	}
	if len([]rune(strings.TrimSpace(query))) < semanticMinTextRunes {
		return nil, "查询文本过短"
	}
	ctx, cancel := context.WithTimeout(context.Background(), semanticQueryTimeout)
	defer cancel()
	embedded, err := r.embedTextsFunc()(ctx, cfg, []string{query})
	if err != nil || len(embedded) != 1 || len(embedded[0]) == 0 {
		return nil, "查询向量生成失败或超时"
	}
	events, err := vectors.SearchMessageEventsByVector(ctx, MessageHistoryVectorQuery{
		Session: sessionKey(event), SessionPrefix: groupHistorySessionPrefix(event), ExcludeSession: sessionKey(event),
		Vector: embedded[0], Model: cfg.Model, FromTime: 0, ThroughTime: through,
		Limit: crossGroupContextSearchLimit, CrossSession: true, MinSimilarity: crossGroupSemanticMinSimilarity,
	})
	if err != nil {
		return nil, "向量检索失败或超时"
	}
	if len(events) == 0 {
		return nil, "没有达到相似度门槛的已索引消息"
	}
	return events, "已执行"
}

func crossGroupCandidateKey(event MessageEvent) string {
	if event.MessageID != "" {
		return event.GroupID + "\x00" + event.MessageID
	}
	return event.GroupID + "\x00" + event.UserID + "\x00" + historyPlainText(event)
}
