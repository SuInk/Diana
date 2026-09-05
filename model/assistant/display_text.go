// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// maxDisplayQuotePreviewRunes 是「回复 某人：……」里原话摘要的长度上限。整条
// 记录本身也就一两百字，被引用的原文只是用来认出是哪一条。
const maxDisplayQuotePreviewRunes = 30

// DisplayEventText 把一条消息渲染成控制台上给人看的一行。
//
// 和 PlainText 的区别只在两处标记：at 段没带昵称时向 resolve 要一个，引用段渲染成
// 「回复 某人：原话」而不是 [diana-reply:数字ID]。那两种写法是给模型和出站适配器看
// 的中间产物，人翻记录时只会看见一串号码，认不出提到了谁、回的是哪条。
func DisplayEventText(event MessageEvent, resolve AtMentionNameResolver) string {
	text := DisplaySegmentsText(event.Segments, event.Quoted, resolve)
	if text == "" {
		// RawMessage 是平台原样给的串，可能还带着 CQ 码，但总比空行强。
		text = strings.Join(strings.Fields(strings.TrimSpace(event.RawMessage)), " ")
	}
	return text
}

// DisplaySegmentsText 是 DisplayEventText 的 segment 版本，给已经单独拿到 segment
// 的调用方用——事件列表会先批量把昵称写回 at 段，那时传 resolve 为 nil 即可。
func DisplaySegmentsText(segments []MessageSegment, quoted *QuotedMessage, resolve AtMentionNameResolver) string {
	text := strings.TrimSpace(plainTextWithOptions(segments, plainTextOptions{
		resolveName: resolve,
		renderReply: func(messageID string) string {
			return quotedDisplayLabel(quoted, messageID, resolve)
		},
	}))
	return strings.Join(strings.Fields(text), " ")
}

// quotedDisplayLabel 渲染引用标记。被引用的原消息在事件上时写清回的是谁的哪句话；
// 只有一个消息 ID 时就只写「[回复]」——那串号码对翻记录的人没有任何意义，写出来
// 反而挤掉正文。
func quotedDisplayLabel(quoted *QuotedMessage, messageID string, resolve AtMentionNameResolver) string {
	if quoted == nil || (quoted.MessageID != "" && messageID != "" && quoted.MessageID != messageID) {
		return "[回复] "
	}
	sender := strings.TrimSpace(quoted.SenderName)
	if sender == "" && strings.TrimSpace(quoted.UserID) != "" && resolve != nil {
		sender = strings.TrimSpace(resolve(strings.TrimSpace(quoted.UserID)))
	}
	if sender == "" {
		sender = strings.TrimSpace(quoted.UserID)
	}
	preview := strings.TrimSpace(plainTextWithOptions(quoted.Segments, plainTextOptions{
		resolveName: resolve,
		// 被引用的消息如果自己也是条回复，只写「[回复]」，不再往下展开一层。
		renderReply: func(string) string { return "[回复] " },
	}))
	if preview == "" {
		preview = strings.TrimSpace(quoted.RawMessage)
	}
	preview = strings.Join(strings.Fields(preview), " ")
	preview = truncateDisplayQuotePreview(preview)
	// 结尾补一个空格，免得标记和正文粘成一句；整条文本最后会做空白归一化，
	// 只有标记没有正文时这个空格会被去掉。
	switch {
	case sender != "" && preview != "":
		return "[回复 " + sender + "：" + preview + "] "
	case sender != "":
		return "[回复 " + sender + "] "
	case preview != "":
		return "[回复：" + preview + "] "
	}
	return "[回复] "
}

func truncateDisplayQuotePreview(text string) string {
	runes := []rune(text)
	if len(runes) <= maxDisplayQuotePreviewRunes {
		return text
	}
	return string(runes[:maxDisplayQuotePreviewRunes]) + "…"
}
