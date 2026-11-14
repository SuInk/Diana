// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
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
		"cursor":    toolStringParam("search 续读：原样填上一页的 next_cursor，query 等其他参数不变"),
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

// memorySearchCursor 是 search 的键集游标：上一页最后一条的 (得分, id)，加上排序用的
// 时间点和检索词指纹。续读时用同一个时间点重排，只给严格排在这一条后面的，翻页之间
// 有新记忆写入、旧记忆被注入路径回写 last_verified_at 也不会跳条或重复。
type memorySearchCursor struct {
	Version int     `json:"v"`
	Now     int64   `json:"t"`
	Score   float64 `json:"s"`
	ID      string  `json:"i"`
	Scope   string  `json:"q"`
}

func memorySearchScope(event MessageEvent, text string) string {
	sum := sha256.Sum256([]byte(sessionKey(event) + "\x00" + text))
	return hex.EncodeToString(sum[:8])
}

func (c memorySearchCursor) encode() string {
	body, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(body)
}

func decodeMemorySearchCursor(token, scope string) (*memorySearchCursor, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	invalid := fmt.Errorf("cursor 无效：请原样传回上一页结果里的 next_cursor（query 不能改），或不传 cursor 从头检索")
	if len(token) > 1024 {
		return nil, invalid
	}
	body, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, invalid
	}
	var cursor memorySearchCursor
	if json.Unmarshal(body, &cursor) != nil || cursor.Version != 1 || cursor.ID == "" || cursor.Scope != scope {
		return nil, invalid
	}
	return &cursor, nil
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
	var cursor *memorySearchCursor
	limit := chatHistoryCount(input, "limit", 8)
	switch op {
	case "search":
		if text == "" || len([]rune(text)) > 512 {
			return "", fmt.Errorf("query 必须为 1 到 512 字符")
		}
		var err error
		if cursor, err = decodeMemorySearchCursor(configToolString(input, "cursor"), memorySearchScope(t.event, text)); err != nil {
			return "", err
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
	query := r.memoryQueryForEvent(ctx, t.event, text, 0)
	if op == "read" {
		query.IDs = []string{id}
		query.MaxCandidates = 1
	} else {
		// 检索不设候选上限：自动注入只取 structuredMemoryLoadLimit 条候选、排序后最多给
		// 二十几条，工具要报真实总数、翻得完全部命中。
		query.Unlimited = true
		// 排序时间点钉进游标，续读用同一个时间点重排。去掉单调时钟读数：首页要是带着它
		// 算龄期，和续读时从游标还原的纯墙钟时间会差出零点几纳秒，得分对不上游标。
		query.Now = time.Unix(0, query.Now.UnixNano())
		if cursor != nil {
			query.Now = time.Unix(0, cursor.Now)
		}
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
			if extra, err := store.ListStructuredMemories(loadCtx, shared); err == nil {
				items = mergeRetrievedMemoryCandidates(items, extra)
			}
		}
		items = r.expandMemoryAssociations(loadCtx, store, query, t.event, items)
		markLinkedMemorySubjects(query, items)
		items = rankStructuredMemoriesWith(items, t.event, text, query.Now, toolMemoryRank)
	}
	total := len(items)
	if cursor != nil {
		after := cursor.Score
		rest := items[:0]
		for _, item := range items {
			if memoryRanksBefore(StructuredMemoryItem{RetrievalScore: after, ID: cursor.ID}, item) {
				rest = append(rest, item)
			}
		}
		items = rest
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
	scope := memorySearchScope(t.event, text)
	budget := agent.ToolOutputBudget(ctx)
	if budget <= 0 {
		budget = agent.ToolOutputMaxChars
	}
	return marshalToolResultPage(ctx, len(entries), 0, limit, func(n int, _ toolResultPage) any {
		page := toolCursorPage{ReturnedCount: n, RemainingCount: len(entries) - n}
		if page.HasMore = page.RemainingCount > 0 && n > 0 && op == "search"; page.HasMore {
			last := items[n-1]
			page.NextCursor = memorySearchCursor{Version: 1, Now: query.Now.UnixNano(), Score: last.RetrievalScore, ID: last.ID, Scope: scope}.encode()
			reason := ""
			if want := min(limit, len(entries)); n < want {
				reason = fmt.Sprintf("单次结果上限 %d 字只装下 %d 条，", budget, n)
			}
			page.PageNote = fmt.Sprintf("共命中 %d 条，按相关度排，本页 %d 条，%s还剩 %d 条没给：传 cursor=next_cursor、query 等其他参数不变接着读；没读完之前不要断言没有。",
				total, n, reason, page.RemainingCount)
		}
		return struct {
			Items []memoryToolItem `json:"items"`
			Total int              `json:"total"`
			toolCursorPage
			Guidance string `json:"guidance"`
		}{Items: entries[:n], Total: total, toolCursorPage: page, Guidance: guidance}
	})
}
