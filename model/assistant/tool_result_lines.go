// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
)

// toolResultBudget 返回这次工具结果的字数上限：Runner 给的预算，不在 Runner 里
// 调用时回落到 agent.ToolOutputMaxChars。
func toolResultBudget(ctx context.Context) int {
	if budget := agent.ToolOutputBudget(ctx); budget > 0 {
		return budget
	}
	return agent.ToolOutputMaxChars
}

// textLineWindow 是从一段文本里按整行取出的一页，行号从 1 起。
type textLineWindow struct {
	Text  string
	Start int
	// End 是装下的最后一行（含）；End < Start 表示一行都没装下。
	End   int
	Total int
	// CutRunes 不为 0 时，End 这一行本身就超过单次上限，只给了开头，省略了这么多字。
	CutRunes int
}

// NextLine 是续读要传的起始行；0 表示已经读到末尾。
func (w textLineWindow) NextLine() int {
	if w.End >= w.Total {
		return 0
	}
	return w.End + 1
}

// ResumeLine 是没给完时续读要传的起始行，0 表示没有可续读的了。一行都没装下就从
// 起始行再读；只剩一行超长、只给了开头时这一行算读过（省略了多少字由 Range 写明），
// 否则续读会卡在同一行上原地打转。
func (w textLineWindow) ResumeLine() int {
	if next := w.NextLine(); next > 0 {
		return next
	}
	if w.End < w.Start && w.Start <= w.Total {
		return w.Start
	}
	return 0
}

// Complete 说明从 Start 起没有可续读的内容了。
func (w textLineWindow) Complete() bool {
	return w.ResumeLine() == 0
}

// Whole 说明这一页就是原文全文：从第 1 行起、读到末尾、没有超长行被切。
func (w textLineWindow) Whole() bool {
	return w.Complete() && w.Start <= 1 && w.CutRunes == 0
}

// Range 写成「第 a–b 行，共 n 行」，给续读说明用。
func (w textLineWindow) Range() string {
	if w.End < w.Start {
		return fmt.Sprintf("第 %d 行起一行都没装下，共 %d 行", w.Start, w.Total)
	}
	text := fmt.Sprintf("第 %d–%d 行，共 %d 行", w.Start, w.End, w.Total)
	if w.CutRunes > 0 {
		text += fmt.Sprintf("；第 %d 行超过单次上限，只给了开头，后面 %d 字没有给出", w.End, w.CutRunes)
	}
	return text
}

// takeTextLines 照 Pi 的做法从第 start 行起按整行装进 budget：下一行装不下就停在
// 它前面，不缩短行、不跳行。cost 衡量一段文字放进结果后占多少字（纯文本按 rune，
// 嵌进 JSON 的用 jsonStringRunes）。第一行就装不下时只给它的开头并记下省略多少字，
// 免得一行超长的文本永远读不出来。
func takeTextLines(text string, start, budget int, cost func(string) int) textLineWindow {
	lines := []string{}
	if text != "" {
		lines = strings.Split(text, "\n")
	}
	if start < 1 {
		start = 1
	}
	window := textLineWindow{Start: start, End: start - 1, Total: len(lines)}
	if start > len(lines) {
		return window
	}
	newline := cost("\n")
	var builder strings.Builder
	used := 0
	for index := start - 1; index < len(lines); index++ {
		line := lines[index]
		lineCost := cost(line)
		if index > start-1 {
			lineCost += newline
		}
		if used+lineCost > budget {
			if index == start-1 && budget > 0 {
				prefix := textPrefixWithin(line, budget, cost)
				builder.WriteString(prefix)
				window.End = index + 1
				window.CutRunes = utf8.RuneCountInString(line) - utf8.RuneCountInString(prefix)
			}
			break
		}
		if index > start-1 {
			builder.WriteString("\n")
		}
		builder.WriteString(line)
		used += lineCost
		window.End = index + 1
	}
	window.Text = builder.String()
	return window
}

// textPrefixWithin 取 line 开头不超过 budget 的部分（按 cost 计）。
func textPrefixWithin(line string, budget int, cost func(string) int) string {
	runes := []rune(line)
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		if cost(string(runes[:mid])) <= budget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return string(runes[:low])
}

// jsonStringRunes 是 text 编成 JSON 字符串后（不含两端引号）占多少字：换行、引号、
// HTML 字符都会被转义，按原文 rune 数算会让结果超出预算。
func jsonStringRunes(text string) int {
	body, err := json.Marshal(text)
	if err != nil {
		return utf8.RuneCountInString(text)
	}
	return utf8.RuneCount(body) - 2
}

func plainRunes(text string) int {
	return utf8.RuneCountInString(text)
}
