// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// ReplyLatencySample 是一次真的回复出去的处理耗时，以及能如实拆出来的几个阶段。
//
// 阶段字段是指针：nil 表示这一条量不到，而不是 0。非流式调用没有首 token 时刻，
// 老日志没有工具耗时，这些拿 0 去参与平均只会把数字拉成假的好看。
type ReplyLatencySample struct {
	EventID     string    `json:"event_id"`
	MessageID   string    `json:"message_id,omitempty"`
	ProfileID   string    `json:"profile_id,omitempty"`
	Kind        string    `json:"kind"`
	GroupID     string    `json:"group_id,omitempty"`
	CompletedAt time.Time `json:"completed_at"`
	// TotalMS 是回复轮次的墙钟耗时（inbound_events.duration_ms），和总览卡片的平均响应同一个数。
	TotalMS int64 `json:"total_ms"`
	// WaitMS 是进队列到回复轮次开始之间的等待：排队、冷却、回复判断。不在 TotalMS 里。
	WaitMS *int64 `json:"wait_ms,omitempty"`
	// TTFTMS 是这一轮第一个流式模型调用的首 token 时延。
	TTFTMS *int64 `json:"ttft_ms,omitempty"`
	// ModelMS 是这一轮所有模型调用的耗时之和；并行调用时可能超过 TotalMS。
	ModelMS *int64 `json:"model_ms,omitempty"`
	// ToolMS 是 Agent 执行工具的耗时之和。
	ToolMS     *int64 `json:"tool_ms,omitempty"`
	ModelCalls int64  `json:"model_calls,omitempty"`

	startedAt time.Time
}

// turnStart 是回复轮次开始的时刻。记到了回复前等待就用入队时刻加等待，那是准的；
// 老数据只能拿完成时刻倒推，completed_at 比轮次结束略晚，倒推出来的起点也略晚一点。
func (s ReplyLatencySample) turnStart() time.Time {
	if !s.startedAt.IsZero() {
		return s.startedAt
	}
	return s.CompletedAt.Add(-time.Duration(s.TotalMS) * time.Millisecond)
}

// replyLatencyCallSlack 容忍日志时间戳和完成时间之间的先后：日志在调用返回时写，
// completed_at 在整轮收尾后写，理论上总在后面，但两边各自取的时钟。只放宽结束那一侧：
// 起点那一侧紧挨着的是回复判断那次模型调用，它在轮次开始前返回，放宽了就会被算进来。
const replyLatencyCallSlack = 2 * time.Second

// ReplyLatencySamples 读取完成时间落在 [since, until) 里的回复，并按消息把模型调用和
// Agent 工具耗时对上去。profileID 非空时只看这台机器人。
//
// 全部取自已经落库的数据：inbound_events 记整轮耗时和回复前等待，app_logs 里的
// llm_usage 记每次模型调用的耗时和首 token，agent_run 的收尾记录记工具耗时。
// 这几张表本来就跨重启保留（llm_usage 永不清理，运行日志默认留 30 天），不另开一套样本表。
func (s *SQLiteStore) ReplyLatencySamples(ctx context.Context, since, until time.Time, profileID string) ([]ReplyLatencySample, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("reply latency storage unavailable")
	}
	if !since.Before(until) {
		return nil, fmt.Errorf("invalid latency window")
	}
	defer s.observeStorage(ctx, "ReplyLatencySamples", "read")()

	profileID = strings.TrimSpace(profileID)
	args := []any{since.UnixNano(), until.UnixNano()}
	profileCondition := ""
	if profileID != "" {
		profileCondition = " AND profile_id = ?"
		args = append(args, profileID)
	}
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT id, COALESCE(message_id, ''), COALESCE(profile_id, ''), kind, COALESCE(group_id, ''),
  duration_ms, created_at, completed_at, reply_wait_ms
FROM inbound_events
WHERE completed_at >= ? AND completed_at < ?
  AND `+inboundHandledPredicate+` AND COALESCE(duration_ms, 0) > 0`+profileCondition+`
