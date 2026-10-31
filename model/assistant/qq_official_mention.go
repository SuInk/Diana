// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strconv"
	"strings"
)

// QQ 官方群里 @ 成员只认 Markdown 消息里的 <qqbot-at-user id="member_openid" />，
// 纯文本消息没有提及写法（旧的 <@openid> 文档已标为弃用）。之前提及标记走通用的
// platformOutboundText，渲染成「@昵称」文本；而官方群消息基本不带昵称，于是退回
// 「@<id>」，把一串只在本群有效、谁也认不出的 32 位十六进制 openid 发进了群里。
//
// 现在群消息里的提及先换成占位符，等 Markdown 降级和补空行做完再换成原生标签，
// 避免标签被当成正文加工；纯文本（私聊、频道、Markdown 被拒后的降级重发）里查得到
// 昵称就写「@昵称」，查不到就整个去掉，不再显示 openid。

const (
	qqMentionPlaceholderOpen  = ""
	qqMentionPlaceholderClose = ""
)

// qqOfficialPlainText 把出站正文压成纯文本，提及只留查得到的昵称。
func qqOfficialPlainText(msg OutgoingMessage) string {
	text := dianaMentionMarkerPattern.ReplaceAllStringFunc(msg.Text, func(marker string) string {
		id := dianaMentionMarkerPattern.FindStringSubmatch(marker)[1]
		if name := strings.TrimSpace(msg.MentionNames[id]); name != "" {
			return "@" + name
		}
		return ""
	})
	return strings.TrimSpace(text)
}

// qqOfficialMentionPlaceholders 把提及标记换成占位符，返回被提及的 openid，
// 顺序与占位符编号一致。没有提及时原样返回、ids 为空。
func qqOfficialMentionPlaceholders(text string) (string, []string) {
	var ids []string
	text = dianaMentionMarkerPattern.ReplaceAllStringFunc(text, func(marker string) string {
		ids = append(ids, dianaMentionMarkerPattern.FindStringSubmatch(marker)[1])
		return qqMentionPlaceholderOpen + strconv.Itoa(len(ids)-1) + qqMentionPlaceholderClose
	})
	return strings.TrimSpace(text), ids
}

// qqOfficialRestoreMentions 把占位符换回 QQ 官方 Markdown 的 @ 标签。
func qqOfficialRestoreMentions(content string, ids []string) string {
	for i, id := range ids {
		placeholder := qqMentionPlaceholderOpen + strconv.Itoa(i) + qqMentionPlaceholderClose
		content = strings.ReplaceAll(content, placeholder, `<qqbot-at-user id="`+id+`" />`)
	}
	return content
}

// qqOfficialDropLeadingSenderMention 去掉回复开头对当前发言者的 @。
//
// 官方群里的 @ 渲染成蓝字，却点不开对方资料；被动回复本身已经挂在对方那条消息
// 下面，群里人多时运行时还会自动挂引用卡片。开头再 @ 一次只是多一个名字，读着生硬。
// 提示词已经让模型别写，这里兜住模型照写的情况。只动开头那一个：正文中间点名
// 别人、或者 @ 的不是发言者本人，都是有意的，原样保留。
func qqOfficialDropLeadingSenderMention(text, senderID string) string {
	senderID = strings.TrimSpace(senderID)
	if senderID == "" {
		return text
	}
	quote := ""
	rest := text
	if _, body, ok := extractOutgoingReplyMarker(strings.TrimLeft(text, " \t")); ok {
		trimmed := strings.TrimLeft(text, " \t")
		quote = trimmed[:len(trimmed)-len(body)]
		rest = body
	}
	trimmed := strings.TrimLeft(rest, " \t")
	match := dianaMentionMarkerPattern.FindStringSubmatchIndex(trimmed)
	if match == nil || match[0] != 0 || trimmed[match[2]:match[3]] != senderID {
		return text
	}
	body := strings.TrimLeft(trimmed[match[1]:], " \t")
	if strings.TrimSpace(body) == "" {
		return text
	}
	return quote + body
}
