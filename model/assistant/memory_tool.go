// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const dianaMemoryToolName = "memory"

type dianaMemoryTool struct {
	runtime *Runtime
	event   MessageEvent
}

func (t *dianaMemoryTool) Name() string { return dianaMemoryToolName }
func (t *dianaMemoryTool) Description() string {
	return "查阅已提取的长期记忆：先 search 拿索引，再 read 读全文和证据。未命中可换词，不要编造历史；原始聊天用 chat_history。"
}
func (t *dianaMemoryTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("search 查索引；read 读单条全文", "search", "read"),
		"query":     toolStringParam("search 必填：关键词，≤512 字符"),
		"id":        toolStringParam("read 必填：search 返回的记忆 ID"),
		"limit":     toolCountParam("search 条数，默认 8，不设上限；超出单次结果上限时按整条分页", 1),
		"offset":    toolCountParam("search 从第几条起（0 起），续读时填上次的 next_offset", 0),
	})
}

type memoryToolItem struct {
	ID              string    `json:"id"`
	Topic           string    `json:"topic"`
	Subject         string    `json:"subject,omitempty"`
	Recorded        string    `json:"recorded"`
	Entity          string    `json:"entity,omitempty"`
	Content         string    `json:"content,omitempty"`
	Evidence        string    `json:"evidence,omitempty"`
	Origin          string    `json:"origin"`
	Version         int       `json:"version"`
	UpdatedAt       time.Time `json:"updated_at"`
	SourceMessageID string    `json:"source_message_id,omitempty"`
	Reason          string    `json:"reason,omitempty"`
}

func (t *dianaMemoryTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("memory runtime unavailable")
	}
	r := t.runtime
	r.mu.RLock()
	store := r.structuredMemory
	r.mu.RUnlock()
	if store == nil {
		return "", fmt.Errorf("记忆存储未配置")
	}
	op := strings.TrimSpace(configToolString(input, "operation"))
	text := strings.TrimSpace(configToolString(input, "query"))
	var id string
	limit := chatHistoryCount(input, "limit", 8)
	offset := toolPageOffset(input)
	switch op {
	case "search":
		if text == "" || len([]rune(text)) > 512 {
			return "", fmt.Errorf("query 必须为 1 到 512 字符")
		}
	case "read":
		id = strings.TrimSpace(configToolString(input, "id"))
		if id == "" || len(id) > 128 {
			return "", fmt.Errorf("无效的记忆 ID")
		}
		text = ""
		limit, offset = 1, 0
	default:
		return "", fmt.Errorf("operation 必须为 search 或 read")
	}
	query := r.memoryQueryForEvent(ctx, t.event, text, structuredMemoryLoadLimit)
	if op == "read" {
		query.IDs = []string{id}
		query.MaxCandidates = 1
	}
	loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	items, err := store.ListStructuredMemories(loadCtx, query)
	if err != nil {
		return "", err
	}
	if op == "search" {
		if query.CrossGroup || len(query.CrossPlatformGroupPrefixes) > 0 {
			shared := query
			shared.SharedPublicOnly = true
			shared.MaxCandidates = sharedPublicMemoryLoadLimit
			if extra, err := store.ListStructuredMemories(loadCtx, shared); err == nil {
				items = mergeRetrievedMemoryCandidates(items, extra)
			}
		}
		items = r.expandMemoryAssociations(loadCtx, store, query, t.event, items)
		markLinkedMemorySubjects(query, items)
		items = rankStructuredMemories(items, t.event, text, query.Now)
	}
	entries := make([]memoryToolItem, 0, len(items))
	for _, item := range items {
		origin := "当前会话"
		if item.SourceSession != query.Session {
			origin = "同一机器人其他会话"
			for _, prefix := range query.CrossPlatformGroupPrefixes {
				if strings.HasPrefix(item.SourceSession, prefix) {
					origin = "其他平台群公共记忆"
				}
			}
		}
		// 谁的、什么时候记下的：不给这两样，模型只能把「早上吃了布洛芬」安到当前发言者头上、当成今天。
		// search 也给全文，不再截成片段；read 另外带证据和来源消息。
		entry := memoryToolItem{ID: item.ID, Topic: item.Topic, Subject: firstNonEmpty(strings.TrimSpace(item.SubjectName), strings.TrimSpace(item.SubjectUserID)), Recorded: memoryRecordedLabel(item.SourceEventTime, item.CreatedAt, time.Now()), Entity: item.Entity, Content: item.Content, Origin: origin, Version: item.Version, UpdatedAt: item.UpdatedAt, Reason: item.RetrievalReason}
		if op == "read" {
			entry.Evidence = item.Evidence
			if item.SourceSession == query.Session {
				entry.SourceMessageID = item.SourceMessageID
			}
		}
		entries = append(entries, entry)
	}
	const guidance = "检索范围遵循当前记忆开关；空结果可能是不存在、已失效、被新版替代或不可访问。旧记忆不代表当前事实，必要时核对；recorded 是记下的日期，内容里的时间说法相对那一天；证据是提取时的片段，不是完整原始聊天。"
	return marshalToolResultPage(ctx, len(entries), offset, limit, func(n int, page toolResultPage) any {
		start := min(page.Offset, len(entries))
		return struct {
			Items []memoryToolItem `json:"items"`
			toolResultPage
			Guidance string `json:"guidance"`
		}{Items: entries[start : start+n], toolResultPage: page, Guidance: guidance}
	})
}
