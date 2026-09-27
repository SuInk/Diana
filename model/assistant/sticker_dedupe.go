// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"strings"
	"unicode"
)

// 同一张表情包常被转存、重新压缩，或者同一个模板换几个字再发一遍，文件哈希各不相同。
// 按内容判重：看图上原文和简介。阈值取自线上表情包库的实测（约 250 条有简介的样本）：
//   - 原文完全相同几乎都是同一张图的不同副本；
//   - 原文里连续 4 个以上汉字相同，命中的只有「同模板换字」那类（「代码在自己上传」/「代码在自己冒出来」）；
//     放宽到英文字母会把 Sleep 和 deepsleep 这种不相干的图并在一起；
//   - 简介双字重合度 0.35 以上都是同一张图描述了两次，0.30 那一档开始混进完全不同的图。
//
// 不按平台表情名判：不同表情包重名（「无语」「思考」）很常见。
const (
	stickerDuplicateMinSharedHan = 4
	stickerDuplicateMinOverlap   = 0.35
	stickerDuplicateMinDescRunes = 20
)

var stickerQuotedText = regexp.MustCompile(`[“"「『]([^”"」』]{2,60})[”"」』]`)

type stickerSignature struct {
	quotes []string
	grams  map[string]struct{}
}

func newStickerSignature(candidate stickerCandidate) stickerSignature {
	signature := stickerSignature{}
	for _, match := range stickerQuotedText.FindAllStringSubmatch(candidate.Description, -1) {
		if quote := stickerCompactText(match[1]); len([]rune(quote)) >= 2 {
			signature.quotes = append(signature.quotes, quote)
		}
	}
	if compact := []rune(stickerCompactText(candidate.Description)); len(compact) >= stickerDuplicateMinDescRunes {
		signature.grams = make(map[string]struct{}, len(compact))
		for index := 0; index+2 <= len(compact); index++ {
			signature.grams[string(compact[index:index+2])] = struct{}{}
		}
	}
	return signature
}

// stickerCompactText 去掉空白和标点，只留文字本身来比较。
func stickerCompactText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}

// duplicates 判断两张表情包是不是同一张（或同一模板换了字）。
func (a stickerSignature) duplicates(b stickerSignature) bool {
	for _, left := range a.quotes {
		for _, right := range b.quotes {
			if left == right || longestSharedHanRun(left, right) >= stickerDuplicateMinSharedHan {
				return true
			}
		}
	}
	if len(a.grams) == 0 || len(b.grams) == 0 {
		return false
	}
	shared := 0
	for gram := range a.grams {
		if _, ok := b.grams[gram]; ok {
			shared++
		}
	}
	return float64(shared)/float64(len(a.grams)+len(b.grams)-shared) >= stickerDuplicateMinOverlap
}

// longestSharedHanRun 返回两段文字最长公共子串里连续汉字的个数。
func longestSharedHanRun(left, right string) int {
	a, b := []rune(left), []rune(right)
	best := 0
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] && unicode.Is(unicode.Han, a[i-1]) {
				current[j] = previous[j-1] + 1
				best = max(best, current[j])
			} else {
				current[j] = 0
			}
		}
		previous, current = current, previous
	}
	return best
}