ORDER BY completed_at`, args...)
	if err != nil {
		return nil, fmt.Errorf("query reply latency: %w", err)
	}
	var samples []ReplyLatencySample
	for rows.Next() {
		var sample ReplyLatencySample
		var createdAt, completedAt int64
		var wait sql.NullInt64
		if err := rows.Scan(&sample.EventID, &sample.MessageID, &sample.ProfileID, &sample.Kind, &sample.GroupID,
			&sample.TotalMS, &createdAt, &completedAt, &wait); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan reply latency: %w", err)
		}
		sample.CompletedAt = time.Unix(0, completedAt).UTC()
		if wait.Valid {
			value := wait.Int64
			sample.WaitMS = &value
			sample.startedAt = time.Unix(0, createdAt).Add(time.Duration(value) * time.Millisecond).UTC()
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate reply latency: %w", err)
	}
	_ = rows.Close()
	if len(samples) == 0 {
		return samples, nil
	}

	byMessage := map[string][]int{}
	earliest := until
	for index, sample := range samples {
		if id := strings.TrimSpace(sample.MessageID); id != "" {
			byMessage[id] = append(byMessage[id], index)
		}
		if start := sample.turnStart(); start.Before(earliest) {
			earliest = start
		}
	}
	// 按消息找出调用落在哪一轮里。同一个消息号可能对应不止一行（平台消息号撞了、
	// 或者手动重试过），按时间把调用归给包住它的那一轮。
	owner := func(messageID string, at time.Time) int {
		for _, index := range byMessage[strings.TrimSpace(messageID)] {
			sample := samples[index]
			if !at.Before(sample.turnStart()) && !at.After(sample.CompletedAt.Add(replyLatencyCallSlack)) {
				return index
			}
		}
		return -1
	}
	// created_at 是 RFC3339Nano 文本，整秒时不带小数，字典序比较在同一秒内会错位；
	// 往前多取一秒，精确的归属交给上面的 owner。
	logSince := earliest.Add(-time.Second).UTC().Format(time.RFC3339Nano)
	logUntil := until.Add(replyLatencyCallSlack).UTC().Format(time.RFC3339Nano)

	if err := s.attachReplyLatencyModelCalls(ctx, samples, owner, logSince, logUntil); err != nil {
		return nil, err
	}
	if err := s.attachReplyLatencyToolTime(ctx, samples, owner, logSince, logUntil); err != nil {
		return nil, err
	}
	return samples, nil
}

func (s *SQLiteStore) attachReplyLatencyModelCalls(ctx context.Context, samples []ReplyLatencySample, owner func(string, time.Time) int, since, until string) error {
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT COALESCE(target, ''), created_at,
  CASE WHEN json_valid(metadata) THEN json_extract(metadata, '$.message_id') END,
  CASE WHEN json_valid(metadata) THEN json_extract(metadata, '$.duration_ms') END,
  CASE WHEN json_valid(metadata) THEN json_extract(metadata, '$.ttft_ms') END
FROM app_logs
WHERE action = 'llm_usage' AND created_at >= ? AND created_at < ?`, since, until)
	if err != nil {
		return fmt.Errorf("query reply latency model calls: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type firstCall struct {
		start time.Time
		ttft  int64
	}
	first := map[int]firstCall{}
	for rows.Next() {
		var target, createdAt string
		var metaMessageID sql.NullString
		var duration, ttft sql.NullFloat64
		if err := rows.Scan(&target, &createdAt, &metaMessageID, &duration, &ttft); err != nil {
			return fmt.Errorf("scan reply latency model calls: %w", err)
		}
		at, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			continue
		}
		messageID := strings.TrimSpace(target)
		if messageID == "" {
			messageID = strings.TrimSpace(metaMessageID.String)
		}
		index := owner(messageID, at)
		if index < 0 {
			continue
		}
		durationMS := int64(0)
		if duration.Valid && duration.Float64 > 0 {
			durationMS = int64(duration.Float64)
		}
		sample := &samples[index]
		sample.ModelCalls++
		// 老日志没有 duration_ms：有调用、量不到时长时宁可留空，也不写 0。
		if durationMS > 0 {
			total := durationMS
			if sample.ModelMS != nil {
				total += *sample.ModelMS
			}
			sample.ModelMS = &total
		}
		// 首 token 取这一轮最早开始的那次流式调用。非流式调用没有 ttft_ms，跳过它们
		// 而不是当 0 算。
		if ttft.Valid && ttft.Float64 > 0 {
			start := at.Add(-time.Duration(durationMS) * time.Millisecond)
			if current, ok := first[index]; !ok || start.Before(current.start) {
				first[index] = firstCall{start: start, ttft: int64(ttft.Float64)}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate reply latency model calls: %w", err)
	}
	for index, call := range first {
		value := call.ttft
		samples[index].TTFTMS = &value
	}
	return nil
}

func (s *SQLiteStore) attachReplyLatencyToolTime(ctx context.Context, samples []ReplyLatencySample, owner func(string, time.Time) int, since, until string) error {
	// agent_run 的收尾记录 target 就是消息号；单次工具日志的 target 是工具名，联网搜索那条
	// 还抹掉了消息号，按它们汇总会漏，所以只认收尾记录上的 tools_duration_ms。
	rows, err := s.eventReader().QueryContext(ctx, `
SELECT COALESCE(target, ''), created_at,
  CASE WHEN json_valid(metadata) THEN json_extract(metadata, '$.phase') END,
  CASE WHEN json_valid(metadata) THEN json_extract(metadata, '$.tools_duration_ms') END
FROM app_logs
WHERE action = 'agent_run' AND created_at >= ? AND created_at < ?`, since, until)
	if err != nil {
		return fmt.Errorf("query reply latency tool time: %w", err)
	}
	defer func() { _ = rows.Close() }()
	// 同一轮里只要有一次 Agent 运行没记工具耗时（升级前的日志），这一轮的工具耗时就
	// 量不全，整条留空。
	incomplete := map[int]bool{}
	for rows.Next() {
		var target, createdAt string
		var phase sql.NullString
		var toolMS sql.NullFloat64
		if err := rows.Scan(&target, &createdAt, &phase, &toolMS); err != nil {
			return fmt.Errorf("scan reply latency tool time: %w", err)
		}
		if phase.String != "completed" && phase.String != "failed" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			continue
		}
		index := owner(target, at)
		if index < 0 {
			continue
		}
		if !toolMS.Valid {
			incomplete[index] = true
			continue
		}
		total := int64(math.Max(0, toolMS.Float64))
		if samples[index].ToolMS != nil {
			total += *samples[index].ToolMS
		}
		samples[index].ToolMS = &total
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate reply latency tool time: %w", err)
	}
	for index := range incomplete {
		samples[index].ToolMS = nil
	}
	return nil
}

