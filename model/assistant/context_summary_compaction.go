// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// contextSummaryHeaderPrefix 标出这段压缩摘要覆盖了哪一段被移出近期窗口的历史。
// 没有水位标识时，模型分不清摘要讲的是十分钟前还是上周，重新压缩也会把时间边界
// 一起压没。
const contextSummaryHeaderPrefix = "【较早上下文摘要范围："

// contextSummaryMaxRunes 是摘要累积时的上限，防止摘要本身无限增长。
const contextSummaryMaxRunes = 4000

// minimumContextSummaryTokens 保证再紧张的窗口也给摘要留下能放进水位标识和
// 几条结论的空间；比这更小就不值得带摘要了。
const minimumContextSummaryTokens int64 = 192

func contextSummaryTimeLabel(unix int64) string {
	if unix <= 0 {
		return "未知时间"
	}
	return time.Unix(unix, 0).Local().Format("2006-01-02 15:04")
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

// parseContextSummaryHeader 取回已有水位标识里的起点和条数，让多次合并累加而不是
// 每次都从本批事件重新起算。
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

// dropOldestContextSummaryLines 按整行丢弃最旧的正文，直到摘要落进 maxRunes。
// 与按字符截断的区别在于每一条留下来的记录都仍然完整。
func dropOldestContextSummaryLines(header string, body []string, maxRunes int) []string {
	if maxRunes <= 0 {
		return body
	}
	for len(body) > 0 && len([]rune(joinContextSummary(header, body))) > maxRunes {
		body = body[1:]
	}
	return body
}

// fitOlderSummaryToBudget 让较早上下文摘要落进它的目标配额。
//
// 这里原本会先请模型把摘要重新压一遍，压不下去再结构化裁剪。那条模型压缩路径
// 已经删掉：实测从未触发过（窗口只用到 12%），而方向上历史开销由稳定前缀缓存
// 负责，不靠把历史压短——见 docs/group-prompt-cache.md。留着只是多一次可能
// 发生的模型调用和一段要跟着改的代码。
//
// 现在只做结构化裁剪：整行丢掉最旧的记录，水位标识始终保留。摘要是一个完整语义
// 单元，首尾截断会把结论、实体关系和时间边界一起切掉，所以宁可整条丢。
func (r *Runtime) fitOlderSummaryToBudget(summary string, budget int64) (string, bool) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", false
	}
	if budget < minimumContextSummaryTokens {
		budget = minimumContextSummaryTokens
	}
	if llm.EstimateTextTokens(summary) <= budget {
		return summary, false
	}
	header, body := splitContextSummary(summary)
	targetRunes := int(budget * 3 / 4)
	reduced := dropOldestContextSummaryLines(header, body, targetRunes)
	if len(reduced) == len(body) {
		return summary, false
	}
	if len(reduced) == 0 {
		return joinContextSummary(header, nil), true
	}
	return joinContextSummary(header, reduced), true
}
