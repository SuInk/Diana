// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"strings"
	"unicode/utf16"
)

// 提及用平台中立的标记，不再让模型直接写 OneBot 的 CQ 码。
//
// Diana 自己的另外两个标记早就是中立的：<dianabr> 在 splitReply 里就消化掉了，
// [diana-reply:ID] 由 applyOutgoingReplyMarker 抽成 ReplyMessageID，两个平台各自
// 映射。只有 @ 还在教方言——而 replyMentionPrompt 只看 event.Kind 是不是群聊、
// 不看平台，于是 Telegram 群里它同样教模型写 [CQ:at,qq=…]，而 TelegramChannel.Send
// 是把正文原样发出去的：那边的人看到的就是字面量 [CQ:at,qq=10001]。
//
// 现在统一成 [diana-at:<用户ID>]，出站时按平台翻译：
//
//   - OneBot：换成 CQ at 码，后续照原路进 at 段。
//   - Telegram：换成「@昵称」文本，同时给 sendMessage 传一条 text_mention entity
//     指向这个用户 id。这是 Telegram 给「没有 username 的人」准备的提及方式，
//     显示成可点击的名字，对方有通知——等于把提及虚拟出来，不依赖 username。
//
// 标记里放 id 而不是昵称：昵称会改、会重名，而 id 是 replyMentionCandidates 里
// 给模型的那份候选名单的键。显示用的昵称在出站时按 id 查（见 MentionNames）。
const dianaMentionMarkerPrefix = "[diana-at:"

// dianaMentionMarkerPattern 里的 id 允许字母数字下划线和负号：脱敏开启时模型
// 复制回来的是 im_user_xxx 这种别名，还原成真实 id 之前也要能匹配上。
var dianaMentionMarkerPattern = regexp.MustCompile(`\[diana-at:([A-Za-z0-9_-]{1,64})\]`)

// dianaMentionSpan 是一次提及在渲染结果里的位置，偏移量按 UTF-16 码元计
// ——Telegram 的 entity 就是这么算的，一个 emoji 占两个。
type dianaMentionSpan struct {
	UserID  string
	Display string
	Offset  int
	Length  int
}

// mentionMarkerFor 生成一个提及标记。提示词和工具返回值都用它，避免各处手写。
func mentionMarkerFor(userID string) string {
	return dianaMentionMarkerPrefix + strings.TrimSpace(userID) + "]"
}

// mentionedIDsInText 列出正文里被提及的 id，顺序与出现顺序一致，不去重之外的加工。
func mentionedIDsInText(text string) []string {
	matches := dianaMentionMarkerPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		id := match[1]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// dianaMentionsToCQ 把标记翻成 OneBot 的 CQ at 码。
func dianaMentionsToCQ(text string) string {
	if !strings.Contains(text, dianaMentionMarkerPrefix) {
		return text
	}
	return dianaMentionMarkerPattern.ReplaceAllString(text, "[CQ:at,qq=$1]")
}

// renderDianaMentions 把标记换成可读文本，并给出每个提及在结果里的位置。
// Telegram 侧用它同时拿到正文和 entity 需要的偏移量。
func renderDianaMentions(text string, names map[string]string) (string, []dianaMentionSpan) {
	if !strings.Contains(text, dianaMentionMarkerPrefix) {
		return text, nil
	}
	var builder strings.Builder
	var spans []dianaMentionSpan
	offset := 0
	last := 0
	for _, bounds := range dianaMentionMarkerPattern.FindAllStringSubmatchIndex(text, -1) {
		plain := text[last:bounds[0]]
		builder.WriteString(plain)
		offset += utf16Length(plain)

		userID := text[bounds[2]:bounds[3]]
		display := mentionDisplayText(userID, names[userID])
		builder.WriteString(display)
		length := utf16Length(display)
		spans = append(spans, dianaMentionSpan{UserID: userID, Display: display, Offset: offset, Length: length})
		offset += length
		last = bounds[1]
	}
	builder.WriteString(text[last:])
	return builder.String(), spans
}

// mentionDisplayText 决定提及显示成什么。查不到昵称就退回 id：显示成 @10001
// 不好看，但比显示成空白或漏掉这个人要好。
func mentionDisplayText(userID string, name string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return "@" + trimmed
	}
	return "@" + strings.TrimSpace(userID)
}

// utf16Length 返回字符串占多少个 UTF-16 码元。Telegram 的 entity 偏移量按这个算，
// 按字节或按 rune 数都会在 emoji 和生僻字上错位。
func utf16Length(text string) int {
	if text == "" {
		return 0
	}
	return len(utf16.Encode([]rune(text)))
}
