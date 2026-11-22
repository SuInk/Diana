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
	return "查阅已提取的长期记忆：先 search 拿索引，再 read 读全文和证据。未命中可换词，不要编造历史；原始聊天用 chat_history。记忆是后台提炼的，会记错：有人指出某条记错了，核实属实后用 forget 删掉，别只口头认错。"
}
func (t *dianaMemoryTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("search 查索引；read 读单条全文；forget 删掉一条记错或过时的记忆", "search", "read", "forget"),
		"query":     toolStringParam("search 必填：关键词，≤512 字符"),
		"id":        toolStringParam("read、forget 必填：search 返回的记忆 ID"),
		"reason":    toolStringParam("forget 必填：哪里记错了、怎么核实的，≤200 字符"),
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
	case "forget":
		return t.forget(ctx, store, strings.TrimSpace(configToolString(input, "id")), strings.TrimSpace(configToolString(input, "reason")))
	default:
		return "", fmt.Errorf("operation 必须为 search、read 或 forget")
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

// forget 删掉一条记错或过时的记忆。
//
// 记忆出错时以前只有本人开口门控才会删，旁人指出记错了，机器人只能口头认错，下一轮
// 这条照旧注入、照旧被当成事实。现在核实属实后能直接删，但谁都能删就成了给别人抹
// 记忆的口子，所以按群管的规矩限人：本人、机器人主人和管理员，本群约定谁都能撤（和
// 门控一致），其余只有本群群主、群管理员能删本群记下的记忆，身份实时向平台查。
func (t *dianaMemoryTool) forget(ctx context.Context, store StructuredMemoryStore, id, reason string) (string, error) {
	if id == "" || len(id) > 128 {
		return "", fmt.Errorf("无效的记忆 ID")
	}
	if reason == "" || len([]rune(reason)) > 200 {
		return "", fmt.Errorf("reason 必须为 1 到 200 字符：写清哪里记错了、怎么核实的")
	}
	forgetter, ok := store.(StructuredMemoryForgetStore)
	if !ok {
		return "", fmt.Errorf("当前记忆存储不支持删除")
	}
	r := t.runtime
	query := r.memoryQueryForEvent(ctx, t.event, "", 0)
	query.IDs = []string{id}
	query.MaxCandidates = 1
	loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	items, err := store.ListStructuredMemories(loadCtx, query)
	if err != nil {
		return "", err
	}
	if len(items) == 0 || items[0].ID != id {
		return "", fmt.Errorf("没找到这条记忆：可能已失效，或不在当前会话能访问的范围")
	}
	if err := t.authorizeForget(ctx, items[0], query.Session); err != nil {
		return "", err
	}
	forgotten, err := forgetter.ForgetStructuredMemory(ctx, id)
	if err != nil {
		return "", err
	}
	if !forgotten {
		return "", fmt.Errorf("这条记忆已经不是生效状态")
	}
	body, _ := json.Marshal(map[string]any{"ok": true, "id": id, "content": items[0].Content, "note": "已删除，之后不会再注入；同一件事以后有人重新说起，仍可能被重新记下。"})
	return string(body), nil
}

func (t *dianaMemoryTool) authorizeForget(ctx context.Context, item StructuredMemoryItem, session string) error {
	speaker := strings.TrimSpace(t.event.UserID)
	if subject := strings.TrimSpace(item.SubjectUserID); subject != "" && subject == speaker {
		return nil
	}
	sameSession := strings.TrimSpace(item.SourceSession) == strings.TrimSpace(session)
	if sameSession && IsGroupRule(item) {
		return nil
	}
	if policy := t.runtime.relationshipPolicy(ctx, t.event); policy.Owner || policy.Admin {
		return nil
	}
	denied := fmt.Errorf("这条记的是别人的事，只有本人、机器人主人，或本群群主、群管理员（限本群记下的记忆）能删；当前发言者不能删，如实告诉对方这条可能记错了即可")
	if !sameSession || t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		return denied
	}
	role, err := t.runtime.liveGroupRole(ctx, t.event, t.event.GroupID, speaker)
	if err != nil {
		return fmt.Errorf("无法确认你在本群的身份，暂不删除：%w", err)
	}
	if !GroupRoleCanConfigure(role) {
		return denied
	}
	return nil
}
