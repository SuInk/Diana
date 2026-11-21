// SPDX-License-Identifier: GPL-3.0-only
// Adapted from MaiBot src/chat/utils/utils.py (_split_into_sentence_segments,
// _merge_processed_segments_to_max_count, _finish_response) and
// src/config/official_configs.py (ResponseSplitterConfig) at
// b7f36825463413c47e61d44fe8a93427f6fe5a6f. See NOTICE.md.
package maibotreply

import (
	"regexp"
	"strings"
	"unicode"
)

// SplitterRevision 是断句规则移植自的 MaiBot 提交。
const SplitterRevision = "b7f36825463413c47e61d44fe8a93427f6fe5a6f"

// MaiBot ResponseSplitterConfig 的默认值。
const (
	// MaxSplitNum 是一次回复最多拆成几条消息。
	MaxSplitNum = 3
)

// Segment 是断出来的一句和原文里紧跟它的分隔符。几句合回一条时用分隔符恢复停顿。
type Segment struct {
	Text      string
	Separator string
}

var (
	splitBlankLines      = regexp.MustCompile(`\n\s*\n+`)
	splitNewlineThenSep  = regexp.MustCompile(`\n\s*([，,。;\s])`)
	splitSepThenNewline  = regexp.MustCompile(`([，,。;\s])\s*\n`)
	splitInlineNewlines  = regexp.MustCompile(`[^\S\r\n]*[\r\n]+[^\S\r\n]*`)
	splitQuoteCharacters = map[rune]bool{'"': true, '\'': true, '“': true, '”': true, '‘': true, '’': true, '「': true, '」': true, '『': true, '』': true}
	splitSeparators      = map[rune]bool{'，': true, ',': true, ' ': true, '。': true, ';': true, '；': true, '\n': true}
)

