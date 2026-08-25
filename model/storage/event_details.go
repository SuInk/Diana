// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

// InboundEventDetail is the durable message-processing audit row exposed to
// the WebUI.
type InboundEventDetail struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"`
	Platform   string    `json:"platform,omitempty"`
	ProfileID  string    `json:"profile_id,omitempty"`
	GroupID    string    `json:"group_id,omitempty"`
	UserID     string    `json:"user_id,omitempty"`
	SenderName string    `json:"sender_name,omitempty"`
	SenderRole string    `json:"sender_role,omitempty"`
	// SenderLevel 是发言者的群等级。回复门槛按等级卡人，排查「为什么这条没回」
	// 时得能直接看到当时的等级，而不是回群里翻资料卡。
	SenderLevel       int                 `json:"sender_level,omitempty"`
	SenderLevelLabel  string              `json:"sender_level_label,omitempty"`
	SubType           string              `json:"sub_type,omitempty"`
	OriginalTime      *time.Time          `json:"original_time,omitempty"`
	OperatorID        string              `json:"operator_id,omitempty"`
	OperatorName      string              `json:"operator_name,omitempty"`
	OperatorRole      string              `json:"operator_role,omitempty"`
	MessageID         string              `json:"message_id,omitempty"`
	Text              string              `json:"text,omitempty"`
	Status            string              `json:"status"`
	Outcome           string              `json:"outcome,omitempty"`
	Decision          string              `json:"decision,omitempty"`
	Reason            string              `json:"reason,omitempty"`
	Reply             string              `json:"reply,omitempty"`
	Error             string              `json:"error,omitempty"`
	DurationMS        int64               `json:"duration_ms,omitempty"`
	DeliveryStage     string              `json:"delivery_stage,omitempty"`
	OutboundMessageID string              `json:"outbound_message_id,omitempty"`
	ReplyGeneratedAt  *time.Time          `json:"reply_generated_at,omitempty"`
	SendAttemptedAt   *time.Time          `json:"send_attempted_at,omitempty"`
	SendAckedAt       *time.Time          `json:"send_acked_at,omitempty"`
	SelfEchoAt        *time.Time          `json:"self_echo_at,omitempty"`
	DeliveryError     string              `json:"delivery_error,omitempty"`
	LLMCalls          int64               `json:"llm_calls,omitempty"`
	InputTokens       int64               `json:"input_tokens,omitempty"`
	OutputTokens      int64               `json:"output_tokens,omitempty"`
	TotalTokens       int64               `json:"total_tokens,omitempty"`
	CachedInputTokens int64               `json:"cached_input_tokens,omitempty"`
	Images            []InboundEventImage `json:"images,omitempty"`
	// Subtasks 是这条消息触发的后台子任务（生成图片、文档 OCR 等）。图片是任务跑完
	// 之后异步发出去的，事件详情里只有一句文字回复时看不出它从哪来。
	Subtasks []assistant.InboundEventSubtask `json:"subtasks,omitempty"`
	// Delivery 是这一轮实际发出去的内容概览。Reply 只是文本，说不出还发了转发
	// 卡片、几张图或一个视频。
	Delivery assistant.OutboundDelivery `json:"delivery,omitempty"`
}

// InboundEventImage intentionally contains display metadata only. The WebUI
// serves image bytes through an authenticated event-scoped endpoint so local
// cache paths and temporary OneBot URLs never leak into the event list.
type InboundEventImage struct {
	Index       int    `json:"index"`
	Summary     string `json:"summary,omitempty"`
	Unavailable bool   `json:"unavailable,omitempty"`
}

type InboundEventDetailPage struct {
	Events            []InboundEventDetail
	Total             int64
	FilteredTotal     int64
	Replied           int64
	NotReplied        int64
	Pending           int64
	Errors            int64
	Notices           int64
	LLMCalls          int64
	InputTokens       int64
	OutputTokens      int64
	TotalTokens       int64
	CachedInputTokens int64
}

