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
