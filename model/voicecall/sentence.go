// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package voicecall

import (
	"regexp"
	"strings"
	"unicode"
)

// 逐句合成的切分规则：一句一合成，第一句越早念出来越好，所以按句末标点切；但一个
// 「嗯。」也单独请求一次合成不划算，太短的句子并到下一句。一句太长时在逗号处再切，
// 免得用户等整段合成完才听到声音。
const (
	minSentenceRunes = 6
	maxSentenceRunes = 80
)

// SentenceSplitter 把陆续到来的回复文字切成可以逐句合成的句子。
type SentenceSplitter struct {
	buffer []rune
}

// Push 收一段文字，返回已经完整的句子，剩下的留着等后文。
func (s *SentenceSplitter) Push(text string) []string {
	s.buffer = append(s.buffer, []rune(text)...)
	var out []string
	for {
		cut := s.nextCut()
		if cut <= 0 {
			break
		}
		sentence := strings.TrimSpace(string(s.buffer[:cut]))
		s.buffer = s.buffer[cut:]
		if sentence != "" {
			out = append(out, sentence)
		}
	}
	return out
}

// Flush 交出剩下的文字。回复说完时调用。
func (s *SentenceSplitter) Flush() []string {
	rest := strings.TrimSpace(string(s.buffer))
	s.buffer = nil
	if rest == "" {
		return nil
	}
	return []string{rest}
}

// nextCut 找下一个切点（切点之前是一句）；没有可切的返回 0。
func (s *SentenceSplitter) nextCut() int {
	lastComma := 0
	for i, r := range s.buffer {
		end := 0
		switch {
		case isSentenceEnd(r):
			end = i + 1
			// 「？！」「……」「。」」这类连在一起的收尾标点算进同一句。
			for end < len(s.buffer) && (isSentenceEnd(s.buffer[end]) || isClosingMark(s.buffer[end])) {
				end++
			}
			if end == len(s.buffer) && r == '.' {
				// 英文句点后面还没来字，可能是小数点或缩写，等后文。
				return 0
			}
			if r == '.' && !unicode.IsSpace(s.buffer[end]) {
				continue
			}
		case r == '\n':
			end = i + 1
		case isSoftBreak(r):
			lastComma = i + 1
		}
		if end > 0 {
			if countSpeakable(s.buffer[:end]) < minSentenceRunes {
				continue
			}
			return end
		}
		if i+1 >= maxSentenceRunes && lastComma > 0 {
			return lastComma
		}
	}
	return 0
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '。', '！', '？', '!', '?', '；', ';', '…', '.':
		return true
	}
	return false
}

func isClosingMark(r rune) bool {
	switch r {
	case '”', '’', '"', '\'', '」', '』', '）', ')', '~', '～':
		return true
	}
	return false
}

func isSoftBreak(r rune) bool {
	switch r {
	case '，', ',', '、', '：', ':':
		return true
	}
	return false
}

func countSpeakable(runes []rune) int {
	count := 0
	for _, r := range runes {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			count++
		}
	}
	return count
}

var (
	markdownLinkPattern = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	bareURLPattern      = regexp.MustCompile(`https?://\S+`)
	markdownMarkPattern = regexp.MustCompile("[*_#`>|]+")
	spacePattern        = regexp.MustCompile(`\s+`)
)

// SpeakableText 去掉念出来没意义的东西：Markdown 记号、链接地址。链接文字保留，
// 裸链接换成「链接」两个字，文字回复里照样看得到原文。
func SpeakableText(text string) string {
	text = markdownLinkPattern.ReplaceAllString(text, "$1")
	text = bareURLPattern.ReplaceAllString(text, "链接")
	text = markdownMarkPattern.ReplaceAllString(text, "")
	text = spacePattern.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}