// InboundEventResultFilter limits event detail rows without changing the
// aggregate overview for the selected time range.
type InboundEventResultFilter string

const (
	InboundEventResultAll        InboundEventResultFilter = "all"
	InboundEventResultReplied    InboundEventResultFilter = "replied"
	InboundEventResultNotReplied InboundEventResultFilter = "not_replied"
	InboundEventResultPending    InboundEventResultFilter = "pending"
	InboundEventResultError      InboundEventResultFilter = "error"
	InboundEventResultNotice     InboundEventResultFilter = "notice"
)

const (
	inboundEventRepliedCondition    = `(i.status = 'done' AND ((COALESCE(i.outcome, '') = 'error_replied' AND COALESCE(i.delivery_stage, '') IN ('acknowledged', 'echo_persisted')) OR (COALESCE(i.outcome, '') != 'error_replied' AND (COALESCE(i.decision, '') = 'replied' OR (COALESCE(i.decision, '') = '' AND (COALESCE(i.outcome, '') = 'replied' OR COALESCE(i.outcome, '') LIKE 'replied_%'))))))`
	inboundEventErrorCondition      = `(i.status = 'done' AND NOT ` + inboundEventRepliedCondition + ` AND (COALESCE(i.decision, '') = 'error' OR COALESCE(i.outcome, '') IN ('error_send_unconfirmed', 'processing_error', 'dropped_outbound_delivery') OR (COALESCE(i.outcome, '') = 'error_replied' AND COALESCE(i.delivery_stage, '') NOT IN ('acknowledged', 'echo_persisted')) OR (COALESCE(i.decision, '') = '' AND (NULLIF(TRIM(i.processing_error), '') IS NOT NULL OR NULLIF(TRIM(i.last_error), '') IS NOT NULL))))`
	inboundEventNoticeCondition     = `(i.status = 'done' AND COALESCE(i.decision, '') = 'notice')`
	inboundEventNotRepliedCondition = `(i.status = 'done' AND NOT ` + inboundEventRepliedCondition + ` AND NOT ` + inboundEventErrorCondition + ` AND NOT ` + inboundEventNoticeCondition + `)`
)

// ParseInboundEventResultFilter accepts only the stable result categories
// exposed by the event API.
func ParseInboundEventResultFilter(raw string) (InboundEventResultFilter, bool) {
	filter := InboundEventResultFilter(strings.TrimSpace(raw))
	if filter == "" {
		filter = InboundEventResultAll
	}
	switch filter {
	case InboundEventResultAll, InboundEventResultReplied, InboundEventResultNotReplied, InboundEventResultPending, InboundEventResultError, InboundEventResultNotice:
		return filter, true
	default:
		return "", false
	}
}

func inboundEventResultCondition(filter InboundEventResultFilter) (string, bool) {
	switch filter {
	case InboundEventResultAll:
		return "1 = 1", true
	case InboundEventResultReplied:
		return inboundEventRepliedCondition, true
	case InboundEventResultNotReplied:
		return inboundEventNotRepliedCondition, true
	case InboundEventResultPending:
		return `i.status != 'done'`, true
	case InboundEventResultError:
		return inboundEventErrorCondition, true
	case InboundEventResultNotice:
		return inboundEventNoticeCondition, true
	default:
		return "", false
	}
}

// ListInboundEventDetails returns one page of durable inbound decisions and
// aggregate counts for the entire selected time range.
// InboundEventQuery 是事件列表的查询条件。
//
// 原先是 since/limit/offset 加一个变参 result，再想按群、按用户筛就只能继续往
// 位置参数上堆，调用方读起来全是没有名字的值。条件写成结构体之后，加一维筛选
// 不动签名，也不动只关心其中两三项的调用方。
type InboundEventQuery struct {
	Since  time.Time
	Limit  int
	Offset int
	Result InboundEventResultFilter
	// GroupID 只看这一个群，留空表示不限。
	GroupID string
	// ProfileID 只看这一台机器人，留空表示不限。多机器人部署里，控制台的
	// 「当前机器人」切换靠它生效。
	ProfileID string
}

