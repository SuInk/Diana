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
// Diana 自己的另外两个标记早就是中立的：[diana-msg] 在 splitReply 里就消化掉了，
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

// 出站前的标记归一与清理。
//
// 标记的语法是模型手写的，写歪就没人认得出来。线上 9/20 换上 mimo-x-flash-preview
// 之后一天里写出六种形态：<diana-at:ID>、(diana-at:ID)、[ diana-at:ID ]、
// @diana-at-ID，以及光秃秃一个 @diana-at。dianaMentionMarkerPattern 一个都不认，
// 于是它们原样当正文发进群——截图里那句「@diana-at-30007 撤啥呀」就是这么来的。
//
// 提示词已经写明唯一合法写法，但小模型记得有这个标记、不记得确切语法是常态，
// 只靠提示词堵不住。出站前因此过两道：
//
//   - normalizeDianaMentionVariants：外壳换成方括号、分隔符换成冒号，只要 id 还
//     认得出来就还原成真提及——模型本来就是想 @ 这个人，没理由因为括号打错就不 @。
//   - dropUnusableDianaMentions：id 认不出来（占位符、编出来的、只写了半个标记）
//     的整个丢掉。不降级成纯文本的「@昵称」：那个点不动也没通知，读的人以为被叫
//     了其实没有；少一个 @ 只是这句话没点名，句子本身照样完整。
//
// 两道都跳过代码块和行内代码：Diana 在群里讲自己怎么实现时会把标记写进反引号，
// 那是在展示写法，不是要提及谁。
const mentionCodeSpanAlternation = "(?s)```.*?```|~~~.*?~~~|`[^`\n]*`|"

var (
	// 变体的核心：diana-at 后面跟一个分隔符和一个还认得出的 id。
	dianaMentionVariantCore = `[Dd]iana[-_ ]?at[ \t]*[:：=-][ \t]*([A-Za-z0-9_-]{1,64})`
	// 连同外壳一起吃掉：方括号、尖括号、圆括号、花括号、中文方括号，或者一个 @。
	dianaMentionVariantPattern = regexp.MustCompile(mentionCodeSpanAlternation +
		`[\[<({【]?[ \t]*@?[ \t]*` + dianaMentionVariantCore + `[ \t]*[\]>)}】]?`)
	dianaMentionVariantIDPattern = regexp.MustCompile(dianaMentionVariantCore)
	// 归一之后还剩下的标记残骸：括号里裹着 diana-at 的任何东西，以及没带 id 的裸写。
	dianaMentionResiduePattern = regexp.MustCompile(mentionCodeSpanAlternation +
		`[\[<({【][^\[\]<>(){}【】\n]{0,80}?[Dd]iana[-_ ]?at[^\[\]<>(){}【】\n]{0,80}?[\]>)}】]` +
		`|@?[ \t]*[Dd]iana[-_ ]?at(?:[ \t]*[:：=-][ \t]*[A-Za-z0-9_-]{0,64})?`)
)

// normalizeDianaMentionVariants 把写歪的提及标记改回正规形态。
func normalizeDianaMentionVariants(text string) string {
	if !strings.Contains(strings.ToLower(text), "diana") {
		return text
	}
	return dianaMentionVariantPattern.ReplaceAllStringFunc(text, func(token string) string {
		if isMentionCodeSpan(token) {
			return token
		}
		match := dianaMentionVariantIDPattern.FindStringSubmatch(token)
		if match == nil {
			return token
		}
		return mentionMarkerFor(match[1])
	})
}

// dropUnusableDianaMentions 丢掉 id 不可用的标记，正规且 id 可用的原样留下。
func dropUnusableDianaMentions(text string, acceptable func(id string) bool) string {
	if !strings.Contains(strings.ToLower(text), "diana") {
		return text
	}
	var builder strings.Builder
	last := 0
	for _, bounds := range dianaMentionResiduePattern.FindAllStringIndex(text, -1) {
		token := text[bounds[0]:bounds[1]]
		if isMentionCodeSpan(token) || usableMentionMarker(token, acceptable) {
			continue
		}
		builder.WriteString(text[last:bounds[0]])
		last = bounds[1]
		// 标记连着的那个空格是给提及和正文分隔用的，标记没了它就成了多余的缩进。
		if last < len(text) && text[last] == ' ' {
			last++
		}
	}
	if last == 0 {
		return text
	}
	builder.WriteString(text[last:])
	return strings.TrimSpace(builder.String())
}

// usableMentionMarker 判断这段残骸是不是一个照常发出去就行的正规标记。
func usableMentionMarker(token string, acceptable func(id string) bool) bool {
	match := dianaMentionMarkerPattern.FindStringSubmatch(token)
	if match == nil || match[0] != token {
		return false
	}
	return acceptable(match[1])
}

func isMentionCodeSpan(token string) bool {
	return strings.HasPrefix(token, "`") || strings.HasPrefix(token, "~")
}

// mentionIDAcceptable 判断这个 id 能不能当成 platform 上的账号发出去。
//
// OneBot 和 Telegram 的用户 ID 都是纯数字，卡死数字最省事。其余平台的 ID 形态各
// 不相同（飞书是 ou_ 开头的 open_id，企业微信是字母数字的账号名），没法统一断言，
// 只排掉两种确定不是账号的：把标记名写进 id 的，和没还原成功的脱敏别名——别名能
// 对上就一定在 restoreText 那步换成真 ID 了，还留着 im_ 前缀说明这个 id 是编的。
func mentionIDAcceptable(platform, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if strings.Contains(strings.ToLower(id), "diana") || strings.HasPrefix(id, identityAliasPrefix) {
		return false
	}
	switch NormalizePlatformID(platform) {
	case PlatformOneBotV11, PlatformTelegram:
		return numericChatID(id)
	}
	return true
}

func numericChatID(id string) bool {
	for index := 0; index < len(id); index++ {
		if id[index] < '0' || id[index] > '9' {
			return false
		}
	}
	return len(id) > 0
}
