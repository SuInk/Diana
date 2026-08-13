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
	ID                string              `json:"id"`
	At                time.Time           `json:"at"`
	Kind              string              `json:"kind"`
	Platform          string              `json:"platform,omitempty"`
	ProfileID         string              `json:"profile_id,omitempty"`
	GroupID           string              `json:"group_id,omitempty"`
	UserID            string              `json:"user_id,omitempty"`
	SenderName        string              `json:"sender_name,omitempty"`
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
	Images            []InboundEventImage `json:"images,omitempty"`
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
	Events        []InboundEventDetail
	Total         int64
	FilteredTotal int64
	Replied       int64
	NotReplied    int64
	Pending       int64
	Errors        int64
	LLMCalls      int64
	InputTokens   int64
	OutputTokens  int64
	TotalTokens   int64
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
)

const (
	inboundEventRepliedCondition    = `(i.status = 'done' AND ((COALESCE(i.outcome, '') = 'error_replied' AND COALESCE(i.delivery_stage, '') IN ('acknowledged', 'echo_persisted')) OR (COALESCE(i.outcome, '') != 'error_replied' AND (COALESCE(i.decision, '') = 'replied' OR (COALESCE(i.decision, '') = '' AND (COALESCE(i.outcome, '') = 'replied' OR COALESCE(i.outcome, '') LIKE 'replied_%'))))))`
	inboundEventErrorCondition      = `(i.status = 'done' AND NOT ` + inboundEventRepliedCondition + ` AND (COALESCE(i.decision, '') = 'error' OR COALESCE(i.outcome, '') IN ('error_send_unconfirmed', 'processing_error', 'dropped_outbound_delivery') OR (COALESCE(i.outcome, '') = 'error_replied' AND COALESCE(i.delivery_stage, '') NOT IN ('acknowledged', 'echo_persisted')) OR (COALESCE(i.decision, '') = '' AND (NULLIF(TRIM(i.processing_error), '') IS NOT NULL OR NULLIF(TRIM(i.last_error), '') IS NOT NULL))))`
	inboundEventNotRepliedCondition = `(i.status = 'done' AND NOT ` + inboundEventRepliedCondition + ` AND NOT ` + inboundEventErrorCondition + `)`
)

// ParseInboundEventResultFilter accepts only the stable result categories
// exposed by the event API.
func ParseInboundEventResultFilter(raw string) (InboundEventResultFilter, bool) {
	filter := InboundEventResultFilter(strings.TrimSpace(raw))
	if filter == "" {
		filter = InboundEventResultAll
	}
	switch filter {
	case InboundEventResultAll, InboundEventResultReplied, InboundEventResultNotReplied, InboundEventResultPending, InboundEventResultError:
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
	default:
		return "", false
	}
}