func (s *SQLiteStore) ListInboundEventDetails(ctx context.Context, query InboundEventQuery) (InboundEventDetailPage, error) {
	page := InboundEventDetailPage{Events: []InboundEventDetail{}}
	if s == nil || s.db == nil {
		return page, nil
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	resultFilter := query.Result
	if strings.TrimSpace(string(resultFilter)) == "" {
		resultFilter = InboundEventResultAll
	}
	resultCondition, ok := inboundEventResultCondition(resultFilter)
	if !ok {
		return InboundEventDetailPage{}, fmt.Errorf("unsupported inbound event result filter %q", resultFilter)
	}
	sinceUnix := int64(0)
	if !query.Since.IsZero() {
		sinceUnix = query.Since.Unix()
	}
	// 群筛选要同时作用在两个计数和列表上，否则顶部统计说的是全部事件、
	// 下面列的却是一个群，两个数字对不上。
	groupCondition := ""
	scopeArgs := []any{sinceUnix}
	if groupID := strings.TrimSpace(query.GroupID); groupID != "" {
		groupCondition = " AND i.group_id = ?"
		scopeArgs = append(scopeArgs, groupID)
	}
	// 机器人筛选和群筛选一样要同时作用在计数和列表上，否则顶部统计与下面的
	// 列表说的不是同一批事件。
	if profileID := strings.TrimSpace(query.ProfileID); profileID != "" {
		groupCondition += " AND COALESCE(i.profile_id, '') = ?"
		scopeArgs = append(scopeArgs, profileID)
	}

	if err := s.db.QueryRowContext(ctx, `
SELECT
  COUNT(*),
	COALESCE(SUM(CASE WHEN `+inboundEventRepliedCondition+` THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN `+inboundEventNotRepliedCondition+` THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN i.status != 'done' THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN `+inboundEventErrorCondition+` THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN `+inboundEventNoticeCondition+` THEN 1 ELSE 0 END), 0)
FROM inbound_events AS i
WHERE i.event_time >= ?`+groupCondition+`
`, scopeArgs...).Scan(&page.Total, &page.Replied, &page.NotReplied, &page.Pending, &page.Errors, &page.Notices); err != nil {
		return InboundEventDetailPage{}, fmt.Errorf("count inbound event details: %w", err)
	}
	page.FilteredTotal = page.Total
	if resultFilter != InboundEventResultAll {
		if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM inbound_events AS i
WHERE i.event_time >= ?`+groupCondition+` AND (`+resultCondition+`)
`, scopeArgs...).Scan(&page.FilteredTotal); err != nil {
			return InboundEventDetailPage{}, fmt.Errorf("count filtered inbound event details: %w", err)
		}
	}

	// 画像名用标量子查询取，不用 LEFT JOIN。
	//
	// user_profiles 的主键是 (bot_profile_id, user_id)：同一个人每台机器人各存一份。
	// 原先这里 LEFT JOIN 只按 user_id 匹配，两台机器人都见过的人，他的每条事件都会被
	// 乘上画像行数——列表里同一条事件出现两遍，一次挂 A 认识的名字一次挂 B 的。
	// 而上面两个计数根本不 join，于是界面显示成「已显示 36 / 22」，显示数比总数还大。
	//
	// 补全 join 键也能修，但那还是把「不会重复」寄托在键的假设上——这个 bug 正是
	// 那个假设漂移造成的。标量子查询在结构上就只能返回一个值，加列、改键都不会再翻倍。
	// IN (自己那台, '') 加上 ORDER BY 优先自己那台：多机器人之前写的画像 bot_profile_id
	// 是空串，还能继续兜住，不会因为这次修改丢掉老数据的昵称。
	rows, err := s.db.QueryContext(ctx, `
SELECT
  i.id, i.event_time, i.kind, COALESCE(i.group_id, ''), COALESCE(i.user_id, ''),
  COALESCE(NULLIF(TRIM(m.sender_name), ''), NULLIF(TRIM((
    SELECT u.display_name FROM user_profiles AS u
    WHERE u.user_id = i.user_id AND u.bot_profile_id IN (COALESCE(i.profile_id, ''), '')
    ORDER BY (u.bot_profile_id = COALESCE(i.profile_id, '')) DESC
    LIMIT 1
  )), ''), ''),
  COALESCE(i.message_id, ''), COALESCE(m.text, ''), COALESCE(m.payload, ''),
  i.status, COALESCE(i.outcome, ''), COALESCE(i.decision, ''), COALESCE(i.decision_reason, ''),
  COALESCE(i.reply_text, ''), COALESCE(NULLIF(TRIM(i.processing_error), ''), i.last_error, ''),
  COALESCE(i.duration_ms, 0), COALESCE(i.delivery_stage, ''), COALESCE(i.outbound_message_id, ''),
  COALESCE(i.reply_generated_at, 0), COALESCE(i.send_attempted_at, 0), COALESCE(i.send_acked_at, 0),
  COALESCE(i.self_echo_at, 0), COALESCE(i.delivery_error, ''),
  COALESCE(i.created_at, 0), COALESCE(i.completed_at, 0), COALESCE(i.delivery_json, '')
FROM inbound_events AS i
LEFT JOIN message_events AS m ON m.id = i.id
WHERE i.event_time >= ?`+groupCondition+` AND (`+resultCondition+`)
ORDER BY i.event_time DESC, i.created_at DESC, i.id DESC
LIMIT ? OFFSET ?
`, append(append([]any(nil), scopeArgs...), limit, offset)...)
	if err != nil {
		return InboundEventDetailPage{}, fmt.Errorf("list inbound event details: %w", err)
	}
	defer func() { _ = rows.Close() }()

	mentionIDs := make(map[string]struct{})
	var pending []pendingMentionText
	for rows.Next() {
		var item InboundEventDetail
		var payload string
		var eventTime, replyGeneratedAt, sendAttemptedAt, sendAckedAt, selfEchoAt, createdAt, completedAt int64
		var deliveryJSON string
		if err := rows.Scan(
			&item.ID, &eventTime, &item.Kind, &item.GroupID, &item.UserID,
			&item.SenderName, &item.MessageID, &item.Text, &payload, &item.Status,
			&item.Outcome, &item.Decision, &item.Reason, &item.Reply, &item.Error,
			&item.DurationMS, &item.DeliveryStage, &item.OutboundMessageID,
			&replyGeneratedAt, &sendAttemptedAt, &sendAckedAt, &selfEchoAt, &item.DeliveryError,
			&createdAt, &completedAt, &deliveryJSON,
		); err != nil {
			return InboundEventDetailPage{}, fmt.Errorf("scan inbound event detail: %w", err)
		}
		if strings.TrimSpace(deliveryJSON) != "" {
			// 旧记录没有这一列，解析失败也不该让整页事件查不出来。
			if err := json.Unmarshal([]byte(deliveryJSON), &item.Delivery); err != nil {
				item.Delivery = assistant.OutboundDelivery{}
			}
		}
		item.At = time.Unix(eventTime, 0)
		item.Status = strings.TrimSpace(item.Status)
		item.Outcome = strings.TrimSpace(item.Outcome)
		item.Decision = strings.TrimSpace(item.Decision)
		item.Reason = strings.TrimSpace(item.Reason)
		item.Error = strings.TrimSpace(item.Error)
		item.DeliveryStage = strings.TrimSpace(item.DeliveryStage)
		item.OutboundMessageID = strings.TrimSpace(item.OutboundMessageID)
		item.DeliveryError = strings.TrimSpace(item.DeliveryError)
		item.ReplyGeneratedAt = unixNanoTimePointer(replyGeneratedAt)
		item.SendAttemptedAt = unixNanoTimePointer(sendAttemptedAt)
		item.SendAckedAt = unixNanoTimePointer(sendAckedAt)
		item.SelfEchoAt = unixNanoTimePointer(selfEchoAt)
		if source, ok := decodeInboundEventMessage(payload, item.Text); ok {
			item.Platform = strings.TrimSpace(source.Platform)
			item.ProfileID = strings.TrimSpace(source.ProfileID)
			item.SubType = strings.TrimSpace(source.SubType)
			item.OperatorID = strings.TrimSpace(source.OperatorID)
			item.OperatorName = strings.TrimSpace(source.OperatorName)
			item.OperatorRole = strings.TrimSpace(source.OperatorRole)
			item.SenderRole = strings.TrimSpace(source.SenderRole)
			item.SenderLevel = source.SenderLevel
			item.SenderLevelLabel = strings.TrimSpace(source.SenderLevelLabel)
			if source.OriginalTime > 0 {
				originalAt := time.Unix(source.OriginalTime, 0).UTC()
				item.OriginalTime = &originalAt
			}
			segments := inboundEventDisplaySegments(source, item.Text)
			item.Images = inboundEventImages(segments)
			// 正文留到补齐 @ 昵称之后再渲染：昵称要查库，攒成一批比逐行查快得多。
			collectMentionIDs(segments, mentionIDs)
			pending = append(pending, pendingMentionText{index: len(page.Events), segments: segments})
		}
		if item.DurationMS <= 0 && completedAt > createdAt && createdAt > 0 {
			item.DurationMS = (completedAt - createdAt) / int64(time.Millisecond)
		}
		page.Events = append(page.Events, item)
	}
	if err := rows.Err(); err != nil {
		return InboundEventDetailPage{}, fmt.Errorf("iterate inbound event details: %w", err)
	}
	mentionNames, err := s.resolveMentionNames(ctx, mentionIDs)
	if err != nil {
		return InboundEventDetailPage{}, err
	}
	for _, item := range pending {
		applyMentionNames(item.segments, mentionNames)
		if displayText := strings.TrimSpace(assistant.PlainText(item.segments)); displayText != "" || len(page.Events[item.index].Images) > 0 {
			// A CQ-only image message has no textual body. Clearing the raw CQ
			// code lets the WebUI render the structured image instead.
			page.Events[item.index].Text = displayText
		}
	}
	eventIDs := make([]string, 0, len(page.Events))
	for index := range page.Events {
		eventIDs = append(eventIDs, page.Events[index].ID)
	}
	subtasks, err := s.LoadInboundEventSubtasks(ctx, eventIDs)
	if err != nil {
		return InboundEventDetailPage{}, err
	}
	for index := range page.Events {
		page.Events[index].Subtasks = subtasks[page.Events[index].ID]
	}
	usageByMessage, usage, err := s.inboundEventTokenUsage(ctx, query.Since, query.GroupID)
	if err != nil {
		return InboundEventDetailPage{}, err
	}
	page.LLMCalls = usage.LLMCalls
	page.InputTokens = usage.InputTokens
	page.OutputTokens = usage.OutputTokens
	page.TotalTokens = usage.TotalTokens
	page.CachedInputTokens = usage.CachedInputTokens
	for index := range page.Events {
		if eventUsage, found := usageByMessage[strings.TrimSpace(page.Events[index].MessageID)]; found {
			page.Events[index].LLMCalls = eventUsage.LLMCalls
			page.Events[index].InputTokens = eventUsage.InputTokens
			page.Events[index].OutputTokens = eventUsage.OutputTokens
			page.Events[index].TotalTokens = eventUsage.TotalTokens
			page.Events[index].CachedInputTokens = eventUsage.CachedInputTokens
		}
	}
	return page, nil
}

func unixNanoTimePointer(value int64) *time.Time {
	if value <= 0 {
		return nil
	}
	at := time.Unix(0, value).UTC()
	return &at
}

// InboundEventImageSegment returns one current-message image by its one-based
// display index. It never exposes the containing event or media source path.
func (s *SQLiteStore) InboundEventImageSegment(ctx context.Context, eventID string, imageIndex int) (assistant.MessageSegment, bool, error) {
	if s == nil || s.db == nil || strings.TrimSpace(eventID) == "" || imageIndex <= 0 {
		return assistant.MessageSegment{}, false, nil
	}
	var payload, text string
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(m.payload, ''), COALESCE(m.text, '')
FROM inbound_events AS i
JOIN message_events AS m ON m.id = i.id
WHERE i.id = ?
LIMIT 1
`, strings.TrimSpace(eventID)).Scan(&payload, &text)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return assistant.MessageSegment{}, false, nil
		}
		return assistant.MessageSegment{}, false, fmt.Errorf("load inbound event image: %w", err)
	}
	source, ok := decodeInboundEventMessage(payload, text)
	if !ok {
		return assistant.MessageSegment{}, false, nil
	}
	current := 0
	for _, segment := range inboundEventDisplaySegments(source, text) {
		if !inboundEventStillImage(segment) {
			continue
		}
		current++
		if current == imageIndex {
			return segment, true, nil
		}
	}
	return assistant.MessageSegment{}, false, nil
}

