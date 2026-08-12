package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// InboundEventDetail is the durable message-processing audit row exposed to
// the WebUI.
type InboundEventDetail struct {
	ID           string    `json:"id"`
	At           time.Time `json:"at"`
	Kind         string    `json:"kind"`
	Platform     string    `json:"platform,omitempty"`
	ProfileID    string    `json:"profile_id,omitempty"`
	GroupID      string    `json:"group_id,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	SenderName   string    `json:"sender_name,omitempty"`
	MessageID    string    `json:"message_id,omitempty"`
	Text         string    `json:"text,omitempty"`
	Status       string    `json:"status"`
	Outcome      string    `json:"outcome,omitempty"`
	Decision     string    `json:"decision,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	Reply        string    `json:"reply,omitempty"`
	Error        string    `json:"error,omitempty"`
	DurationMS   int64     `json:"duration_ms,omitempty"`
	LLMCalls     int64     `json:"llm_calls,omitempty"`
	InputTokens  int64     `json:"input_tokens,omitempty"`
	OutputTokens int64     `json:"output_tokens,omitempty"`
	TotalTokens  int64     `json:"total_tokens,omitempty"`
}

type InboundEventDetailPage struct {
	Events       []InboundEventDetail
	Total        int64
	Replied      int64
	NotReplied   int64
	Pending      int64
	Errors       int64
	LLMCalls     int64
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

// ListInboundEventDetails returns one page of durable inbound decisions and
// aggregate counts for the entire selected time range.
func (s *SQLiteStore) ListInboundEventDetails(ctx context.Context, since time.Time, limit, offset int) (InboundEventDetailPage, error) {
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
	sinceUnix := int64(0)
	if !since.IsZero() {
		sinceUnix = since.Unix()
	}

	if err := s.db.QueryRowContext(ctx, `
SELECT
  COUNT(*),
  COALESCE(SUM(CASE WHEN COALESCE(outcome, '') = 'replied' OR COALESCE(outcome, '') = 'error_replied' OR COALESCE(outcome, '') LIKE 'replied_%' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN status = 'done' AND NOT (COALESCE(outcome, '') = 'replied' OR COALESCE(outcome, '') = 'error_replied' OR COALESCE(outcome, '') LIKE 'replied_%') THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN status != 'done' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN NULLIF(TRIM(processing_error), '') IS NOT NULL OR NULLIF(TRIM(last_error), '') IS NOT NULL OR outcome IN ('error_replied', 'dropped_outbound_delivery') THEN 1 ELSE 0 END), 0)
FROM inbound_events
WHERE event_time >= ?
`, sinceUnix).Scan(&page.Total, &page.Replied, &page.NotReplied, &page.Pending, &page.Errors); err != nil {
		return InboundEventDetailPage{}, fmt.Errorf("count inbound event details: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT
  i.id, i.event_time, i.kind, COALESCE(i.group_id, ''), COALESCE(i.user_id, ''),
  COALESCE(m.sender_name, ''), COALESCE(i.message_id, ''), COALESCE(m.text, ''), COALESCE(m.payload, ''),
  i.status, COALESCE(i.outcome, ''), COALESCE(i.decision, ''), COALESCE(i.decision_reason, ''),
  COALESCE(i.reply_text, ''), COALESCE(NULLIF(TRIM(i.processing_error), ''), i.last_error, ''),
  COALESCE(i.duration_ms, 0), COALESCE(i.created_at, 0), COALESCE(i.completed_at, 0)
FROM inbound_events AS i
LEFT JOIN message_events AS m ON m.id = i.id
WHERE i.event_time >= ?
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
		var eventTime, createdAt, completedAt int64
		if err := rows.Scan(
			&item.ID, &eventTime, &item.Kind, &item.GroupID, &item.UserID,
			&item.SenderName, &item.MessageID, &item.Text, &payload, &item.Status,
			&item.Outcome, &item.Decision, &item.Reason, &item.Reply, &item.Error,
			&item.DurationMS, &createdAt, &completedAt,
		); err != nil {
			return InboundEventDetailPage{}, fmt.Errorf("scan inbound event detail: %w", err)
		}
		item.At = time.Unix(eventTime, 0)
		item.Status = strings.TrimSpace(item.Status)
		item.Outcome = strings.TrimSpace(item.Outcome)
		item.Decision = strings.TrimSpace(item.Decision)
		item.Reason = strings.TrimSpace(item.Reason)
		item.Error = strings.TrimSpace(item.Error)
		if strings.TrimSpace(payload) != "" {
			var source struct {
				Platform  string `json:"platform"`
				ProfileID string `json:"profile_id"`
			}
			if json.Unmarshal([]byte(payload), &source) == nil {
				item.Platform = strings.TrimSpace(source.Platform)
				item.ProfileID = strings.TrimSpace(source.ProfileID)
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
