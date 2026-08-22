// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// AliasTriggerMode 描述群聊触发称呼的匹配松紧。
//
// 裸子串匹配只能回答「这条消息里有没有出现这个称呼」，但群里出现称呼有两种截然
// 不同的情况：叫它（「diana 帮我看看」）和谈论它（「diana 刚才那句话好怪」）。
// 后者按裸匹配一样会触发，机器人就凑进了本来没它的对话里。
//
// 这里只做结构判断：词边界、是否被引号整个括起来、是否处在呼语位置。区分「叫它」
// 和「谈论它」是语义问题，以前靠三张中文词表在代码里判（「diana 的/说的/刚才」算
// 谈论，「diana 帮/你/看看」算呼叫），本项目不允许用关键词判断意图，那段判断已经
// 删除。代价是谈论它的消息会重新触发回复；要恢复这层区分，正确做法是让主动回复
// 路由来判——它的输入里本来就有 bot_aliases 和完整上下文，并会给出 directed_at_bot。
type AliasTriggerMode string

const (
	// AliasTriggerLoose 出现即触发，连被引号整个括起来的引述也算。
	AliasTriggerLoose AliasTriggerMode = "loose"
	// AliasTriggerSmart 出现即触发，但被引号整个括起来时视为引述这个词本身，不触发。
	AliasTriggerSmart AliasTriggerMode = "smart"
	// AliasTriggerStrict 还要求称呼处在呼语位置：一侧只剩标点或空白。
	AliasTriggerStrict AliasTriggerMode = "strict"
)

// defaultAliasTriggerMode 默认取 smart：宁可多回一条，也不要漏掉真正在叫它的人。
const defaultAliasTriggerMode = AliasTriggerSmart

// normalizeAliasTriggerMode 归一化匹配模式，未识别的值一律回落到默认档。
func normalizeAliasTriggerMode(mode AliasTriggerMode) AliasTriggerMode {
	switch AliasTriggerMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case AliasTriggerLoose:
		return AliasTriggerLoose
	case AliasTriggerSmart:
		return AliasTriggerSmart
	case AliasTriggerStrict:
		return AliasTriggerStrict
	}
	return defaultAliasTriggerMode
}

// AliasTriggerModes 返回可选档位，供 WebUI 展示。
func AliasTriggerModes() []AliasTriggerMode {
	return []AliasTriggerMode{AliasTriggerLoose, AliasTriggerSmart, AliasTriggerStrict}
}

// aliasTriggerMode 返回本次生效的匹配模式，未归一化的配置也能安全读取。
func aliasTriggerMode(cfg BotConfig) AliasTriggerMode {
	return normalizeAliasTriggerMode(cfg.GroupTriggerMode)
}

// aliasQuotePairs 是把称呼整个括起来时的引号对，括起来通常是在引述这个词本身。
var aliasQuotePairs = map[rune]rune{
	'“':  '”',
	'「':  '」',
	'『':  '』',
	'《':  '》',
	'"':  '"',
	'\'': '\'',
}

// matchedAliasesInText 返回按当前模式判定为「在叫机器人」的称呼。
func matchedAliasesInText(text string, aliases []string, mode AliasTriggerMode) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	mode = normalizeAliasTriggerMode(mode)
	matched := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if aliasTriggers(text, alias, mode) {
			matched = appendUniqueStrings(matched, alias)
		}
	}
	return matched
}

// aliasTriggers 判断称呼在这条消息里的任意一次出现是否构成呼叫。
func aliasTriggers(text, alias string, mode AliasTriggerMode) bool {
	for offset := 0; ; {
		index := strings.Index(text[offset:], alias)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(alias)
		offset = start + 1
		before, after := text[:start], text[end:]
		if !aliasHasWordBoundary(alias, before, after) {
			continue
		}
		if mode == AliasTriggerLoose {
			return true
		}
		if aliasIsQuoted(before, after) {
			continue
		}
		if mode == AliasTriggerStrict && !aliasIsVocative(before, after) {
			continue
		}
		return true
	}
}

// aliasHasWordBoundary 拦住 ASCII 称呼粘在更长单词或网址里的情况，例如 diana
// 不应该被 dianabc 或 example.com/diana2 命中。非 ASCII 称呼没有词边界概念。
func aliasHasWordBoundary(alias, before, after string) bool {
	if !isASCIIWord(alias) {
		return true
	}
	if last, size := utf8.DecodeLastRuneInString(before); size > 0 && isASCIIWordRune(last) {
		return false
	}
	if next, size := utf8.DecodeRuneInString(after); size > 0 && isASCIIWordRune(next) {
		return false
	}
	return true
}

// aliasIsQuoted 报告这次出现是否被引号整个括起来。
func aliasIsQuoted(before, after string) bool {
	open, size := utf8.DecodeLastRuneInString(before)
	if size == 0 {
		return false
	}
	closing, ok := aliasQuotePairs[open]
	if !ok {
		return false
	}
	next, size := utf8.DecodeRuneInString(after)
	return size > 0 && next == closing
}

// aliasIsVocative 报告这次出现是否处在呼语位置：句首或句尾，两侧只有标点或空白。
func aliasIsVocative(before, after string) bool {
	return aliasBoundaryIsBlank(before) || aliasBoundaryIsBlank(after)
}

// aliasBoundaryIsBlank 报告一侧是否只剩标点、空白或 @ 段落残留。
func aliasBoundaryIsBlank(side string) bool {
	for _, char := range strings.TrimSpace(side) {
		if unicode.IsSpace(char) || unicode.IsPunct(char) || unicode.IsSymbol(char) {
			continue
		}
		return false
	}
	return true
}

func isASCIIWord(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !isASCIIWordRune(char) {
			return false
		}
	}
	return true
}

func isASCIIWordRune(char rune) bool {
	return char < utf8.RuneSelf && (unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_')
}