func decodeInboundEventMessage(payload, fallbackText string) (assistant.MessageEvent, bool) {
	var event assistant.MessageEvent
	if strings.TrimSpace(payload) != "" && json.Unmarshal([]byte(payload), &event) == nil {
		return event, true
	}
	fallbackText = strings.TrimSpace(fallbackText)
	if fallbackText == "" {
		return assistant.MessageEvent{}, false
	}
	return assistant.MessageEvent{RawMessage: fallbackText}, true
}

func inboundEventDisplaySegments(event assistant.MessageEvent, fallbackText string) []assistant.MessageSegment {
	if len(event.Segments) > 0 {
		return event.Segments
	}
	raw := strings.TrimSpace(event.RawMessage)
	if raw == "" {
		raw = strings.TrimSpace(fallbackText)
	}
	if raw == "" {
		return nil
	}
	return assistant.CQToSegments(raw)
}

// pendingMentionText 记住一条事件的 segment，等昵称查回来后再渲染正文。
type pendingMentionText struct {
	index    int
	segments []assistant.MessageSegment
}

// collectMentionIDs 收出 at 段里还没有昵称的账号。已经带昵称的（部分 OneBot 实现
// 会附上）不必再查库。
func collectMentionIDs(segments []assistant.MessageSegment, into map[string]struct{}) {
	for _, segment := range segments {
		if segment.Type != "at" {
			continue
		}
		qq := strings.TrimSpace(segment.Data["qq"])
		if qq == "" || qq == "all" || assistant.AtMentionName(segment) != "" {
			continue
		}
		into[qq] = struct{}{}
	}
}