// LatencyDistribution 是一组耗时的分布。Samples 为 0 时其余字段都没有意义，前端显示「—」。
type LatencyDistribution struct {
	Samples int   `json:"samples"`
	AvgMS   int64 `json:"avg_ms"`
	P50MS   int64 `json:"p50_ms"`
	P90MS   int64 `json:"p90_ms"`
	P99MS   int64 `json:"p99_ms"`
	MinMS   int64 `json:"min_ms"`
	MaxMS   int64 `json:"max_ms"`
}

// SummarizeLatency 计算一组耗时的平均值和分位数。分位数用最近秩法：取排序后第
// ⌈p·n⌉ 个样本，结果总是一个真实出现过的耗时，不在两个样本之间插值。
func SummarizeLatency(values []int64) LatencyDistribution {
	if len(values) == 0 {
		return LatencyDistribution{}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var sum int64
	for _, value := range sorted {
		sum += value
	}
	return LatencyDistribution{
		Samples: len(sorted),
		AvgMS:   int64(math.Round(float64(sum) / float64(len(sorted)))),
		P50MS:   nearestRank(sorted, 50),
		P90MS:   nearestRank(sorted, 90),
		P99MS:   nearestRank(sorted, 99),
		MinMS:   sorted[0],
		MaxMS:   sorted[len(sorted)-1],
	}
}

func nearestRank(sorted []int64, percentile float64) int64 {
	rank := int(math.Ceil(percentile / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// ReplyLatencySummary 是一个时间窗里的回复耗时：整轮耗时和各阶段各自的分布。
// 各阶段的样本数可以比整轮少——量不到的那几条不参与。
type ReplyLatencySummary struct {
	Since   time.Time            `json:"since"`
	Until   time.Time            `json:"until"`
	Total   LatencyDistribution  `json:"total"`
	Wait    LatencyDistribution  `json:"wait"`
	TTFT    LatencyDistribution  `json:"ttft"`
	Model   LatencyDistribution  `json:"model"`
	Tool    LatencyDistribution  `json:"tool"`
	Slowest []ReplyLatencySample `json:"slowest,omitempty"`
	Fastest []ReplyLatencySample `json:"fastest,omitempty"`
}

// SummarizeReplyLatency 汇总完成时间落在 [since, until) 里的样本，并挑出最慢、最快各
// extremes 条。
func SummarizeReplyLatency(samples []ReplyLatencySample, since, until time.Time, extremes int) ReplyLatencySummary {
	summary := ReplyLatencySummary{Since: since, Until: until}
	var inWindow []ReplyLatencySample
	var total, wait, ttft, model, tool []int64
	for _, sample := range samples {
		if sample.CompletedAt.Before(since) || !sample.CompletedAt.Before(until) {
			continue
		}
		inWindow = append(inWindow, sample)
		total = append(total, sample.TotalMS)
		if sample.WaitMS != nil {
			wait = append(wait, *sample.WaitMS)
		}
		if sample.TTFTMS != nil {
			ttft = append(ttft, *sample.TTFTMS)
		}
		if sample.ModelMS != nil {
			model = append(model, *sample.ModelMS)
		}
		if sample.ToolMS != nil {
			tool = append(tool, *sample.ToolMS)
		}
	}
	summary.Total = SummarizeLatency(total)
	summary.Wait = SummarizeLatency(wait)
	summary.TTFT = SummarizeLatency(ttft)
	summary.Model = SummarizeLatency(model)
	summary.Tool = SummarizeLatency(tool)
	if extremes > 0 && len(inWindow) > 0 {
		// 同样耗时的按完成时间新的在前，结果稳定，前端刷新时列表不会乱跳。
		sort.SliceStable(inWindow, func(i, j int) bool {
			if inWindow[i].TotalMS != inWindow[j].TotalMS {
				return inWindow[i].TotalMS > inWindow[j].TotalMS
			}
			return inWindow[i].CompletedAt.After(inWindow[j].CompletedAt)
		})
		count := min(extremes, len(inWindow))
		summary.Slowest = append([]ReplyLatencySample(nil), inWindow[:count]...)
		// 样本不多时最慢和最快会是同一批，最快那边只取剩下的，免得同一条出现两次。
		rest := inWindow[count:]
		fastest := min(extremes, len(rest))
		for index := 0; index < fastest; index++ {
			summary.Fastest = append(summary.Fastest, rest[len(rest)-1-index])
		}
	}
	return summary
}
