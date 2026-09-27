// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package vrchat

import (
	"strings"
	"unicode"
)

const (
	// ChatboxMaxRunes / ChatboxMaxLines 是 VRChat 聊天框的硬上限，超出的部分
	// 客户端直接截掉，所以长文必须自己分段。
	ChatboxMaxRunes = 144
	ChatboxMaxLines = 9
	// chatboxMinBreak 是找断点时最靠前能接受的位置：断得太早会切出一堆一两个字
	// 的碎段，每段又要占一个限速间隔。
	chatboxMinBreak = ChatboxMaxRunes / 3
)

// SplitChatbox 把一段文字切成若干能放进 VRChat 聊天框的段。
//
// 优先在换行处断，其次句末标点，再次逗号和空白，实在找不到才硬切，这样每段
// 单独显示时读起来仍然完整。
func SplitChatbox(text string) []string {
	text = normalizeChatboxText(text)
	if text == "" {
		return nil
	}
	runes := []rune(text)
	var out []string
	for len(runes) > 0 {
		limit := chatboxWindow(runes)
		if limit >= len(runes) {
			if segment := strings.TrimSpace(string(runes)); segment != "" {
				out = append(out, segment)
			}
			break
		}
		cut := chatboxBreak(runes[:limit])
		if segment := strings.TrimSpace(string(runes[:cut])); segment != "" {
			out = append(out, segment)
		}
		runes = []rune(strings.TrimLeftFunc(string(runes[cut:]), unicode.IsSpace))
	}
	return out
}

// chatboxWindow 返回这一段最多能放多少个字：字数和行数两个上限取先到的那个。
func chatboxWindow(runes []rune) int {
	limit := min(len(runes), ChatboxMaxRunes)
	lines := 1
	for i := range limit {
		if runes[i] != '\n' {
			continue
		}
		lines++
		if lines > ChatboxMaxLines {
			return i
		}
	}
	return limit
}

func chatboxBreak(window []rune) int {
	tiers := []string{"\n", "。！？!?…；;", "，,、：: "}
	for _, marks := range tiers {
		for i := len(window) - 1; i >= chatboxMinBreak; i-- {
			if strings.ContainsRune(marks, window[i]) {
				return i + 1
			}
		}
	}
	return len(window)
}

// normalizeChatboxText 去掉控制字符并压缩空行：聊天框只有九行，空行是浪费。
func normalizeChatboxText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var builder strings.Builder
	newlines := 0
	for _, r := range strings.TrimSpace(text) {
		if r == '\n' {
			newlines++
			if newlines > 1 {
				continue
			}
			builder.WriteRune(r)
			continue
		}
		newlines = 0
		if r == '\t' {
			r = ' '
		}
		if unicode.IsControl(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