// applyMentionNames 把查到的昵称写回 at 段，PlainText 随后就能渲染成「@昵称（账号）」。
func applyMentionNames(segments []assistant.MessageSegment, names map[string]string) {
	for _, segment := range segments {
		if segment.Type != "at" || segment.Data == nil {
			continue
		}
		qq := strings.TrimSpace(segment.Data["qq"])
		if qq == "" || qq == "all" || assistant.AtMentionName(segment) != "" {
			continue
		}
		if name := strings.TrimSpace(names[qq]); name != "" {
			segment.Data["name"] = name
		}
	}
}

// resolveMentionNames 批量解析被提及者的昵称。优先用最近一次发言时的群名片——那是
// 群里其他人当时看到的称呼；没有发过言就退回全局资料里的显示名。
func (s *SQLiteStore) resolveMentionNames(ctx context.Context, ids map[string]struct{}) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	list := make([]any, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(list)), ",")
	names := make(map[string]string, len(list))

	profiles, err := s.db.QueryContext(ctx, `
SELECT user_id, COALESCE(TRIM(display_name), '')
FROM user_profiles
WHERE user_id IN (`+placeholders+`) AND TRIM(COALESCE(display_name, '')) <> ''
`, list...)
	if err != nil {
		return nil, fmt.Errorf("resolve mention display names: %w", err)
	}
	if err := scanMentionNames(profiles, names); err != nil {
		return nil, err
	}

	cards, err := s.db.QueryContext(ctx, `
SELECT user_id, sender_name
FROM message_events
WHERE user_id IN (`+placeholders+`) AND TRIM(COALESCE(sender_name, '')) <> ''
GROUP BY user_id
HAVING event_time = MAX(event_time)
`, list...)
	if err != nil {
		return nil, fmt.Errorf("resolve mention sender names: %w", err)
	}
	if err := scanMentionNames(cards, names); err != nil {
		return nil, err
	}
	return names, nil
}

