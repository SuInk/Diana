// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"regexp"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// 历史行的日期只在变化时写一次。
//
// historyLinePrefix 给每条历史都渲染完整的「2026-09-28 07:33:05」，线上一次主回复
// 带七百来条历史，几乎全是同一两天的，光重复的日期就有七千多字。这里在请求发出前
// 按消息顺序过一遍：和上面最近一次出现的日期相同的，只留时分秒。
//
// 为什么不在渲染时就省：历史行按事件缓存（GroupPromptHistoryEntry），而且预裁剪和
// 供应商兜底都会从旧往新丢整轮历史。渲染时省掉的日期，只要带日期的那条被丢，后面
// 同一天的行就只剩时分秒、没有日期可沿用。放在预算层之后，看到的就是最终要发的那
// 一份：第一条历史一定带日期，省略提示后面接着的也一定带。
//
// 输出只取决于消息本身，不看当前时间：同一段历史每次省出来的结果逐字相同，前面的
// 行也不会因为后面追加了什么而变，前缀缓存不受影响。供应商客户端还有最后一道
// 兜底裁剪（fitMessagesToTokenBudget）在这之后，它可能丢掉带日期的那条，所以省过
// 的消息把原文留在 UntrimmedContent：一旦走到裁剪，全部换回完整日期再裁。

// historyDateInheritNotice 是省日期的前提：请求的 system 里有这句说明才省。它写在
// promptHistoryFormat 里，只有主回复的 system 头部带；判断类调用、以及覆盖了这段
// 提示词的机器人都没有这句，它们的历史行照旧每条带完整日期，不会让模型凭空猜日子。
const historyDateInheritNotice = "日期只在第一条和跨天时写出（如「[历史 2026-09-03 14:05:00]」），之后同一天的只写时分秒（如「[历史 14:06:12]」），日期沿用上面最近一次写出的。"

var (
	// historyLineDatePattern 匹配运行时渲染的本群历史行开头，只有这种才会被省。
	// 跨群历史不省：它们只有零星几条，排在易变的尾部，还带着「约多久前」，照旧写全。
	historyLineDatePattern = regexp.MustCompile(`^(\[历史 )(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2}\] )`)
	// historyLineDateSeenPattern 认模型可能当成「日期上下文」的所有行首，包括中和后
	// 的全角写法和伪造的行。它们只用来更新「上面最近的日期」，不会被改写：伪造一行
	// 别的日期，结果只会是下一条真历史照常带上日期，读不出错的日子。
	historyLineDateSeenPattern = regexp.MustCompile(`^[\[［](?:跨群)?历史 (\d{4}-\d{2}-\d{2}) `)
)

func withHistoryLineDatesRun(run llmProviderRunFunc) llmProviderRunFunc {
	return func(provider LLMProvider) (string, error) {
		return run(&historyLineDatesProvider{provider: provider})
	}
}

type historyLineDatesProvider struct{ provider LLMProvider }

func (p *historyLineDatesProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return p.provider.Generate(ctx, requestWithCompactHistoryDates(req))
}

// requestWithCompactHistoryDates 省掉和上一条相同的历史日期。只改标成历史优先级的
// user 消息；其他消息（摘要、当前消息、工具结果）里的日期行只参与记录，不动。
func requestWithCompactHistoryDates(req llm.GenerateRequest) llm.GenerateRequest {
	explained := false
	for _, message := range req.Messages {
		explained = explained || (message.Role == llm.RoleSystem && strings.Contains(message.Content, historyDateInheritNotice))
	}
	if !explained {
		return req
	}
	// 复制切片：重试和 Agent 循环会复用原请求。
	messages := make([]llm.Message, len(req.Messages))
	lastDate := ""
	for index, message := range req.Messages {
		if message.Role != llm.RoleSystem {
			rewrite := message.Role == llm.RoleUser && len(message.Parts) == 0 &&
				(message.Priority == llm.MessagePriorityHistory || message.Priority == llm.MessagePriorityRecentHistory)
			if rewrite {
				if compact := compactHistoryLineDates(message.Content, &lastDate); compact != message.Content {
					message.UntrimmedContent = message.Content
					message.Content = compact
				}
			} else {
				noteHistoryLineDates(message.Content, &lastDate)
				for _, part := range message.Parts {
					if part.Type == llm.ContentPartText {
						noteHistoryLineDates(part.Text, &lastDate)
					}
				}
			}
		}
		messages[index] = message
	}
	req.Messages = messages
	return req
}

// compactHistoryLineDates 逐行处理：一条消息可能拼了多行历史（Agent 的历史图片批次）。
func compactHistoryLineDates(text string, lastDate *string) string {
	if !strings.Contains(text, "历史 ") {
		return text
	}
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		if match := historyLineDatePattern.FindStringSubmatch(line); match != nil {
			if match[2] == *lastDate {
				lines[index] = match[1] + match[3] + line[len(match[0]):]
			}
			*lastDate = match[2]
			continue
		}
		if match := historyLineDateSeenPattern.FindStringSubmatch(line); match != nil {
			*lastDate = match[1]
		}
	}
	return strings.Join(lines, "\n")
}

func noteHistoryLineDates(text string, lastDate *string) {
	if !strings.Contains(text, "历史 ") {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		if match := historyLineDateSeenPattern.FindStringSubmatch(line); match != nil {
			*lastDate = match[1]
		}
	}
}
