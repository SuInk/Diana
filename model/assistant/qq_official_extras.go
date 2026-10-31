// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/agent"
)

// QQ 官方机器人消息上除了按钮还能挂的东西：正文版式（markdown.style）、单聊输入框上方
// 的推荐回复（prompt_keyboard）、图文卡片（msg_type 8）。和按钮一样，取值和请求体结构都
// 定义在源码里，模型在收尾字段里只写内容或挑名字，由 Diana 拼。
//
// 来源：markdown.style 的 main_font_size / layout 和 prompt_keyboard 只出现在官方 Go SDK
// botgo 和 nonebot adapter-qq 里，开放平台文档没写；nonebot 只往单聊发 prompt_keyboard，
// 按钮固定 style 2、action.type 2。图文卡片 card.type=tuwen 见开放平台 2026-10-28 的更新日志，
// 旧的 Ark 模板在 v2 的单聊和群里基本发不出来，所以不用 Ark。
const (
	messageStyleFinalizeFieldName = "message_style"
	suggestionsFinalizeFieldName  = "suggestions"
	cardFinalizeFieldName         = "card"

	qqMarkdownLayoutCentered = "hide_avatar_and_center"
	qqMsgTypeCard            = 8
	qqCardTypeTuwen          = "tuwen"
	qqSuggestionStyle        = 2
	qqSuggestionMax          = 3
	qqCardTitleMaxRunes      = 30
	qqCardDescMaxRunes       = 60
)

// qqMessageFontSizes 是模型能写的正文字号。
var qqMessageFontSizes = map[string]string{"小字": "small", "中字": "middle", "大字": "large"}

// QQMarkdownStyle 是一条 Markdown 消息的版式，零值是平台默认。
type QQMarkdownStyle struct {
	FontSize string
	Centered bool
}

func (s QQMarkdownStyle) isZero() bool {
	return s.FontSize == "" && !s.Centered
}

// payload 转成 markdown.style。
func (s QQMarkdownStyle) payload() map[string]any {
	style := map[string]any{}
	if s.FontSize != "" {
		style["main_font_size"] = s.FontSize
	}
	if s.Centered {
		style["layout"] = qqMarkdownLayoutCentered
	}
	return style
}

// QQCard 是一张图文卡片，点开跳 URL。
type QQCard struct {
	Title       string
	Description string
	URL         string
	PicURL      string
}

func (card QQCard) payload() map[string]any {
	content := map[string]any{"title": card.Title, "description": card.Description, "url": card.URL}
	if card.PicURL != "" {
		content["pic_url"] = card.PicURL
	}
	return map[string]any{"msg_type": qqMsgTypeCard, "card": map[string]any{"type": qqCardTypeTuwen, "content": content}}
}

func messageStyleFinalizeField() agent.FinalizeField {
	return agent.FinalizeField{
		Name: messageStyleFinalizeFieldName,
		Description: "这条回复的版式，平时不填。对方要求或内容适合时写：「小字」「中字」「大字」调正文字号，" +
			"「居中」隐藏头像并让正文居中（适合短公告、诗句、卡片式内容），可用空格组合，如「大字 居中」。",
	}
}

func suggestionsFinalizeField() agent.FinalizeField {
	return agent.FinalizeField{
		Name: suggestionsFinalizeFieldName,
		Description: "单聊里输入框上方的推荐回复，对方点一下就发出那句话。和 buttons 二选一：" +
			"只是给对方递话头、不挂在消息底下时用它，写 1～3 句对方最可能接着说的话，用 | 分隔，每句 2～8 个字。",
	}
}

func cardFinalizeField() agent.FinalizeField {
	return agent.FinalizeField{
		Name: cardFinalizeFieldName,
		Description: "要给对方一个网页时，可以在正文之后再发一张图文卡片，点开就是那个网页，平时不填。" +
			"写「标题|一句话描述|https://网址」，有合适的封面图再加「|https://图片网址」；只用你确实读过的真实网址。",
	}
}