func scanMentionNames(rows *sql.Rows, into map[string]string) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var userID, name string
		if err := rows.Scan(&userID, &name); err != nil {
			return fmt.Errorf("scan mention name: %w", err)
		}
		if userID = strings.TrimSpace(userID); userID == "" {
			continue
		}
		if name = strings.TrimSpace(name); name != "" {
			into[userID] = name
		}
	}
	return rows.Err()
}

func inboundEventImages(segments []assistant.MessageSegment) []InboundEventImage {
	images := make([]InboundEventImage, 0)
	for _, segment := range segments {
		if !inboundEventStillImage(segment) {
			continue
		}
		summary := strings.TrimSpace(segment.Data["summary"])
		if summary == "" {
			summary = strings.TrimSpace(segment.Data["name"])
		}
		images = append(images, InboundEventImage{
			Index:       len(images) + 1,
			Summary:     summary,
			Unavailable: strings.EqualFold(strings.TrimSpace(segment.Data["image_unavailable"]), "true"),
		})
	}
	return images
}

func inboundEventStillImage(segment assistant.MessageSegment) bool {
	return segment.Type == "image" && !strings.EqualFold(strings.TrimSpace(segment.Data["source_type"]), "video_frame")
}

type inboundEventTokenTotals struct {
	LLMCalls          int64
	InputTokens       int64
	OutputTokens      int64
	TotalTokens       int64
	CachedInputTokens int64
}

