// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"strings"
)

// OneBot v11 文本消息不渲染 Markdown，模型输出里的标记会原样显示成星号井号。
// 这里在发送前把常见 Markdown 语法降级成纯文本；未来接入支持富文本的平台
//（如 Telegram）时应按平台能力跳过这层转换。

var (
	mdCodeFencePattern  = regexp.MustCompile("(?m)^\\s*```[^\\n]*$\\n?")
	mdInlineCodePattern = regexp.MustCompile("`{1,2}([^`\n]+)`{1,2}")
	mdBoldPattern       = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	mdHeadingPattern    = regexp.MustCompile(`(?m)^\s*#{1,6}\s+`)
	mdLinkPattern       = regexp.MustCompile(`!?\[([^\]\n]*)\]\(([^)\n]+)\)`)
	mdBulletPattern     = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+`)
	mdQuotePattern      = regexp.MustCompile(`(?m)^\s*>\s?`)
	mdRulePattern       = regexp.MustCompile(`(?m)^\s*(?:-{3,}|\*{3,}|_{3,})\s*$\n?`)
	mdExtraBlankPattern = regexp.MustCompile(`\n{3,}`)
)

// markdownToPlain 把 Markdown 标记降级为可读的纯文本，保留 <dianabr> 分段标记。
func markdownToPlain(text string) string {
	if !strings.ContainsAny(text, "*#`[]_->") {
		return text
	}
	// 分隔线要在列表符号之前处理，避免 "---" 被当成列表项。
	text = mdRulePattern.ReplaceAllString(text, "")
	// 代码围栏只去掉 ``` 行本身，代码内容原样保留。
	text = mdCodeFencePattern.ReplaceAllString(text, "")
	text = mdInlineCodePattern.ReplaceAllString(text, "$1")
	text = mdBoldPattern.ReplaceAllString(text, "$1$2")
	text = mdHeadingPattern.ReplaceAllString(text, "")
	text = mdLinkPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := mdLinkPattern.FindStringSubmatch(match)
		label := strings.TrimSpace(parts[1])
		target := strings.TrimSpace(parts[2])
		if label == "" || label == target {
			return target
		}
		return label + " (" + target + ")"
	})
	text = mdBulletPattern.ReplaceAllString(text, "$1• ")
	text = mdQuotePattern.ReplaceAllString(text, "")
	text = mdExtraBlankPattern.ReplaceAllString(text, "\n\n")
	return text
}
