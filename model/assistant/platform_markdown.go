// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"strings"
)

// 钉钉、飞书、企业微信协议上都有富文本消息类型，但三家支持的 Markdown 子集各不相同，
// 而且都不支持表格。直接把模型原文塞进去，不支持的那部分就会以字面量露出来——和当初
// 纯文本发 ** 是同一个毛病，只是换了个地方。
//
// 所以这里按能力位降级：平台认的留着，不认的就地转换成它认得的表达（斜体转粗体、
// 代码块转普通文本、表格转等宽对齐的行）。每个适配器声明自己支持什么，转换逻辑只有一份。

type platformMarkdownFeatures struct {
	Headings   bool // # 标题
	Bold       bool // **粗体**
	Italic     bool // *斜体*
	Strike     bool // ~~删除线~~
	InlineCode bool // `行内代码`
	CodeFence  bool // ``` 代码块
	Links      bool // [文字](地址)
	Quote      bool // > 引用
	Lists      bool // - 列表
}

var (
	pmFencePattern    = regexp.MustCompile("(?s)```[A-Za-z0-9_+-]*\\n?(.*?)```")
	pmInlineCode      = regexp.MustCompile("`([^`\\n]+)`")
	pmHeadingPattern  = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]+(.+)$`)
	pmQuotePattern    = regexp.MustCompile(`(?m)^[ \t]*>[ \t]?`)
	pmLinkPattern     = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
	pmMarkdownProbe   = regexp.MustCompile("(?m)```|`[^`\\n]+`|\\*\\*[^\\n*]+\\*\\*|~~[^\\n~]+~~|^[ \t]*#{1,6}[ \t]+|^[ \t]*>[ \t]?|^[ \t]*[-*+][ \t]+|^[ \t]*\\|.*\\|[ \t]*$|\\[[^\\]\n]+\\]\\([^)\\s]+\\)")
	pmOrderedListItem = regexp.MustCompile(`(?m)^[ \t]*\d+[.、)][ \t]+`)
)

// platformTextHasMarkdown 判断这段文字里到底有没有 Markdown 结构。
//
// 没有就让适配器照旧发纯文本：绝大多数聊天消息都是大白话，为它们换一种消息类型
// 只会白白改变气泡样式、@ 行为和回复挂载方式，却什么也没渲染。
func platformTextHasMarkdown(text string) bool {
	return pmMarkdownProbe.MatchString(text)
}

// downgradeMarkdownFor 把文本降级成目标平台认得的 Markdown 子集。
func downgradeMarkdownFor(text string, features platformMarkdownFeatures) string {
	// 表格谁都不支持，先转成等宽对齐的行。平台认代码块就裹一层，让它保持等宽；
	// 不认就退化成纯文本——列还是对齐的，只是字体不一定等宽。
	text = convertMarkdownTables(text, func(body string) string {
		if features.CodeFence {
			return "```\n" + body + "\n```"
		}
		return body
	})

	if !features.CodeFence {
		// 去掉围栏但留下代码本身：代码是内容，围栏只是标记。
		text = pmFencePattern.ReplaceAllString(text, "$1")
	}
	if !features.InlineCode {
		text = pmInlineCode.ReplaceAllString(text, "$1")
	}
	if !features.Headings {
		// 标题降级成粗体，这是它在这些平台上最接近的表达；粗体也不支持就只剩正文。
		text = pmHeadingPattern.ReplaceAllStringFunc(text, func(match string) string {
			body := strings.TrimSpace(pmHeadingPattern.FindStringSubmatch(match)[1])
			if features.Bold {
				return "**" + body + "**"
			}
			return body
		})
	}
	if !features.Bold {
		text = tgBoldPattern.ReplaceAllString(text, "$1")
	}
	if !features.Italic {
		// 斜体转粗体而不是直接抹掉：模型用它表示强调，降成正文就把语气丢了。
		replacement := "$1$2$3"
		if features.Bold {
			replacement = "$1**$2**$3"
		}
		text = tgItalicStar.ReplaceAllString(text, replacement)
		text = tgItalicUnderscore.ReplaceAllString(text, replacement)
	}
	if !features.Strike {
		text = tgStrikePattern.ReplaceAllString(text, "$1")
	}
	if !features.Links {
		// 链接摊平成「文字（地址）」，否则地址就整个丢了。
		text = pmLinkPattern.ReplaceAllString(text, "$1（$2）")
	}
	if !features.Quote {
		text = pmQuotePattern.ReplaceAllString(text, "")
	}
	if !features.Lists {
		text = normalizeListLines(text)
		text = pmOrderedListItem.ReplaceAllStringFunc(text, func(match string) string {
			return strings.TrimLeft(match, " \t")
		})
	}
	return text
}
