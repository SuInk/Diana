// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
)

const crossGroupSemanticMinSimilarity = 0.65

func (r *Runtime) crossGroupSemanticEvents(event MessageEvent, store MessageHistoryStore, query string, through int64) ([]MessageEvent, string) {
	botConfig := r.effectiveConfigForEvent(event)
	if !boolValue(botConfig.SemanticSearchEnabled, false) {
		return nil, "未开启语义检索"
	}
	cfg, ok := r.embeddingProviderConfig(botConfig)
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
	vector, err := r.embedQueryText(withLLMUsagePurpose(withLLMUsageContext(ctx, event), "cross_group_semantic"), cfg, query)
	if errors.Is(err, errSemanticQueryBreakerOpen) {
		return nil, "embedding 服务连续失败，本次跳过"
	}
	if err != nil {
		return nil, "查询向量生成失败或超时"
	}
	events, err := vectors.SearchMessageEventsByVector(ctx, MessageHistoryVectorQuery{
		Session: sessionKey(event), SessionPrefix: groupHistorySessionPrefix(event), ExcludeSession: sessionKey(event),
		Vector: vector, Model: cfg.Model, FromTime: 0, ThroughTime: through,
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