// ListInboundEventDetails returns one page of durable inbound decisions and
// aggregate counts for the entire selected time range.
func (s *SQLiteStore) ListInboundEventDetails(ctx context.Context, since time.Time, limit, offset int, resultFilters ...InboundEventResultFilter) (InboundEventDetailPage, error) {
	page := InboundEventDetailPage{Events: []InboundEventDetail{}}
	if s == nil || s.db == nil {
		return page, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	resultFilter := InboundEventResultAll
	if len(resultFilters) > 0 {
		resultFilter = resultFilters[0]
	}
	resultCondition, ok := inboundEventResultCondition(resultFilter)
	if !ok {
		return InboundEventDetailPage{}, fmt.Errorf("unsupported inbound event result filter %q", resultFilter)
	}
	sinceUnix := int64(0)
	if !since.IsZero() {
		sinceUnix = since.Unix()
	}

	if err := s.db.QueryRowContext(ctx, `
SELECT
  COUNT(*),
	COALESCE(SUM(CASE WHEN `+inboundEventRepliedCondition+` THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN `+inboundEventNotRepliedCondition+` THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN i.status != 'done' THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN `+inboundEventErrorCondition+` THEN 1 ELSE 0 END), 0)
FROM inbound_events AS i
WHERE i.event_time >= ?
`, sinceUnix).Scan(&page.Total, &page.Replied, &page.NotReplied, &page.Pending, &page.Errors); err != nil {
		return InboundEventDetailPage{}, fmt.Errorf("count inbound event details: %w", err)
	}
	page.FilteredTotal = page.Total
	if resultFilter != InboundEventResultAll {
		if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM inbound_events AS i
WHERE i.event_time >= ? AND (`+resultCondition+`)
`, sinceUnix).Scan(&page.FilteredTotal); err != nil {
			return InboundEventDetailPage{}, fmt.Errorf("count filtered inbound event details: %w", err)
		}
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT
  i.id, i.event_time, i.kind, COALESCE(i.group_id, ''), COALESCE(i.user_id, ''),
  COALESCE(m.sender_name, ''), COALESCE(i.message_id, ''), COALESCE(m.text, ''), COALESCE(m.payload, ''),
  i.status, COALESCE(i.outcome, ''), COALESCE(i.decision, ''), COALESCE(i.decision_reason, ''),
  COALESCE(i.reply_text, ''), COALESCE(NULLIF(TRIM(i.processing_error), ''), i.last_error, ''),
  COALESCE(i.duration_ms, 0), COALESCE(i.delivery_stage, ''), COALESCE(i.outbound_message_id, ''),
  COALESCE(i.reply_generated_at, 0), COALESCE(i.send_attempted_at, 0), COALESCE(i.send_acked_at, 0),
  COALESCE(i.self_echo_at, 0), COALESCE(i.delivery_error, ''),
  COALESCE(i.created_at, 0), COALESCE(i.completed_at, 0)
FROM inbound_events AS i
LEFT JOIN message_events AS m ON m.id = i.id
WHERE i.event_time >= ? AND (`+resultCondition+`)
ORDER BY i.event_time DESC, i.created_at DESC, i.id DESC
LIMIT ? OFFSET ?
`, sinceUnix, limit, offset)
	if err != nil {
		return InboundEventDetailPage{}, fmt.Errorf("list inbound event details: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var item InboundEventDetail
		var payload string
		var eventTime, replyGeneratedAt, sendAttemptedAt, sendAckedAt, selfEchoAt, createdAt, completedAt int64
		if err := rows.Scan(
			&item.ID, &eventTime, &item.Kind, &item.GroupID, &item.UserID,
			&item.SenderName, &item.MessageID, &item.Text, &payload, &item.Status,
			&item.Outcome, &item.Decision, &item.Reason, &item.Reply, &item.Error,
			&item.DurationMS, &item.DeliveryStage, &item.OutboundMessageID,
			&replyGeneratedAt, &sendAttemptedAt, &sendAckedAt, &selfEchoAt, &item.DeliveryError,
			&createdAt, &completedAt,
		); err != nil {
			return InboundEventDetailPage{}, fmt.Errorf("scan inbound event detail: %w", err)
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
			segments := inboundEventDisplaySegments(source, item.Text)
			item.Images = inboundEventImages(segments)
			if displayText := strings.TrimSpace(assistant.PlainText(segments)); displayText != "" || len(item.Images) > 0 {
				// A CQ-only image message has no textual body. Clearing the raw CQ
				// code lets the WebUI render the structured image instead.
				item.Text = displayText
			}
		}
		if item.DurationMS <= 0 && completedAt > createdAt && createdAt > 0 {
			item.DurationMS = (completedAt - createdAt) / int64(time.Millisecond)
		}
		page.Events = append(page.Events, item)
	}
	if err := rows.Err(); err != nil {
		return InboundEventDetailPage{}, fmt.Errorf("iterate inbound event details: %w", err)
	}
	usageByMessage, usage, err := s.inboundEventTokenUsage(ctx, since)
	if err != nil {
		return InboundEventDetailPage{}, err
	}
	page.LLMCalls = usage.LLMCalls
	page.InputTokens = usage.InputTokens
	page.OutputTokens = usage.OutputTokens
	page.TotalTokens = usage.TotalTokens
	for index := range page.Events {
		if eventUsage, found := usageByMessage[strings.TrimSpace(page.Events[index].MessageID)]; found {
			page.Events[index].LLMCalls = eventUsage.LLMCalls
			page.Events[index].InputTokens = eventUsage.InputTokens
			page.Events[index].OutputTokens = eventUsage.OutputTokens
			page.Events[index].TotalTokens = eventUsage.TotalTokens
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
	LLMCalls     int64
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

func (s *SQLiteStore) inboundEventTokenUsage(ctx context.Context, since time.Time) (map[string]inboundEventTokenTotals, inboundEventTokenTotals, error) {
	sinceText := time.Unix(0, 0).UTC().Format(time.RFC3339Nano)
	if !since.IsZero() {
		sinceText = since.UTC().Format(time.RFC3339Nano)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT target, metadata
FROM app_logs
WHERE created_at >= ? AND action IN ('qqbot.llm_usage', 'assistant.llm_usage')
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
		inputTokens := int64FromAny(meta["input_tokens"])
		outputTokens := int64FromAny(meta["output_tokens"])
		totalTokens := int64FromAny(meta["total_tokens"])
		if totalTokens <= 0 && (inputTokens > 0 || outputTokens > 0) {
			totalTokens = inputTokens + outputTokens
		}
		current := inboundEventTokenTotals{
			LLMCalls:     1,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			TotalTokens:  totalTokens,
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
}
