// SPDX-License-Identifier: GPL-3.0-only
// Go adaptation of Mai-with-u/MaiBot replyer at
// f9ec3670a3638d6a4708ee5253f7ffe2838c9415.
// See NOTICE.md and LICENSE in this directory.

// Package maibotreply ports MaiBot's text reply composition, not its planner,
// database, model client or delivery service. It has no runtime dependencies.
package maibotreply

import (
	"strings"
	"time"
)

const Revision = "f9ec3670a3638d6a4708ee5253f7ffe2838c9415"

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Target struct {
	ID, Sender, Content, BotName string
	Self                         bool
	QuoteIDs                     []string
}

type Input struct {
	Identity, Style, Attention, OutputInstruction                  string
	History                                                        []Message
	Target                                                         *Target
	Time                                                           time.Time
	Expressions, TemporaryStyle, Reason, Reference, RequestedStyle string
	Requirements, KeywordReaction, Attachments, DuplicateReminder  string
	FinalInstruction                                               string
}

func SystemPrompt(in Input) string {
	output := in.OutputInstruction
	if output == "" {
		output = "请注意不要输出多余内容(包括不必要的前后缀，冒号，括号，表情包，@等 )，只输出发言内容就好。"
	}
	return in.Identity + "\n现在请你读读之前的聊天记录，把握当前的话题，然后给出日常且口语化的回复，\n" + in.Style + "\n你可以参考【回复信息参考】中的信息，但是视情况而定，不用完全遵守。\n" + in.Attention + "\n" + output + "\n"
}

func normalize(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return text
}

func GuidedReply(text string) string { return normalize(text, 500) }

func TargetBlock(target *Target) string {
	if target == nil {
		return ""
	}
	id := target.ID
	if id == "" {
		id = "未知"
	}
	content := normalize(target.Content, 300)
	if content == "" {
		content = "[无可见文本内容]"
	}
	if target.Self {
		name := strings.TrimSpace(target.BotName)
		if name == "" {
			name = target.Sender
		}
		return "你想要补充说明你自己（" + name + "） 发送的 msg_id为 " + id + " 的消息，你可以在这条目标消息的基础上补充发言，不要把你自己的发言当成别人的发言。\n- 你之前的发言内容：" + content
	}
	lines := []string{"你想要回复的消息是 " + target.Sender + " 发送的 msg_id为 " + id + " 的消息，你这次要回复的就是这条目标消息，不要把其他历史消息当成当前回复对象。"}
	if len(target.QuoteIDs) > 0 {
		lines = append(lines, "- quote="+strings.Join(target.QuoteIDs, ","))
	}
	return strings.Join(append(lines, "- 发言内容："+content), "\n")
}

func Reference(reason, reference string) string {
	if reference = strings.TrimSpace(reference); reference != "" {
		return reference
	}
	return strings.TrimSpace(reason)
}

func RequestedStyle(style string) string {
	switch strings.TrimSpace(style) {
	case "简短表达":
		return "请简短的回复，允许句子残缺，奇怪表达，倒装，省略，符合口语习惯，符合省力随意回复习惯"
	case "长回复":
		return "可以针对问题做出较为详细的评论和说明"
	default:
		return ""
	}
}

func FinalUserMessage(in Input) string {
	sections := []string{"当前时间：" + in.Time.Format("2006-01-02 15:04:05")}
	for _, value := range []string{TargetBlock(in.Target), in.DuplicateReminder, in.Requirements, in.KeywordReaction} {
		if value = strings.TrimSpace(value); value != "" {
			sections = append(sections, value)
		}
	}
	if attachment := strings.TrimSpace(in.Attachments); attachment != "" {
		sections = append(sections, "【额外发送内容参考】\n"+attachment)
	}
	instruction := in.FinalInstruction
	if instruction == "" {
		instruction = "请自然地回复。不要输出多余说明、括号、@ 或额外标记，只输出实际要发言的内容。"
	}
	return strings.Join(append(sections, instruction), "\n\n")
}

func Compose(in Input) []Message {
	items := make([]Message, 0, len(in.History)+7)
	items = append(items, Message{"system", SystemPrompt(in)})
	items = append(items, in.History...)
	appendUser := func(text string) {
		if text = strings.TrimSpace(text); text != "" {
			items = append(items, Message{"user", text})
		}
	}
	appendUser(in.Expressions)
	if style := strings.TrimSpace(in.TemporaryStyle); style != "" {
		appendUser("你的说话风格可以尝试：\n" + style)
	}
	appendUser(Reference(in.Reason, in.Reference))
	appendUser(FinalUserMessage(in))
	appendUser(RequestedStyle(in.RequestedStyle))
	return items
}
