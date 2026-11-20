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
	splitSeparators      = map[rune]bool{'，': true, ',': true, ' ': true, '。': true, ';': true, '\n': true}
)

// Merge 把断出来的句子合到最多 MaxSplitNum 条。segments 可以来自好几段
// SplitSentences：条数上限管的是整条回复，不是其中一行。
//
// MaiBot 断出来超过 max_sentence_num 句时不拆（默认还会把原文换成一句默认回复），
// 因为它的提示词把回复压在一两句。Diana 的回复长得多，照那条规则长回复全成了一整块，
// 所以句子再多也合到三条。原文里有换行时，条与条的分界尽量落在换行处，不从一行
// 中间断开；没有换行就照 MaiBot 按句数均分。
func Merge(segments []Segment) []string {
	merged := MergeToMaxCount(segments, MaxSplitNum)
	if cuts := lineBreakCuts(segments, MaxSplitNum); cuts != nil {
		merged = mergeAtCuts(segments, cuts)
	}
	out := make([]string, 0, len(merged))
	for _, segment := range merged {
		if text := strings.TrimSpace(segment.Text); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// lineBreakCuts 在换行处挑至多 maxCount-1 个分界（分界 i 表示第 i 句之后断开），
// 挑离按字数均分的位置最近的那几个。没有换行、或句子本来就不超过 maxCount 时返回 nil。
func lineBreakCuts(segments []Segment, maxCount int) []int {
	if len(segments) <= maxCount || maxCount <= 1 {
		return nil
	}
	ends := make([]int, len(segments)) // ends[i] 是到第 i 句为止的累计字数
	var candidates []int
	total := 0
	for index, segment := range segments {
		total += len([]rune(segment.Text))
		ends[index] = total
		if index < len(segments)-1 && segment.Separator == "\n" {
			candidates = append(candidates, index)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) <= maxCount-1 {
		return candidates
	}
	var cuts []int
	next := 0
	for part := 1; part < maxCount; part++ {
		target := total * part / maxCount
		best := -1
		// 后面还要留够候选给剩下的分界。
		for pos := next; pos <= len(candidates)-(maxCount-part); pos++ {
			if best < 0 || abs(ends[candidates[pos]]-target) < abs(ends[candidates[best]]-target) {
				best = pos
			}
		}
		cuts = append(cuts, candidates[best])
		next = best + 1
	}
	return cuts
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

func abs(value int) int {
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

// MergeToMaxCount 把句子均匀分成至多 maxCount 组，组内补回句间分隔符。
func MergeToMaxCount(segments []Segment, maxCount int) []Segment {
	if len(segments) <= maxCount {
		return segments
	}
	if maxCount <= 0 {
		return nil
	}
	var merged []Segment
	start := 0
	for group := 0; group < maxCount; group++ {
		remaining, groups := len(segments)-start, maxCount-group
		size := (remaining + groups - 1) / groups
		end := start + size
		var text strings.Builder
		for index := start; index < end; index++ {
			text.WriteString(segments[index].Text)
			if index < end-1 {
				text.WriteString(segments[index].Separator)
			}
		}
		merged = append(merged, Segment{Text: text.String(), Separator: segments[end-1].Separator})
		start = end
	}
	return merged
}

func splitAlnum(r rune) bool {
	return unicode.IsDigit(r) || ('a' <= unicode.ToLower(r) && unicode.ToLower(r) <= 'z')
}
