package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/SuInk/diana/model/applog"
)

func (s *SQLiteStore) LLMUsageSince(ctx context.Context, since, until time.Time) (applog.UsageSummary, error) {
	defer s.observeStorage(ctx, "LLMUsageSince", "read")()
	stats := applog.UsageSummary{Since: since, Until: until}
	if s == nil || s.db == nil {
		return stats, fmt.Errorf("usage storage unavailable")
	}
	if !since.Before(until) {
		return stats, fmt.Errorf("invalid usage window")
	}
	// RFC3339Nano has variable fractional precision. Select enclosing seconds
	// with the timestamp index, then enforce exact [since, until) in Go.
	const seconds = "2006-01-02T15:04:05"
	rows, err := s.eventReader().QueryContext(ctx, `SELECT metadata, created_at FROM app_logs
WHERE created_at >= ? AND created_at < ?
AND action IN ('diana.llm_usage', 'assistant.llm_usage', 'chatbot.llm_usage')`,
		since.UTC().Format(seconds), until.UTC().Add(time.Second).Format(seconds))
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	for rows.Next() {
		var metadata sql.NullString
		var timestamp string
		if err := rows.Scan(&metadata, &timestamp); err != nil {
			return stats, err
		}
		at, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return stats, fmt.Errorf("invalid usage timestamp: %w", err)
		}
		if at.Before(since) || !at.Before(until) {
			continue
		}
		var meta map[string]any
		if err := json.Unmarshal([]byte(metadata.String), &meta); err != nil {
			return stats, fmt.Errorf("invalid usage metadata: %w", err)
		}
		input, output := int64FromAny(meta["input_tokens"]), int64FromAny(meta["output_tokens"])
		total := int64FromAny(meta["total_tokens"])
		if total <= 0 {
			total = input + output
		}
		stats.Calls++
		stats.InputTokens += input
		stats.OutputTokens += output
		stats.TotalTokens += total
		stats.CachedInputTokens += int64FromAny(meta["cached_input_tokens"])
	}
	return stats, rows.Err()
}
