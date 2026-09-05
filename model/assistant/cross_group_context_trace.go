// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// 跨群上下文此前一行日志都没有：命中了、被哪道关卡挡了、挡掉多少，从外面完全
// 看不出来。它要连过多道过滤（开关、查询信号、时间边界、跨群与平台、相关性、原发言者
// 是否也在当前群），任何一道不过都是静默返回 nil，表现和「功能没生效」一模一样。
//
// 这里把整条漏斗记成一条调试日志。只在调试模式下写，正常运行不产生额外开销。

// crossGroupContextTrace 是一次跨群检索的漏斗计数。
type crossGroupContextTrace struct {
	// SkipReason 非空表示还没走到检索就退出了。
	SkipReason string
	Terms      int
	// SampleTerms 只取前几个权重最高的词，用来判断分词是不是跑偏了。
	SampleTerms []string
	Candidates  int
	// 各道过滤刷掉的候选数。
	DroppedSameGroup int
	DroppedOutbound  int
	DroppedPlatform  int
	DroppedTopic     int
	DroppedText      int
	// Authors 是通过内容过滤、进入成员校验的作者数；AllowedAuthors 是校验通过的。
	Authors            int
	AllowedAuthors     int
	Selected           int
	DurationMS         int64
	KeywordCandidates  int
	SemanticCandidates int
	KeywordStatus      string
	SemanticStatus     string
	DroppedMembership  int
	DroppedLimit       int
	DroppedTime        int
	SelectedMessages   []map[string]any
}

// crossGroupTraceEnabled 报告当前事件要不要记录漏斗。
func (r *Runtime) crossGroupTraceEnabled(event MessageEvent) bool {
	return r != nil && r.appLogWriter() != nil && r.effectiveConfigForEvent(event).DebugModeEnabled
}

// recordCrossGroupContextTrace 把漏斗写进调试日志。
func (r *Runtime) recordCrossGroupContextTrace(event MessageEvent, trace crossGroupContextTrace) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	metadata := map[string]any{
		"platform":            event.Platform,
		"kind":                string(event.Kind),
		"profile_id":          event.ProfileID,
		"group_id":            event.GroupID,
		"user_id":             event.UserID,
		"message_id":          event.MessageID,
		"duration_ms":         trace.DurationMS,
		"selected":            trace.Selected,
		"search_range":        "全部已保存历史，近期优先",
		"keyword_candidates":  trace.KeywordCandidates,
		"semantic_candidates": trace.SemanticCandidates,
		"keyword_status":      trace.KeywordStatus,
		"semantic_status":     trace.SemanticStatus,
		"selected_messages":   trace.SelectedMessages,
	}
	message := "跨群上下文检索完成"
	if trace.SkipReason != "" {
		metadata["skip_reason"] = trace.SkipReason
		message = "跨群上下文未检索"
	} else {
		metadata["terms"] = trace.Terms
		metadata["sample_terms"] = trace.SampleTerms
		metadata["candidates"] = trace.Candidates
		metadata["dropped"] = map[string]int{
			"same_group": trace.DroppedSameGroup,
			"outbound":   trace.DroppedOutbound,
			"platform":   trace.DroppedPlatform,
			"topic":      trace.DroppedTopic,
			"text":       trace.DroppedText,
			"membership": trace.DroppedMembership,
			"limit":      trace.DroppedLimit,
			"time":       trace.DroppedTime,
		}
		metadata["authors"] = trace.Authors
		metadata["allowed_authors"] = trace.AllowedAuthors
	}
	logCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:     applog.KindDebug,
		Level:    applog.LevelInfo,
		Action:   "diana.cross_group_context",
		Message:  message,
		Target:   event.MessageID,
		Metadata: metadata,
	})
}
