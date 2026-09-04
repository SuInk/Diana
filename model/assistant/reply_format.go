// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
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
	// dianaMarkerLabelPattern 认出 Diana 自己的方括号标记，它们不参与 Markdown 降级。
	dianaMarkerLabelPattern = regexp.MustCompile(`^(?:diana-at|diana-reply|回复):`)
	mdBulletPattern         = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+`)
	mdQuotePattern          = regexp.MustCompile(`(?m)^\s*>\s?`)
	mdRulePattern           = regexp.MustCompile(`(?m)^\s*(?:-{3,}|\*{3,}|_{3,})\s*$\n?`)
	mdExtraBlankPattern     = regexp.MustCompile(`\n{3,}`)
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
		// Diana 自己的标记不是链接标签。[diana-at:10002]（后面正好跟个半角括号）
		// 会被这条规则当成 [文字](目标) 拆掉方括号，标记就废了——出站时认不出来，
		// 字面量直接发进群。
		if dianaMarkerLabelPattern.MatchString(parts[1]) {
			return match
		}
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

// markdownToPlainForConfig 决定这台机器人要不要把 Markdown 降级成纯文本。
//
// 默认值随平台走：能渲染富文本的平台（目前只有 Telegram，它的适配器会把 Markdown
// 转成 entities）默认保留标记，其余平台默认降级——QQ、钉钉、飞书、企业微信的出站
// 都是纯文本消息类型，留着 ** 和 # 只会以字面量漏进聊天窗口。
//
// 用户在配置里显式设过就以设置为准：有人偏好在 Telegram 里也看纯文本，也有人自建的
// OneBot 客户端能渲染 Markdown，这两种都得让得出来。
func markdownToPlainForConfig(cfg BotConfig) bool {
	if cfg.MarkdownToPlain != nil {
		return *cfg.MarkdownToPlain
	}
	return !PlatformSupportsRichText(cfg.Platform)
}

// platformOutputRulesForConfig 只描述当前机器人实际采用的出站格式，避免把另一平台
// 的限制塞进模型上下文。自定义纯文本规则仍然保留，但只在本轮确实会降级时注入。
func platformOutputRulesForConfig(cfg BotConfig) string {
	if markdownToPlainForConfig(cfg) {
		return strings.TrimSpace(cfg.PromptPlaintextRulesText)
	}
	def, ok := PlatformByID(cfg.Platform)
	if ok && def.RichText {
		return fmt.Sprintf("当前聊天平台是 %s，出站适配器会把支持的 Markdown 转成平台富文本；可以自然使用加粗、斜体、标题、列表、链接和代码等格式，具体不兼容项会由适配器自动降级。不要声称当前窗口不支持 Markdown。", def.Name)
	}
	name := strings.TrimSpace(cfg.Platform)
	if ok {
		name = def.Name
	}
	if name == "" {
		name = "当前平台"
	}
	return fmt.Sprintf("当前聊天平台是 %s，当前配置会保留 Markdown 原始标记并交给下游客户端处理；不要把其他平台的 Markdown 规则套到这里。", name)
}
