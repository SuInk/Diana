// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// 聊天消息结尾不挂文字版表情包。
//
// 模型学着群友在句末补一个「（doge」「[狗头]」「/滑稽」，本意是缓和语气，发到 QQ
// 里却只是一串字：它不是表情，括号还常常没闭合，读起来像话没说完。真要配表情，
// 表情包工具会发真的图。和句号一样在程序里去掉，不押在模型听不听话上。
//
// 只认行尾、带括号或斜杠前缀、名字在下面清单里的那种：正文里讨论「doge 是什么」、
// 行中间的「（狗头）」都不动；带代码块的整条回复也不动。

// textStickerNames 是常见的文字版表情名，按小写比较。
var textStickerNames = map[string]bool{
	"doge": true, "狗头": true, "狗头保命": true, "手动狗头": true, "滑稽": true, "斜眼笑": true,
	"吃瓜": true, "捂脸": true, "笑哭": true, "旺柴": true, "二哈": true, "阴险": true,
	"奸笑": true, "坏笑": true, "偷笑": true, "流汗": true, "允悲": true, "机智": true,
}

var textStickerOpeners = map[rune]rune{'（': '）', '(': ')', '[': ']', '【': '】', '/': 0}

// stripBubbleTextStickers 对切好的每条消息去掉行尾的文字表情，去完变空的丢掉。
func stripBubbleTextStickers(bubbles []string) []string {
	out := bubbles[:0]
	for _, bubble := range bubbles {
		if !strings.Contains(bubble, "```") && !strings.Contains(bubble, "://") {
			lines := strings.Split(bubble, "\n")
			for index, line := range lines {
				lines[index] = stripTrailingTextStickers(line)
			}
			bubble = strings.Join(lines, "\n")
		}
		if bubble = strings.TrimSpace(bubble); bubble != "" {
			out = append(out, bubble)
		}
	}
	return out
}

// stripTrailingTextStickers 去掉一行末尾连着的文字表情，比如「……说」（doge」
// 「好吧 [狗头][狗头]」。去掉后留在行尾的逗号、空格一并收掉。
func stripTrailingTextStickers(line string) string {
	for {
		trimmed := strings.TrimRight(line, " \t")
		next, ok := cutTrailingTextSticker(trimmed)
		if !ok {
			return line
		}
		line = strings.TrimRight(next, " \t，,、")
	}
}

func cutTrailingTextSticker(line string) (string, bool) {
	runes := []rune(line)
	end := len(runes)
	if end == 0 {
		return line, false
	}
	// 从行尾往前找最近的开括号或斜杠，中间那段就是候选表情名。
	for start := end - 1; start >= 0 && end-start <= 8; start-- {
		closer, ok := textStickerOpeners[runes[start]]
		if !ok {
			continue
		}
		name := runes[start+1 : end]
		if closer != 0 && len(name) > 0 && name[len(name)-1] == closer {
			name = name[:len(name)-1]
		}
		if textStickerNames[strings.ToLower(strings.TrimSpace(string(name)))] {
			return string(runes[:start]), true
		}
		return line, false
	}
	return line, false
}
