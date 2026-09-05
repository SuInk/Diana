// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"html"
	"regexp"
	"sort"
	"strings"
)

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
		renderSegment: displaySegmentLabel,
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
		renderReply:   func(string) string { return "[回复] " },
		renderSegment: displaySegmentLabel,
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

const (
	// maxDisplayCardTitleRunes 是卡片标题和描述各自的长度上限。卡片正文常常是一整
	// 段营销文案，事件列表一行放不下，也没必要放下。
	maxDisplayCardTitleRunes = 40
)

// displaySegmentLabel 渲染 PlainText 不写出来、或者写出来不能看的那几种段。
//
// json/xml 是 QQ 的分享卡片，PlainText 一个字都不写——给模型的正文里卡片内容由链接
// 解析那条路负责，控制台没有那条路，于是列表上只剩一个 [CQ:json,...] 或者前端兜底的
// 「[卡片消息]」。forward 则会把一长串 base64 resid 摊在正文里，那串东西没有任何一处
// 会再读它，纯粹占地方。
func displaySegmentLabel(segment MessageSegment) string {
	switch segment.Type {
	case "forward":
		if summary := strings.TrimSpace(segment.Data["summary"]); summary != "" {
			return summary
		}
		return "[合并转发]"
	case "json":
		return cardDisplayLabel(jsonCardSummary(segment.Data["data"]))
	case "xml":
		return cardDisplayLabel(xmlCardSummary(segment.Data["data"]))
	}
	return ""
}

// cardSummary 是从一张卡片里挖出来的可显示部分。三样都空表示没挖到。
type cardSummary struct {
	Tag   string
	Title string
	Desc  string
}

func cardDisplayLabel(summary cardSummary) string {
	label := "[卡片"
	if tag := truncateDisplayCardText(summary.Tag); tag != "" {
		label += "·" + tag
	}
	label += "]"
	title := truncateDisplayCardText(summary.Title)
	if title != "" {
		label += " " + title
	}
	// 描述和标题一样时不重复写一遍：分享卡片经常两个字段填同一句话。
	if desc := truncateDisplayCardText(summary.Desc); desc != "" && desc != title {
		if title == "" {
			label += " " + desc
		} else {
			label += " — " + desc
		}
	}
	return label + " "
}

// jsonCardSummary 解析 QQ 的 json 卡片。
//
// meta 下面是一层按来源命名的容器：转发的图文叫 news，小程序叫 detail_1，音乐叫
// music……名字认不完，所以不按名字取，挨个试到能解出标题为止。map 的遍历顺序是随机
// 的，按键名排序保证同一张卡片每次渲染出来一样。
func jsonCardSummary(raw string) cardSummary {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cardSummary{}
	}
	var card struct {
		Prompt string                     `json:"prompt"`
		Desc   string                     `json:"desc"`
		Meta   map[string]json.RawMessage `json:"meta"`
	}
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		return cardSummary{}
	}
	keys := make([]string, 0, len(card.Meta))
	for key := range card.Meta {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var entry struct {
			Title string `json:"title"`
			Desc  string `json:"desc"`
			Tag   string `json:"tag"`
			Host  struct {
				Nick string `json:"nick"`
			} `json:"host"`
		}
		if err := json.Unmarshal(card.Meta[key], &entry); err != nil {
			continue
		}
		title := strings.TrimSpace(entry.Title)
		if title == "" {
			continue
		}
		return cardSummary{
			Tag:   firstNonEmpty(strings.TrimSpace(entry.Tag), strings.TrimSpace(entry.Host.Nick)),
			Title: title,
			Desc:  strings.TrimSpace(entry.Desc),
		}
	}
	// meta 解不出来时退回 prompt：那是 QQ 自己给聊天列表用的一行摘要，形如
	// 「[分享]标题」，虽然粗但比「[卡片消息]」强。
	if prompt := strings.TrimSpace(card.Prompt); prompt != "" {
		return cardSummary{Title: prompt}
	}
	return cardSummary{Title: strings.TrimSpace(card.Desc)}
}

// xmlCardAttrPattern 取 xml 卡片根节点上的 brief / title。xml 卡片的结构按业务各不
// 相同，正经解析不划算，这里只捞这两个属性——brief 就是客户端在聊天列表里显示的那句。
var xmlCardAttrPattern = regexp.MustCompile(`(?i)\b(brief|title)\s*=\s*"([^"]*)"`)

func xmlCardSummary(raw string) cardSummary {
	brief, title := "", ""
	for _, match := range xmlCardAttrPattern.FindAllStringSubmatch(raw, 8) {
		value := strings.TrimSpace(html.UnescapeString(match[2]))
		if value == "" {
			continue
		}
		// brief 不论出现在哪个属性后面都优先：title 常常是「通用」这种业务分类名，
		// brief 才是客户端真正显示的那句。
		if strings.EqualFold(match[1], "brief") {
			if brief == "" {
				brief = value
			}
			continue
		}
		if title == "" {
			title = value
		}
	}
	return cardSummary{Title: firstNonEmpty(brief, title)}
}

func truncateDisplayCardText(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	runes := []rune(text)
	if len(runes) <= maxDisplayCardTitleRunes {
		return text
	}
	return string(runes[:maxDisplayCardTitleRunes]) + "…"
}
