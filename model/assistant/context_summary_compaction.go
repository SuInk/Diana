// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
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

const contextSummaryCompactionPrompt = `你是一个上下文摘要压缩器。输入是一段群聊/私聊的历史压缩摘要，它已经超出可用预算，需要你重新压缩得更短。

要求：
1. 必须保留输入第一行的【较早上下文摘要范围：…】水位标识，原样输出，不要改写其中的时间和条数。
2. 保留人物与人物之间的关系、已经达成的结论、待办和承诺、明确的时间边界、数字与专有名词。
3. 丢掉寒暄、重复表达、情绪词和与后续对话无关的细节。
4. 不要从中间截断句子，输出必须是完整可读的摘要。
5. 不要新增输入里没有的信息，不要推测。
6. 直接输出压缩后的摘要正文，不要输出解释、Markdown 代码块或额外说明。

目标长度：不超过 %d 个汉字。`

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

// fitOlderSummaryToBudget 让较早上下文压缩摘要落进它的目标配额。
// 摘要是一个完整语义单元：首尾截断会把结论、实体关系和时间边界一起切掉，留下
// 一段看着完整、其实缺了要点的背景。超额时先请模型重新压缩，压缩失败或仍然超额
// 再按整行丢弃最旧的记录，水位标识始终保留。
func (r *Runtime) fitOlderSummaryToBudget(ctx context.Context, summary string, budget int64, cfg BotConfig) (string, bool) {
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
	// 估算里一个汉字约一个 token 有余，按目标 token 折算成汉字数给模型一个明确上限。
	targetRunes := int(budget * 3 / 4)
	if compressed := r.recompressContextSummary(ctx, summary, targetRunes, cfg); compressed != "" {
		compressedHeader, compressedBody := splitContextSummary(compressed)
		if compressedHeader == "" {
			compressedHeader = header
		}
		if rebuilt := joinContextSummary(compressedHeader, compressedBody); llm.EstimateTextTokens(rebuilt) <= budget {
			return rebuilt, true
		}
	}
	// 模型不可用或压得不够狠时退回结构化裁剪：丢掉最旧的整条记录。
	reduced := dropOldestContextSummaryLines(header, body, targetRunes)
	if len(reduced) == len(body) {
		return summary, false
	}
	if len(reduced) == 0 {
		return joinContextSummary(header, nil), true
	}
	return joinContextSummary(header, reduced), true
}

func (r *Runtime) recompressContextSummary(ctx context.Context, summary string, targetRunes int, cfg BotConfig) string {
	ctx = withLLMUsagePurpose(ctx, "context_summary_compaction")
	if targetRunes <= 0 {
		return ""
	}
	compactCtx, cancel := context.WithTimeout(ctx, contextSummaryCompactionTimeout(cfg))
	defer cancel()
	raw, err := r.runLLMRouterProvider(compactCtx, func(client LLMProvider) (string, error) {
		resp, generateErr := client.Generate(compactCtx, llm.GenerateRequest{
			Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: fmt.Sprintf(contextSummaryCompactionPrompt, targetRunes)},
				{Role: llm.RoleUser, Content: "请压缩以下摘要：\n" + summary},
			},
		})
		if generateErr != nil {
			return "", generateErr
		}
		return resp.Text, nil
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(stripJSONCodeFence(strings.TrimSpace(raw)))
}

func contextSummaryCompactionTimeout(cfg BotConfig) time.Duration {
	const budget = 30 * time.Second
	if cfg.RequestTimeout > 0 && cfg.RequestTimeout < budget {
		return cfg.RequestTimeout
	}
	return budget
}