// groupID 非空时只统计这个群的用量：顶部的 token 统计必须和下面列出的事件同范围，
// 否则筛了一个群、token 数还是全站的，那个数就没法用来判断这个群贵不贵。
// 用量日志的 metadata 里带 group_id，直接按它过滤，不用回表连 inbound_events。
func (s *SQLiteStore) inboundEventTokenUsage(ctx context.Context, since time.Time, groupID string) (map[string]inboundEventTokenTotals, inboundEventTokenTotals, error) {
	groupID = strings.TrimSpace(groupID)
	sinceText := time.Unix(0, 0).UTC().Format(time.RFC3339Nano)
	if !since.IsZero() {
		sinceText = since.UTC().Format(time.RFC3339Nano)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT target, metadata
FROM app_logs
WHERE created_at >= ? AND action IN ('chatbot.llm_usage', 'assistant.llm_usage')
`, sinceText)
	if err != nil {
		return nil, inboundEventTokenTotals{}, fmt.Errorf("query event token usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byMessage := map[string]inboundEventTokenTotals{}
	var total inboundEventTokenTotals
	for rows.Next() {
		var target, metadata sql.NullString
		if err := rows.Scan(&target, &metadata); err != nil {
			return nil, inboundEventTokenTotals{}, fmt.Errorf("scan event token usage: %w", err)
		}
		meta := map[string]any{}
		if metadata.Valid && strings.TrimSpace(metadata.String) != "" {
			_ = json.Unmarshal([]byte(metadata.String), &meta)
		}
		if groupID != "" && metadataGroupID(meta) != groupID {
			continue
		}
		inputTokens := int64FromAny(meta["input_tokens"])
		outputTokens := int64FromAny(meta["output_tokens"])
		totalTokens := int64FromAny(meta["total_tokens"])
		if totalTokens <= 0 && (inputTokens > 0 || outputTokens > 0) {
			totalTokens = inputTokens + outputTokens
		}
		current := inboundEventTokenTotals{
			LLMCalls:          1,
			InputTokens:       inputTokens,
			OutputTokens:      outputTokens,
			TotalTokens:       totalTokens,
			CachedInputTokens: int64FromAny(meta["cached_input_tokens"]),
		}
		total.add(current)
		messageID := strings.TrimSpace(target.String)
		if messageID == "" {
			messageID, _ = meta["message_id"].(string)
			messageID = strings.TrimSpace(messageID)
		}
		if messageID != "" {
			item := byMessage[messageID]
			item.add(current)
			byMessage[messageID] = item
		}
	}
	if err := rows.Err(); err != nil {
		return nil, inboundEventTokenTotals{}, fmt.Errorf("iterate event token usage: %w", err)
	}
	return byMessage, total, nil
}

func (t *inboundEventTokenTotals) add(other inboundEventTokenTotals) {
	if t == nil {
		return
	}
	t.LLMCalls += other.LLMCalls
	t.InputTokens += other.InputTokens
	t.OutputTokens += other.OutputTokens
	t.TotalTokens += other.TotalTokens
	t.CachedInputTokens += other.CachedInputTokens
}

// metadataGroupID 取用量日志里记的群号。私聊那条是空的，按群筛选时自然不匹配。
func metadataGroupID(meta map[string]any) string {
	value, _ := meta["group_id"].(string)
	return strings.TrimSpace(value)
}

// InboundEventGroup 是某个范围内出现过事件的一个群，用于给筛选器列选项。
type InboundEventGroup struct {
	GroupID string `json:"group_id"`
	Events  int64  `json:"events"`
}

// ListInboundEventGroups 列出这个时间范围里有事件的群，按事件数从多到少。
// 筛选器只列真正有数据的群：机器人可能进了几十个群，绝大多数一条事件都没有，
// 全列出来反而找不到要看的那个。
// ListInboundEventGroups 列出这段时间里出现过的群。botProfileID 非空时只看那台
// 机器人见过的群——Telegram 的 Bot API 没有「列出我加入的群」这种接口，控制台
// 只能靠本地事件推断它在哪些群里。
func (s *SQLiteStore) ListInboundEventGroups(ctx context.Context, since time.Time, botProfileID string) ([]InboundEventGroup, error) {
	groups := []InboundEventGroup{}
	if s == nil || s.db == nil {
		return groups, nil
	}
	sinceUnix := int64(0)
	if !since.IsZero() {
		sinceUnix = since.Unix()
	}
	scopeCondition := ""
	args := []any{sinceUnix}
	if botProfileID = strings.TrimSpace(botProfileID); botProfileID != "" {
		scopeCondition = " AND COALESCE(i.profile_id, '') = ?"
		args = append(args, botProfileID)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT i.group_id, COUNT(*)
FROM inbound_events AS i
WHERE i.event_time >= ? AND COALESCE(TRIM(i.group_id), '') != ''`+scopeCondition+`
GROUP BY i.group_id
ORDER BY COUNT(*) DESC, i.group_id ASC
`, args...)
	if err != nil {
		return nil, fmt.Errorf("list inbound event groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var group InboundEventGroup
		if err := rows.Scan(&group.GroupID, &group.Events); err != nil {
			return nil, fmt.Errorf("scan inbound event group: %w", err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbound event groups: %w", err)
	}
	return groups, nil
}
