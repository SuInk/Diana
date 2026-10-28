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
	// dianaMarkerLabelPattern 认出 Diana 自己的方括号标记，它们不参与 Markdown 降级。
	dianaMarkerLabelPattern = regexp.MustCompile(`^(?:diana-at|diana-reply|回复):|^diana-(?:msg|line)$`)
	mdBulletPattern         = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+`)
	mdQuotePattern          = regexp.MustCompile(`(?m)^\s*>\s?`)
	mdRulePattern           = regexp.MustCompile(`(?m)^\s*(?:-{3,}|\*{3,}|_{3,})\s*$\n?`)
	mdExtraBlankPattern     = regexp.MustCompile(`\n{3,}`)
)

// markdownToPlain 把 Markdown 标记降级为可读的纯文本，保留 [diana-msg] 分段标记。
func markdownToPlain(text string) string {
	explicitLayout := strings.Contains(text, notificationSplitMarker) || strings.Contains(text, notificationLineMarker)
	if explicitLayout {
		text = restoreExplicitReplyLines(text)
		text = strings.ReplaceAll(text, notificationSplitMarker, "\n"+notificationSplitMarker+"\n")
	}
	if !strings.ContainsAny(text, "*#`[]_->") {
		if explicitLayout {
			return encodeExplicitMarkdownLayout(text)
		}
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
	if explicitLayout {
		text = encodeExplicitMarkdownLayout(text)
	}
	return text
}

func encodeExplicitMarkdownLayout(text string) string {
	text = strings.ReplaceAll(text, "\n", notificationLineMarker)
	text = strings.ReplaceAll(text, notificationLineMarker+notificationSplitMarker+notificationLineMarker, notificationSplitMarker)
	for strings.HasPrefix(text, notificationLineMarker) {
		text = strings.TrimPrefix(text, notificationLineMarker)
	}
	for strings.HasSuffix(text, notificationLineMarker) {
		text = strings.TrimSuffix(text, notificationLineMarker)
	}
	return text
}

// markdownToPlainForConfig 决定这台机器人要不要把 Markdown 降级成纯文本。
//
// 默认值随平台走：出站适配器能发富文本的平台（Telegram 转 entities，QQ 官方机器人
// 发 msg_type 2）默认保留标记，其余平台默认降级——OneBot、微信、iMessage 只有纯文本，
// 留着 ** 和 # 只会以字面量漏出来。
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
	def, ok := PlatformByID(cfg.Platform)
	name := strings.TrimSpace(cfg.Platform)
	if ok {
		name = def.Name
	}
	if name == "" {
		name = "当前平台"
	}
	if markdownToPlainForConfig(cfg) {
		return strings.TrimSpace(cfg.promptf(promptPlaintextRulesSpec, map[string]string{"platform": name}))
	}
	if ok && def.RichText {
		return cfg.promptf(promptPlatformRichTextSpec, map[string]string{
			"platform": def.Name,
			"features": platformMarkdownSyntaxNote(def.ID),
		})
	}
	return cfg.promptf(promptPlatformMarkdownSpec, map[string]string{"platform": name})
}

// platformMarkdownSyntaxNote 告诉模型这个平台实际渲染得出哪些写法。不认的写法适配器
// 也会降级，但模型知道边界才会挑对排版：比如在 QQ 官方机器人里就别指望表格对齐。
func platformMarkdownSyntaxNote(platform string) string {
	switch NormalizePlatformID(platform) {
	case PlatformTelegram:
		return "加粗、斜体、删除线、行内代码、代码块、链接、引用和列表都能显示，表格会转成等宽块"
	case PlatformQQOfficial:
		return "标题、加粗、斜体、删除线、代码块、链接、引用和列表都能显示，公式、算式用 $…$ 或 $$…$$ 写 LaTeX 会排成数学公式；不认行内代码和表格，表格会转成对齐的文字行"
	default:
		return "常见 Markdown 都能显示，不兼容的写法会自动降级"
	}
}

var promptPlatformRichTextSpec = registerPrompt(PromptSpec{
	Key:     "reply.platform_rich_text",
	Group:   PromptGroupReplyBase,
	Title:   "富文本平台说明",
	Usage:   "平台能渲染富文本（如 Telegram、QQ 官方机器人）、本轮保留 Markdown 时，替代纯文本排版规则注入。",
	Default: "当前聊天平台是 {platform}，回复里的 Markdown 会渲染成富文本：{features}。讲资料、步骤、对比、清单、代码这类有结构的内容时直接用 Markdown 排版——关键结论加粗，并列要点用列表，代码放进代码块，不要为了像聊天把结构压成一长段。贴网址尽量写成 [标题](网址)，标题用页面名或一句话说明它是什么，不要裸贴长网址；几个链接并列时每个占一个列表项。闲聊和一两句话的回复照常说，不要硬加格式。不要声称当前窗口不支持 Markdown。",
	Vars: []PromptVar{
		{Name: "platform", Description: "聊天平台名称，如 Telegram"},
		{Name: "features", Description: "该平台能渲染的 Markdown 写法"},
	},
})

var promptPlatformMarkdownSpec = registerPrompt(PromptSpec{
	Key:     "reply.platform_markdown",
	Group:   PromptGroupReplyBase,
	Title:   "保留 Markdown 的平台说明",
	Usage:   "平台本身不渲染富文本、但配置里要求保留 Markdown 时，替代纯文本排版规则注入。",
	Default: "当前聊天平台是 {platform}，当前配置会保留 Markdown 原始标记并交给下游客户端处理；不要把其他平台的 Markdown 规则套到这里。",
	Vars:    []PromptVar{{Name: "platform", Description: "聊天平台名称或配置里的平台 ID"}},
})