// parseMessageStyle 解析收尾字段，认不出的词跳过。
func parseMessageStyle(value string) QQMarkdownStyle {
	var style QQMarkdownStyle
	for _, word := range strings.Fields(strings.NewReplacer("，", " ", ",", " ", "、", " ").Replace(value)) {
		if size, ok := qqMessageFontSizes[word]; ok {
			style.FontSize = size
		} else if word == "居中" {
			style.Centered = true
		}
	}
	return style
}

// parseSuggestions 解析推荐回复，最多留三句。
func parseSuggestions(value string) []string {
	var out []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == '|' || r == '\n' }) {
		item = cutButtonText(strings.TrimSpace(item), qqButtonLabelMaxRunes)
		if item == "" {
			continue
		}
		out = append(out, item)
		if len(out) == qqSuggestionMax {
			break
		}
	}
	return out
}

// parseCard 解析「标题|描述|网址|图片」，网址不是 http(s) 时不发卡片。
func parseCard(value string) *QQCard {
	parts := strings.Split(strings.TrimSpace(value), "|")
	if len(parts) < 3 {
		return nil
	}
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	card := &QQCard{Title: cutButtonText(parts[0], qqCardTitleMaxRunes), Description: cutButtonText(parts[1], qqCardDescMaxRunes), URL: parts[2]}
	if len(parts) > 3 && qqCardURL(parts[3]) {
		card.PicURL = parts[3]
	}
	if card.Title == "" || !qqCardURL(card.URL) {
		return nil
	}
	return card
}

func qqCardURL(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

// qqPromptKeyboard 把推荐回复拼成 prompt_keyboard。
func qqPromptKeyboard(suggestions []string) map[string]any {
	buttons := make([]any, 0, len(suggestions))
	for _, label := range suggestions {
		buttons = append(buttons, map[string]any{
			"render_data": map[string]any{"label": label, "style": qqSuggestionStyle},
			"action":      map[string]any{"type": qqButtonActionCommand},
		})
	}
	return map[string]any{"keyboard": map[string]any{"content": map[string]any{"rows": []any{map[string]any{"buttons": buttons}}}}}
}

type finalizeQQExtrasKey struct{}

// finalizeQQExtras 把收尾时填的版式、推荐回复和卡片带到发送阶段。版式管整条回复，拆成
// 几条时每条文字都带上；推荐回复和卡片和按钮一样只挂在最后一条文字上。
type finalizeQQExtras struct {
	mu          sync.Mutex
	style       QQMarkdownStyle
	suggestions []string
	card        *QQCard
}

func withFinalizeQQExtras(ctx context.Context) (context.Context, *finalizeQQExtras) {
	holder := &finalizeQQExtras{}
	return context.WithValue(ctx, finalizeQQExtrasKey{}, holder), holder
}

func finalizeQQExtrasFromContext(ctx context.Context) *finalizeQQExtras {
	holder, _ := ctx.Value(finalizeQQExtrasKey{}).(*finalizeQQExtras)
	return holder
}

func (f *finalizeQQExtras) set(fields map[string]string) {
	if f == nil {
		return
	}
	style := parseMessageStyle(fields[messageStyleFinalizeFieldName])
	suggestions := parseSuggestions(fields[suggestionsFinalizeFieldName])
	card := parseCard(fields[cardFinalizeFieldName])
	f.mu.Lock()
	f.style, f.suggestions, f.card = style, suggestions, card
	f.mu.Unlock()
}

func (f *finalizeQQExtras) markdownStyle() QQMarkdownStyle {
	if f == nil {
		return QQMarkdownStyle{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.style
}

// takeLast 取出只挂在最后一条上的推荐回复和卡片并清空。
func (f *finalizeQQExtras) takeLast() ([]string, *QQCard) {
	if f == nil {
		return nil, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	suggestions, card := f.suggestions, f.card
	f.suggestions, f.card = nil, nil
	return suggestions, card
}
