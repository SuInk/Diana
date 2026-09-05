// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const dianaMemoryToolName = "diana.memory"

type dianaMemoryTool struct {
	runtime *Runtime
	event   MessageEvent
}

func (t *dianaMemoryTool) Name() string { return dianaMemoryToolName }
func (t *dianaMemoryTool) Description() string {
	return "按需查阅已提取的长期记忆，不需要 embedding。先 search 获取简短索引，再 read 读取相关记忆全文与提取证据。未命中时可根据任务换关键词，不要编造历史；原始聊天请使用 diana.chat_history。"
}
func (t *dianaMemoryTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("search 查询记忆索引；read 读取单条当前有效记忆。", "search", "read"),
		"query":     toolStringParam("search 必填，任务相关关键词，最多 512 字符。"),
		"id":        toolStringParam("read 必填，search 返回的记忆 ID。"),
		"limit":     toolIntParam("search 返回数量，默认 8。", 1, 12),
	})
}

type memoryToolItem struct {
	ID              string    `json:"id"`
	Topic           string    `json:"topic"`
	Entity          string    `json:"entity,omitempty"`
	Snippet         string    `json:"snippet,omitempty"`
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
	if !boolValue(r.effectiveConfigForEvent(t.event).LongTermMemoryEnabled, true) {
		return "", fmt.Errorf("长期记忆未启用")
	}
	r.mu.RLock()
	store := r.structuredMemory
	r.mu.RUnlock()
	if store == nil {
		return "", fmt.Errorf("记忆存储未配置")
	}
	op := strings.TrimSpace(configToolString(input, "operation"))
	text := strings.TrimSpace(configToolString(input, "query"))
	var id string
	limit := chatHistoryPositiveInt(input, "limit", 8, 12)
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
		limit = 1
	default:
		return "", fmt.Errorf("operation 必须为 search 或 read")
	}
	query := r.memoryQueryForEvent(t.event, text, structuredMemoryLoadLimit)
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
		items = rankStructuredMemories(items, t.event, text, query.Now)
	}
	result := struct {
		Items    []memoryToolItem `json:"items"`
		Limited  bool             `json:"limited"`
		Guidance string           `json:"guidance"`
	}{Items: []memoryToolItem{}, Guidance: "检索范围遵循当前记忆开关；空结果可能是不存在、已失效、被新版替代或不可访问。旧记忆不代表当前事实，必要时核对；证据是提取时的片段，不是完整原始聊天。"}
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
		entry := memoryToolItem{ID: item.ID, Topic: item.Topic, Entity: item.Entity, Origin: origin, Version: item.Version, UpdatedAt: item.UpdatedAt, Reason: item.RetrievalReason}
		if op == "read" {
			entry.Content, entry.Evidence = item.Content, item.Evidence
			if item.SourceSession == query.Session {
				entry.SourceMessageID = item.SourceMessageID
			}
		} else {
			entry.Snippet = truncateRunes(item.Content, 157)
		}
		result.Items = append(result.Items, entry)
		encoded, _ := json.Marshal(result)
		if len([]rune(string(encoded))) > 6000 {
			result.Items = result.Items[:len(result.Items)-1]
			result.Limited = true
			break
		}
		if len(result.Items) >= limit {
			result.Limited = len(items) > limit
			break
		}
	}
	body, err := json.Marshal(result)
	return string(body), err
}
