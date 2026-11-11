// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ReplyRequestStats 是一个群最近几轮主回复实际发给模型的请求：事件页「上下文占比」
// 画的是按配置算出的预算分配，续接上线以后，实际请求的历史在前缀里一路累积，比预算
// 大得多，两者要并排看才不骗人。
type ReplyRequestStats struct {
	Since time.Time `json:"since"`
	// Turns 是统计到的回复轮数，Requests 是这些轮里主回复调用模型的总次数（工具步骤
	// 每步一次）。
	Turns    int `json:"turns"`
	Requests int `json:"requests"`
	// 每轮第一次请求的实际输入 token（上游报的，含缓存命中部分）。
	FirstInputMedian int64 `json:"first_input_median"`
	FirstInputMax    int64 `json:"first_input_max"`
	// 缓存占比都按 token 加权：命中的输入 token ÷ 全部输入 token。
	FirstCachedPercent float64 `json:"first_cached_percent"`
	AllCachedPercent   float64 `json:"all_cached_percent"`
	// Misses 是第一次请求缓存命中不到三成的轮数。
	Misses int `json:"misses"`
	// Continued、Rebuilt 是接着上一轮发的和整份重新组装的轮数，Unrecorded 是没有续接
	// 记录的（升级前的轮次、后台入口）。RebuildReasons 按原因计数。
	Continued      int            `json:"continued"`
	Rebuilt        int            `json:"rebuilt"`
	Unrecorded     int            `json:"unrecorded"`
	RebuildReasons map[string]int `json:"rebuild_reasons"`
	Models         map[string]int `json:"models"`
}

type replyRequestTurn struct {
	at       string
	requests int
	first    replyRequestCall
	all      replyRequestCall
	model    string
}

type replyRequestCall struct {
	input  int64
	cached int64
}

// GroupReplyRequestStats 统计这个群 since 之后最近 limit 轮主回复的实际请求。
func (s *SQLiteStore) GroupReplyRequestStats(ctx context.Context, profileID, groupID string, since time.Time, limit int) (ReplyRequestStats, error) {
	defer s.observeStorage(ctx, "GroupReplyRequestStats", "read")()
	stats := ReplyRequestStats{Since: since, RebuildReasons: map[string]int{}, Models: map[string]int{}}
	if s == nil || s.db == nil {
		return stats, fmt.Errorf("reply request stats: storage unavailable")
	}
	groupID, profileID = strings.TrimSpace(groupID), strings.TrimSpace(profileID)
	if groupID == "" {
		return stats, nil
	}
	if limit <= 0 {
		limit = 50
	}
	const seconds = "2006-01-02T15:04:05"
	rows, err := s.eventReader().QueryContext(ctx, `SELECT action, metadata, created_at FROM app_logs
WHERE action IN ('llm_usage', 'append_context') AND created_at >= ?
AND json_extract(metadata, '$.group_id') = ?
ORDER BY created_at ASC`, since.UTC().Format(seconds), groupID)
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	turns := map[string]*replyRequestTurn{}
	decisions := map[string]map[string]any{}
	for rows.Next() {
		var action, createdAt string
		var metadata sql.NullString
		if err := rows.Scan(&action, &metadata, &createdAt); err != nil {
			return stats, err
		}
		var meta map[string]any
		if json.Unmarshal([]byte(metadata.String), &meta) != nil {
			continue
		}
		// 同一个群可能同时有两台机器人，各看各的。
		if owner, _ := meta["profile_id"].(string); profileID != "" && strings.TrimSpace(owner) != "" && strings.TrimSpace(owner) != profileID {
			continue
		}
		messageID, _ := meta["message_id"].(string)
		if strings.TrimSpace(messageID) == "" {
			continue
		}
		if action == "append_context" {
			decisions[messageID] = meta
			continue
		}
		if purpose, _ := meta["purpose"].(string); purpose != "reply" {
			continue
		}
		call := replyRequestCall{input: int64Value(meta["input_tokens"]), cached: int64Value(meta["cached_input_tokens"])}
		if call.input <= 0 {
			continue
		}
		turn := turns[messageID]
		if turn == nil {
			model, _ := meta["model"].(string)
			turn = &replyRequestTurn{at: createdAt, first: call, model: model}
			turns[messageID] = turn
		}
		turn.requests++
		turn.all.input += call.input
		turn.all.cached += call.cached
	}
	if err := rows.Err(); err != nil {
		return stats, err
	}
	ids := make([]string, 0, len(turns))
	for id := range turns {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return turns[ids[i]].at > turns[ids[j]].at })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	var firstInputs []int64
	var first, all replyRequestCall
	for _, id := range ids {
		turn := turns[id]
		stats.Turns++
		stats.Requests += turn.requests
		firstInputs = append(firstInputs, turn.first.input)
		first.input += turn.first.input
		first.cached += turn.first.cached
		all.input += turn.all.input
		all.cached += turn.all.cached
		if turn.first.cached*10 < turn.first.input*3 {
			stats.Misses++
		}
		if turn.first.input > stats.FirstInputMax {
			stats.FirstInputMax = turn.first.input
		}
		if model := strings.TrimSpace(turn.model); model != "" {
			stats.Models[model]++
		}
		decision, ok := decisions[id]
		switch {
		case !ok:
			stats.Unrecorded++
		case decision["continued"] == true:
			stats.Continued++
		default:
			stats.Rebuilt++
			reason, _ := decision["reason"].(string)
			if strings.TrimSpace(reason) == "" {
				reason = "unknown"
			}
			stats.RebuildReasons[reason]++
		}
	}
	if len(firstInputs) > 0 {
		sort.Slice(firstInputs, func(i, j int) bool { return firstInputs[i] < firstInputs[j] })
		stats.FirstInputMedian = firstInputs[len(firstInputs)/2]
	}
	stats.FirstCachedPercent = cachedPercent(first)
	stats.AllCachedPercent = cachedPercent(all)
	return stats, nil
}

func cachedPercent(call replyRequestCall) float64 {
	if call.input <= 0 {
		return 0
	}
	return float64(call.cached) * 100 / float64(call.input)
}

func int64Value(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	}
	return 0
}
