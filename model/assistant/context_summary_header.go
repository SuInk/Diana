// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
	"time"
)

// contextSummaryHeaderPrefix 标出这段压缩摘要覆盖了哪一段历史（见 context_compaction.go）。
// 没有它模型分不清摘要讲的是十分钟前还是上周，增量更新时也会把时间边界一起压没。
const contextSummaryHeaderPrefix = "【较早上下文摘要范围："

// contextSummaryTimeLabel 是摘要水位标识里的时间，带 UTC 偏移：摘要文本会一直累积，
// 中途改过机器人时区时，起点和终点可能是不同时区写下的，不标偏移模型分不出来。
func contextSummaryTimeLabel(unix int64, location *time.Location) string {
	if unix <= 0 {
		return "未知时间"
	}
	return formatZonedTime(time.Unix(unix, 0).In(location), "2006-01-02 15:04")
}

// contextSummaryHeader 渲染水位标识。条数为零时返回空串。
func contextSummaryHeader(start, end string, count int) string {
	if count <= 0 {
		return ""
	}
	if strings.TrimSpace(start) == "" {
		start = "未知时间"
	}
	if strings.TrimSpace(end) == "" {
		end = "未知时间"
	}
	return fmt.Sprintf("%s%s ~ %s，共 %d 条】", contextSummaryHeaderPrefix, start, end, count)
}

// splitContextSummary 把摘要拆成水位标识与正文行。没有标识时 header 为空。
func splitContextSummary(summary string) (string, []string) {
	lines := strings.Split(strings.TrimSpace(summary), "\n")
	header := ""
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), contextSummaryHeaderPrefix) {
		header = strings.TrimSpace(lines[0])
		lines = lines[1:]
	}
	body := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			body = append(body, line)
		}
	}
	return header, body
}

func joinContextSummary(header string, body []string) string {
	parts := make([]string, 0, len(body)+1)
	if strings.TrimSpace(header) != "" {
		parts = append(parts, header)
	}
	parts = append(parts, body...)
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// parseContextSummaryHeader 取回已有标识里的起点和条数，让多次压缩累加而不是每次
// 都从本批事件重新起算。
func parseContextSummaryHeader(header string) (start string, count int) {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, contextSummaryHeaderPrefix) {
		return "", 0
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(header, contextSummaryHeaderPrefix), "】")
	rangePart, countPart, ok := strings.Cut(inner, "，共 ")
	if !ok {
		return "", 0
	}
	start, _, _ = strings.Cut(rangePart, " ~ ")
	countPart = strings.TrimSuffix(countPart, " 条")
	if _, err := fmt.Sscanf(countPart, "%d", &count); err != nil {
		count = 0
	}
	return strings.TrimSpace(start), count
}