// Merge 把断出来的句子合到最多 MaxSplitNum 条。segments 可以来自好几段
// SplitSentences：条数上限管的是整条回复，不是其中一行。
//
// MaiBot 断出来超过 max_sentence_num 句时不拆（默认还会把原文换成一句默认回复），
// 因为它的提示词把回复压在一两句。Diana 的回复长得多，照那条规则长回复全成了一整块，
// 所以句子再多也合到三条。原文里有换行时，条与条的分界尽量落在换行处，不从一行
// 中间断开；没有换行就照 MaiBot 按句数均分。
func Merge(segments []Segment) []string {
	merged := segments
	if len(segments) > MaxSplitNum {
		merged = mergeAtCuts(segments, bestCuts(segments, MaxSplitNum))
	}
	out := make([]string, 0, len(merged))
	for _, segment := range merged {
		if text := strings.TrimSpace(segment.Text); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// cutPenalty 是在这种分隔符处断开的代价，按总字数的比例算：换行、句号、分号是一句话
// 说完的地方，逗号和空格只是停顿，从那里断开容易把一件事劈成两条。
func cutPenalty(separator string) float64 {
	switch separator {
	case "\n":
		return 0
	case "。", "；", ";":
		return 0.05
	default:
		return 0.25
	}
}

// bestCuts 在 2 到 maxCount 组里挑总代价最小的分法（分界 i 表示第 i 句之后断开）。
// 代价是每条字数偏离平均值的比例之和，加上每个分界的 cutPenalty：既不留一条孤零零
// 的短尾巴，也尽量不从逗号处把一件事断开。11-20 线上一条讲 RSI 的回复按句数均分，
// 把「0 到 100 衡量涨跌势头」和「过 70 算超买」拆进两条，最后剩一条「天天敲键盘的
// 熟客」，真正换话题的分号反而没用上。
func bestCuts(segments []Segment, maxCount int) []int {
	ends := make([]int, len(segments))
	total := 0
	for index, segment := range segments {
		total += len([]rune(segment.Text))
		ends[index] = total
	}
	if total == 0 {
		return nil
	}
	var best []int
	bestCost := -1.0
	var search func(start, groups int, cuts []int, cost float64)
	search = func(start, groups int, cuts []int, cost float64) {
		// start 是这一组第一句的下标；groups 是含这一组在内还剩几组。
		if groups == 1 {
			full := append(append([]int(nil), cuts...), len(segments)-1)
			sum, previous := 0.0, 0
			for _, end := range full {
				size := ends[end] - previous
				previous = ends[end]
				sum += absFloat(float64(size)-float64(total)/float64(len(full))) / float64(total)
			}
			if final := cost + sum; bestCost < 0 || final < bestCost {
				bestCost, best = final, append([]int(nil), cuts...)
			}
			return
		}
		for end := start; end <= len(segments)-groups; end++ {
			search(end+1, groups-1, append(cuts, end), cost+cutPenalty(segments[end].Separator))
		}
	}
	for groups := 2; groups <= maxCount && groups <= len(segments); groups++ {
		search(0, groups, nil, 0)
	}
	return best
}

func mergeAtCuts(segments []Segment, cuts []int) []Segment {
	var merged []Segment
	start := 0
	for _, end := range append(cuts, len(segments)-1) {
		var text strings.Builder
		for index := start; index <= end; index++ {
			text.WriteString(segments[index].Text)
			if index < end {
				text.WriteString(segments[index].Separator)
			}
		}
		merged = append(merged, Segment{Text: text.String(), Separator: segments[end].Separator})
		start = end + 1
	}
	return merged
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

// SplitSentences 在 ，, 。; 空格和换行处断句，分隔符不留在句子里，再按原文长度
// 概率性地把相邻句合回去：越长越倾向于拆开。换行一定拆；引号里、冒号旁、
// 英文和数字之间的空格、破折号旁的空格不拆。
func SplitSentences(text string, random func() float64) []Segment {
	text = splitBlankLines.ReplaceAllString(text, "\n")
	text = splitNewlineThenSep.ReplaceAllString(text, "\n$1")
	text = splitSepThenNewline.ReplaceAllString(text, "$1\n")

	runes := []rune(text)
	if len(runes) < 3 {
		if random() < 0.01 {
			out := make([]Segment, 0, len(runes))
			for _, r := range runes {
				out = append(out, Segment{Text: string(r)})
			}
			return out
		}
		return []Segment{{Text: text}}
	}

	insideQuote := make([]bool, len(runes))
	inQuote := false
	var quoteChar rune
	for index, char := range runes {
		if splitQuoteCharacters[char] {
			if !inQuote {
				inQuote, quoteChar = true, char
			} else if char == quoteChar || (char == '"' || char == '\'') && (quoteChar == '"' || quoteChar == '\'') {
				inQuote, quoteChar = false, 0
			}
			continue
		}
		insideQuote[index] = inQuote
	}

	var segments []Segment
	var current strings.Builder
	for index, char := range runes {
		if !splitSeparators[char] {
			current.WriteRune(char)
			continue
		}
		canSplit := !insideQuote[index]
		if canSplit && char != '\n' {
			if index > 0 && (runes[index-1] == ':' || runes[index-1] == '：') {
				canSplit = false
			}
			if index < len(runes)-1 && (runes[index+1] == ':' || runes[index+1] == '：') {
				canSplit = false
			}
			if canSplit && char == ' ' && index > 0 && index < len(runes)-1 {
				prev, next := runes[index-1], runes[index+1]
				if prev == '-' || prev == '—' || next == '-' || next == '—' {
					canSplit = false
				} else if splitAlnum(prev) && splitAlnum(next) {
					canSplit = false
				}
			}
		}
		if !canSplit {
			current.WriteRune(char)
			continue
		}
		if current.Len() > 0 {
			segments = append(segments, Segment{Text: current.String(), Separator: string(char)})
		} else if char == ' ' || char == '\n' {
			segments = append(segments, Segment{Separator: string(char)})
		}
		current.Reset()
	}
	if current.Len() > 0 {
		segments = append(segments, Segment{Text: current.String()})
	}
	if len(segments) == 0 {
		if text == "" {
			return nil
		}
		return []Segment{{Text: text}}
	}

	splitStrength := 0.7
	switch {
	case len(runes) < 12:
		splitStrength = 0.2
	case len(runes) < 32:
		splitStrength = 0.6
	}
	mergeProbability := 1 - splitStrength

	var merged []Segment
	for index := 0; index < len(segments); {
		segment := segments[index]
		if index+1 < len(segments) && segment.Text != "" && segment.Separator != "\n" && random() < mergeProbability {
			next := segments[index+1]
			if next.Text != "" {
				merged = append(merged, Segment{Text: segment.Text + segment.Separator + next.Text, Separator: next.Separator})
			} else {
				merged = append(merged, Segment{Text: segment.Text, Separator: next.Separator})
			}
			index += 2
			continue
		}
		merged = append(merged, segment)
		index++
	}

	out := merged[:0]
	for _, segment := range merged {
		if strings.TrimSpace(segment.Text) == "" {
			continue
		}
		segment.Text = strings.TrimSpace(splitInlineNewlines.ReplaceAllString(segment.Text, " "))
		if segment.Text != "" {
			out = append(out, segment)
		}
	}
	return out
}

func splitAlnum(r rune) bool {
	return unicode.IsDigit(r) || ('a' <= unicode.ToLower(r) && unicode.ToLower(r) <= 'z')
}
