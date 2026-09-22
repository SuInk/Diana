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

// GroupLLMUsageSince 统计某个群在窗口内的用量（token 和调用次数），供按群额度判断。
//
// 只数带 group_id 的调用：私聊、后台任务和没有会话归属的调用不算进群额度。
// 口径和 LLMUsageSince 一致——total_tokens 缺失时按 input+output 兜底，缓存命中
// 已经含在 input 里，不重复相加。
func (s *SQLiteStore) GroupLLMUsageSince(ctx context.Context, profileID, groupID string, since, until time.Time) (applog.GroupUsage, error) {
	defer s.observeStorage(ctx, "GroupLLMUsageSince", "read")()
	var usage applog.GroupUsage
	if s == nil || s.db == nil {
		return usage, fmt.Errorf("usage storage unavailable")
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return usage, nil
	}
	if !since.Before(until) {
		return usage, fmt.Errorf("invalid usage window")
	}
	const seconds = "2006-01-02T15:04:05"
	rows, err := s.eventReader().QueryContext(ctx, `SELECT metadata FROM app_logs
WHERE created_at >= ? AND created_at < ?
AND action = 'llm_usage'
AND json_extract(metadata, '$.group_id') = ?`,
		since.UTC().Format(seconds), until.UTC().Add(time.Second).Format(seconds), groupID)
	if err != nil {
		return usage, err
	}
	defer rows.Close()
	profileID = strings.TrimSpace(profileID)
	for rows.Next() {
		var metadata sql.NullString
		if err := rows.Scan(&metadata); err != nil {
			return usage, err
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
		usage.Tokens += amount
		usage.Calls++
	}
	return usage, rows.Err()
}

// GroupLLMUsageSinceByProfile 一次算出这台机器人名下每个群的窗口用量。
//
// 口径和 GroupLLMUsageSince 完全一致，只是把「查一个群」换成「扫一遍按群归并」：
// 控制台群列表动辄几十个群，逐群查等于把同一段日志扫几十遍。
func (s *SQLiteStore) GroupLLMUsageSinceByProfile(ctx context.Context, profileID string, since, until time.Time) (map[string]applog.GroupUsage, error) {
	defer s.observeStorage(ctx, "GroupLLMUsageSinceByProfile", "read")()
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("usage storage unavailable")
	}
	if !since.Before(until) {
		return nil, fmt.Errorf("invalid usage window")
	}
	const seconds = "2006-01-02T15:04:05"
	rows, err := s.eventReader().QueryContext(ctx, `SELECT metadata FROM app_logs
WHERE created_at >= ? AND created_at < ?
AND action = 'llm_usage'
AND json_extract(metadata, '$.group_id') IS NOT NULL`,
		since.UTC().Format(seconds), until.UTC().Add(time.Second).Format(seconds))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profileID = strings.TrimSpace(profileID)
	usage := map[string]applog.GroupUsage{}
	for rows.Next() {
		var metadata sql.NullString
		if err := rows.Scan(&metadata); err != nil {
			return nil, err
		}
		var meta map[string]any
		if err := json.Unmarshal([]byte(metadata.String), &meta); err != nil {
			continue
		}
		groupID := strings.TrimSpace(fmt.Sprintf("%v", meta["group_id"]))
		if groupID == "" || groupID == "<nil>" {
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
		entry := usage[groupID]
		entry.Tokens += amount
		entry.Calls++
		usage[groupID] = entry
	}
	return usage, rows.Err()
}
