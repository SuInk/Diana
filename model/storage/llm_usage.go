package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

func (s *SQLiteStore) LLMUsageSince(ctx context.Context, since, until time.Time) (applog.UsageSummary, error) {
	report, err := s.LLMUsageReport(ctx, applog.UsageFilter{}, since, until)
	return report.Usage, err
}

// LLMUsageReport reads the timestamp-indexed window once and groups by robot,
// platform and group. Exact [since,until) boundaries are enforced after parsing.
func (s *SQLiteStore) LLMUsageReport(ctx context.Context, filter applog.UsageFilter, since, until time.Time) (applog.UsageReport, error) {
	defer s.observeStorage(ctx, "LLMUsageReport", "read")()
	report := applog.UsageReport{Usage: applog.UsageSummary{Since: since, Until: until}, Groups: []applog.GroupTokenUsage{}}
	if s == nil || s.db == nil {
		return report, fmt.Errorf("usage storage unavailable")
	}
	if !since.Before(until) {
		return report, fmt.Errorf("invalid usage window")
	}
	const seconds = "2006-01-02T15:04:05"
	// 筛选条件先在 SQL 里粗筛，省得把整个窗口的元数据都搬进 Go 解析；
	// 循环里仍按原规则精确比对。json_valid 守住空串和坏 JSON：它们本就匹配不上非空条件。
	query := `SELECT metadata,created_at FROM app_logs WHERE created_at>=? AND created_at<? AND action='llm_usage'`
	args := []any{since.UTC().Format(seconds), until.UTC().Add(time.Second).Format(seconds)}
	for _, condition := range []struct{ key, value string }{{"profile_id", filter.ProfileID}, {"platform", filter.Platform}, {"group_id", filter.GroupID}} {
		if condition.value == "" {
			continue
		}
		query += ` AND json_valid(metadata) AND trim(json_extract(metadata,'$.` + condition.key + `'))=?`
		args = append(args, condition.value)
	}
	rows, err := s.eventReader().QueryContext(ctx, query, args...)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	type groupAccumulator struct {
		entry    applog.GroupTokenUsage
		usage    applog.UsageBreakdownAccumulator
		purposes map[string]*applog.UsageBreakdownAccumulator
	}
	var breakdown applog.UsageBreakdownAccumulator
	groups := map[string]*groupAccumulator{}
	for rows.Next() {
		var metadata sql.NullString
		var timestamp string
		if err := rows.Scan(&metadata, &timestamp); err != nil {
			return report, err
		}
		at, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return report, fmt.Errorf("invalid usage timestamp: %w", err)
		}
		if at.Before(since) || !at.Before(until) {
			continue
		}
		var meta map[string]any
		// Empty historical metadata still represents a call with unknown usage.
		// Malformed nonempty JSON remains a read error.
		if metadata.Valid && strings.TrimSpace(metadata.String) != "" {
			if err := json.Unmarshal([]byte(metadata.String), &meta); err != nil {
				return report, fmt.Errorf("invalid usage metadata: %w", err)
			}
		}
		str := func(key string) string { v, _ := meta[key].(string); return strings.TrimSpace(v) }
		profile, platform, group := str("profile_id"), str("platform"), str("group_id")
		if filter.ProfileID != "" && filter.ProfileID != profile || filter.Platform != "" && filter.Platform != platform || filter.GroupID != "" && filter.GroupID != group {
			continue
		}
		call := usageCall(meta)
		addUsageCall(&report.Usage, call)
		breakdown.Add(call)
		if group == "" {
			continue
		}
		key := profile + "\x00" + platform + "\x00" + group
		accumulator := groups[key]
		if accumulator == nil {
			accumulator = &groupAccumulator{
				entry:    applog.GroupTokenUsage{ProfileID: profile, Platform: platform, GroupID: group, Usage: applog.UsageSummary{Since: since, Until: until}, Purposes: map[string]applog.UsageSummary{}},
				purposes: map[string]*applog.UsageBreakdownAccumulator{},
			}
			groups[key] = accumulator
		}
		entry := &accumulator.entry
		addUsageCall(&entry.Usage, call)
		accumulator.usage.Add(call)
		purpose := str("purpose")
		if purpose == "" {
			purpose = "unlabeled"
		}
		usage := entry.Purposes[purpose]
		usage.Since, usage.Until = since, until
		addUsageCall(&usage, call)
		entry.Purposes[purpose] = usage
		if accumulator.purposes[purpose] == nil {
			accumulator.purposes[purpose] = &applog.UsageBreakdownAccumulator{}
		}
		accumulator.purposes[purpose].Add(call)
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	report.Usage.Breakdown = breakdown.Snapshot()
	for _, accumulator := range groups {
		entry := accumulator.entry
		entry.Usage.Breakdown = accumulator.usage.Snapshot()
		for purpose, breakdown := range accumulator.purposes {
			usage := entry.Purposes[purpose]
			usage.Breakdown = breakdown.Snapshot()
			entry.Purposes[purpose] = usage
		}
		report.Groups = append(report.Groups, entry)
	}
	sort.Slice(report.Groups, func(i, j int) bool {
		a, b := report.Groups[i], report.Groups[j]
		if a.Usage.TotalTokens != b.Usage.TotalTokens {
			return a.Usage.TotalTokens > b.Usage.TotalTokens
		}
		return a.ProfileID+"\x00"+a.Platform+"\x00"+a.GroupID < b.ProfileID+"\x00"+b.Platform+"\x00"+b.GroupID
	})
	return report, nil
}

// usageCall 把一行元数据折算成一次调用；每行只算一次，再分别累加到总计、群和用途。
func usageCall(meta map[string]any) applog.UsageBreakdown {
	input, output := int64FromAny(meta["input_tokens"]), int64FromAny(meta["output_tokens"])
	total := int64FromAny(meta["total_tokens"])
	if total <= 0 {
		total = input + output
	}
	var missingCalls int64
	if missing, _ := meta["usage_missing"].(bool); missing || input == 0 && output == 0 && total == 0 {
		missingCalls = 1
	}
	purpose, _ := meta["purpose"].(string)
	provider, _ := meta["provider"].(string)
	model, _ := meta["model"].(string)
	return applog.UsageBreakdown{
		Purpose: purpose, Provider: provider, Model: model, Calls: 1,
		InputTokens: input, OutputTokens: output, TotalTokens: total,
		CachedInputTokens: int64FromAny(meta["cached_input_tokens"]), MissingUsageCalls: missingCalls,
	}
}

func addUsageCall(usage *applog.UsageSummary, call applog.UsageBreakdown) {
	usage.Calls += call.Calls
	usage.InputTokens += call.InputTokens
	usage.OutputTokens += call.OutputTokens
	usage.TotalTokens += call.TotalTokens
	usage.CachedInputTokens += call.CachedInputTokens
	usage.MissingUsageCalls += call.MissingUsageCalls
}

// GroupLLMUsageSince 统计某个群在窗口内的模型调用次数，供按群额度判断。
//
// 只数带 group_id 的调用：私聊、后台任务和没有会话归属的调用不算进群额度。
// 上游没报用量的调用照样算一次。
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
		entry := usage[groupID]
		entry.Calls++
		usage[groupID] = entry
	}
	return usage, rows.Err()
}
