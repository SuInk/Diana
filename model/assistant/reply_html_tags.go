// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"strings"
)

// 模型偶尔用 HTML 行内标签代替 Markdown：生产 OneBot 发出过「…来做<br>扩展你随便加…」，
// gpt-6.1-terra 回放在 QQ 官方机器人上写过「算不出我要花多少<s>钱</s>」。三个平台都不
// 渲染 HTML，标签会原样发出去。这里只认没有属性、成对出现在同一行里的几种常见标签，
// 富文本平台换成对应的 Markdown 交给适配器按能力渲染或降级，纯文本平台只留文字；代码块
// 和行内代码里的标签是内容，不动。
var (
	htmlCodeSpanPattern  = regexp.MustCompile("(?s)```.*?```|`[^`\n]+`")
	htmlBreakPattern     = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlInlineTagPattern = regexp.MustCompile(`(?i)<(b|strong|i|em|s|del|strike|u|code)>([^<>\n]+?)</(b|strong|i|em|s|del|strike|u|code)>`)
)

func htmlInlineTagsToMarkdown(text string, plain bool) string {
	if !strings.Contains(text, "<") {
		return text
	}
	var out strings.Builder
	last := 0
	for _, span := range htmlCodeSpanPattern.FindAllStringIndex(text, -1) {
		out.WriteString(convertHTMLInlineTags(text[last:span[0]], plain))
		out.WriteString(text[span[0]:span[1]])
		last = span[1]
	}
	out.WriteString(convertHTMLInlineTags(text[last:], plain))
	return out.String()
}

func convertHTMLInlineTags(text string, plain bool) string {
	text = htmlBreakPattern.ReplaceAllString(text, "\n")
	return htmlInlineTagPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := htmlInlineTagPattern.FindStringSubmatch(match)
		open, body, closing := strings.ToLower(parts[1]), parts[2], strings.ToLower(parts[3])
		if open != closing {
			return match
		}
		if plain {
			return body
		}
		switch open {
		case "b", "strong":
			return "**" + body + "**"
		case "i", "em":
			return "*" + body + "*"
		case "s", "del", "strike":
			return "~~" + body + "~~"
		case "code":
			return "`" + body + "`"
		default:
			return body
		}
	})
}
