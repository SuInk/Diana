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
type AliasTriggerMode string

const (
	// AliasTriggerLoose 出现即触发，等于本功能加入之前的行为。
	AliasTriggerLoose AliasTriggerMode = "loose"
	// AliasTriggerSmart 出现即触发，但明显是在谈论它时放行给插话判定。
	AliasTriggerSmart AliasTriggerMode = "smart"
	// AliasTriggerStrict 只有称呼用作呼语（句首或句尾）且不是谈论时才触发。
	AliasTriggerStrict AliasTriggerMode = "strict"
)

// defaultAliasTriggerMode 默认取 smart：只丢掉有明确谈论特征的消息，其余照旧触发。
// 宁可多回一条，也不要漏掉真正在叫它的人。
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

// aliasAddressedPatterns 是紧跟称呼之后、说明这是在跟它说话的词。它们优先于
// aliasDiscussPatterns 判定，用来救回「说说」「回我」这类和谈论词共享前缀的说法。
var aliasAddressedPatterns = []string{
	"说说", "讲讲", "回我", "回答我", "回复我", "发个", "发一", "发张", "发条",
	"你", "您", "咱", "帮", "给我", "告诉我", "查一", "查查", "看看", "来一", "来个",
	"在吗", "在么", "在不在", "醒醒", "求", "能不能", "可不可以",
}

// aliasDiscussPatterns 是紧跟称呼之后、说明这条消息在谈论它而不是在叫它的词。
var aliasDiscussPatterns = []string{
	"的", "地", "得",
	"说的", "说得", "说过", "说了", "讲的", "讲过",
	"发的", "发了", "发过", "回的", "回了", "回复的", "回复了", "答的",
	"提到", "刚才", "刚刚", "之前", "上次", "昨天", "今天早",
	"又开始", "好像", "似乎", "怎么样", "是不是有", "这句", "那句", "这条", "那条",
}

// aliasDiscussPrefixes 是紧挨在称呼之前、说明这条消息在谈论它的词。
// 只收明确指向第三方的说法；「让 diana 看看」这类仍然算在叫它，不收。
var aliasDiscussPrefixes = []string{"跟", "和", "与", "关于", "像", "问过", "问了"}

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
		if aliasIsQuoted(before, after) || aliasDiscussed(before, after) {
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

// aliasDiscussed 报告这次出现的上下文是否说明消息在谈论它，而不是在叫它。
func aliasDiscussed(before, after string) bool {
	trimmedAfter := strings.TrimLeft(after, " \t　")
	if hasAnyPrefix(trimmedAfter, aliasAddressedPatterns) {
		return false
	}
	if hasAnyPrefix(trimmedAfter, aliasDiscussPatterns) {
		return true
	}
	return hasAnySuffix(strings.TrimRight(before, " \t　"), aliasDiscussPrefixes)
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

func hasAnyPrefix(text string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func hasAnySuffix(text string, suffixes []string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(text, suffix) {
			return true
		}
	}
	return false
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
