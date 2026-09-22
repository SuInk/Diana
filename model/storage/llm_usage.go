package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
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
AND action = 'llm_usage'`,
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

// GroupLLMTokensSince 统计某个群在窗口内用掉的 token 总量，供按群额度判断。
//
// 只数带 group_id 的调用：私聊、后台任务和没有会话归属的调用不算进群额度。
// 口径和 LLMUsageSince 一致——total_tokens 缺失时按 input+output 兜底，缓存命中
// 已经含在 input 里，不重复相加。
func (s *SQLiteStore) GroupLLMTokensSince(ctx context.Context, profileID, groupID string, since, until time.Time) (int64, error) {
	defer s.observeStorage(ctx, "GroupLLMTokensSince", "read")()
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("usage storage unavailable")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return 0, nil
	}
	if !since.Before(until) {
		return 0, fmt.Errorf("invalid usage window")
	}
	const seconds = "2006-01-02T15:04:05"
	rows, err := s.eventReader().QueryContext(ctx, `SELECT metadata FROM app_logs
WHERE created_at >= ? AND created_at < ?
AND action = 'llm_usage'
AND json_extract(metadata, '$.group_id') = ?`,
		since.UTC().Format(seconds), until.UTC().Add(time.Second).Format(seconds), groupID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	profileID = strings.TrimSpace(profileID)
	var total int64
	for rows.Next() {
		var metadata sql.NullString
		if err := rows.Scan(&metadata); err != nil {
			return 0, err
		}
		var meta map[string]any
		if err := json.Unmarshal([]byte(metadata.String), &meta); err != nil {
			continue
		}
		// 同一个群可能同时有两台机器人，额度各算各的。
		if profileID != "" {
			if owner, ok := meta["profile_id"].(string); ok && strings.TrimSpace(owner) != "" && strings.TrimSpace(owner) != profileID {
				continue
			}
		}
		amount := int64FromAny(meta["total_tokens"])
		if amount <= 0 {
			amount = int64FromAny(meta["input_tokens"]) + int64FromAny(meta["output_tokens"])
		}
		total += amount
	}
	return total, rows.Err()
}
