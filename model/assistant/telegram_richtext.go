// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"strconv"
	"strings"
)

// Telegram 的富文本有两条路：parse_mode 让平台自己解析，或者发纯文本外加一份
// entities 描述哪一段是什么格式。这里走后者。
//
// 不用 parse_mode 是因为 MarkdownV2 要求 _ * [ ] ( ) ~ ` > # + - = | { } . ! 这些
// 字符在非格式用途时全部转义，模型随口写一个句号或减号就会让整条消息以 400 被拒。
// 自己算 entities 就没有转义这回事：正文是纯文本，格式挂在偏移量上。
//
// 偏移量必须按 UTF-16 code unit 计——Telegram 的 entity 就是这么定义的。中文占
// 1 个、emoji 常占 2 个，按 rune 或 byte 算都会错位，格式会盖到相邻的字上。

// telegramEntitySpec 是一条待发送的 entity。
type telegramEntitySpec struct {
	Type     string
	Offset   int
	Length   int
	URL      string
	Language string
	UserID   int64
}

func (e telegramEntitySpec) toParam() map[string]any {
	out := map[string]any{"type": e.Type, "offset": e.Offset, "length": e.Length}
	if e.URL != "" {
		out["url"] = e.URL
	}
	if e.Language != "" {
		out["language"] = e.Language
	}
	if e.UserID != 0 {
		out["user"] = map[string]any{"id": e.UserID}
	}
	return out
}

var (
	tgFencePattern     = regexp.MustCompile("(?s)```([A-Za-z0-9_+-]*)\\n?(.*?)```")
	tgInlineCode       = regexp.MustCompile("`([^`\\n]+)`")
	tgBoldPattern      = regexp.MustCompile(`\*\*([^\n*]+)\*\*`)
	tgStrikePattern    = regexp.MustCompile(`~~([^\n~]+)~~`)
	tgItalicStar       = regexp.MustCompile(`(^|[^\w*])\*([^\n*]+)\*($|[^\w*])`)
	tgItalicUnderscore = regexp.MustCompile(`(^|[^\w_])_([^\n_]+)_($|[^\w_])`)
	tgLinkPattern      = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
	tgHeadingPattern   = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]+(.+)$`)
	tgBulletPattern    = regexp.MustCompile(`(?m)^([ \t]*)[-*+][ \t]+`)
	tgQuotePattern     = regexp.MustCompile(`(?m)^[ \t]*>[ \t]?`)
	tgRulePattern      = regexp.MustCompile(`(?m)^[ \t]*(-{3,}|\*{3,}|_{3,})[ \t]*$\n?`)
)

// telegramRichText 把一段 Markdown 转成 Telegram 的「纯文本 + entities」。
//
// mentions 是上游 renderDianaMentions 已经落地的提及位置（偏移量基于传入的 text）。
// 它们和 Markdown 标记混在同一段文本里，删掉标记会让后面的提及整体前移，所以两者
// 必须在同一次转换里一起算，不能先后各算各的。
func telegramRichText(text string, mentions []dianaMentionSpan) (string, []telegramEntitySpec) {
	if strings.TrimSpace(text) == "" {
		return text, mentionEntitySpecs(mentions)
	}

	// 代码块先抽走：它内部的 * _ ` 都是内容而不是标记，留在原地会被后面的规则啃掉。
	// 抽走后用一个不可能出现在正文里的占位符顶位，最后再放回来。
	type codeBlock struct {
		body     string
		language string
		inline   bool
	}
	var blocks []codeBlock
	const placeholderRune = "\x00"
	placeholder := func(index int) string { return placeholderRune + itoa(index) + placeholderRune }

	text = tgFencePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := tgFencePattern.FindStringSubmatch(match)
		blocks = append(blocks, codeBlock{body: strings.TrimRight(parts[2], "\n"), language: parts[1]})
		return placeholder(len(blocks) - 1)
	})
	text = tgInlineCode.ReplaceAllStringFunc(text, func(match string) string {
		parts := tgInlineCode.FindStringSubmatch(match)
		blocks = append(blocks, codeBlock{body: parts[1], inline: true})
		return placeholder(len(blocks) - 1)
	})

	// 行级标记先归一：它们不产生 entity（Telegram 没有标题类型），只改文本。
	// 标题额外记下来，稍后整行加粗——这是它在 Telegram 上最接近的表达。
	var headingBodies []string
	text = tgRulePattern.ReplaceAllString(text, "")
	text = tgHeadingPattern.ReplaceAllStringFunc(text, func(match string) string {
		body := strings.TrimSpace(tgHeadingPattern.FindStringSubmatch(match)[1])
		headingBodies = append(headingBodies, body)
		return body
	})
	text = tgBulletPattern.ReplaceAllString(text, "$1• ")
	text = tgQuotePattern.ReplaceAllString(text, "")

	// 内联标记按「删标记、记区间」的方式处理。每处理一种，后面的偏移都会变，
	// 所以统一在一个可增量修改的缓冲上做，并同步搬动已经记下的区间。
	buffer := newTelegramSpanBuffer(text, mentions)
	buffer.applyInline(tgBoldPattern, 2, 2, func(string) telegramEntitySpec { return telegramEntitySpec{Type: "bold"} })
	buffer.applyInline(tgStrikePattern, 2, 2, func(string) telegramEntitySpec { return telegramEntitySpec{Type: "strikethrough"} })
	buffer.applyWrapped(tgItalicStar, func(string) telegramEntitySpec { return telegramEntitySpec{Type: "italic"} })
	buffer.applyWrapped(tgItalicUnderscore, func(string) telegramEntitySpec { return telegramEntitySpec{Type: "italic"} })
	buffer.applyLink(tgLinkPattern)

	out, entities := buffer.result()

	// 标题在内联处理之后再加粗：此时标记已经删干净，按最终文本定位不会错位。
	for _, body := range headingBodies {
		if offset, ok := utf16IndexOf(out, body); ok {
			entities = append(entities, telegramEntitySpec{Type: "bold", Offset: offset, Length: utf16Length(body)})
		}
	}

	// 代码块最后放回。占位符本身不含 Markdown 标记，不会被前面的规则动过。
	for index := len(blocks) - 1; index >= 0; index-- {
		block := blocks[index]
		marker := placeholder(index)
		position := strings.Index(out, marker)
		if position < 0 {
			continue
		}
		offset := utf16Length(out[:position])
		out = out[:position] + block.body + out[position+len(marker):]
		delta := utf16Length(block.body) - utf16Length(marker)
		entities = shiftEntitiesAfter(entities, offset, delta)
		spec := telegramEntitySpec{Type: "pre", Offset: offset, Length: utf16Length(block.body), Language: block.language}
		if block.inline {
			spec = telegramEntitySpec{Type: "code", Offset: offset, Length: utf16Length(block.body)}
		}
		entities = append(entities, spec)
	}
	return out, entities
}

// telegramSpanBuffer 在一段文本上反复删除内联标记，同时把已经登记的区间
// （Markdown entity 与提及）跟着搬移。
type telegramSpanBuffer struct {
	text     string
	entities []telegramEntitySpec
}

func newTelegramSpanBuffer(text string, mentions []dianaMentionSpan) *telegramSpanBuffer {
	return &telegramSpanBuffer{text: text, entities: mentionEntitySpecs(mentions)}
}

func mentionEntitySpecs(mentions []dianaMentionSpan) []telegramEntitySpec {
	if len(mentions) == 0 {
		return nil
	}
	out := make([]telegramEntitySpec, 0, len(mentions))
	for _, mention := range mentions {
		out = append(out, telegramEntitySpec{
			Type:   "text_mention",
			Offset: mention.Offset,
			Length: mention.Length,
			// UserID 留给上层解析：非数字 id 要整条丢掉，那是 Telegram 的约束，
			// 不该在这里悄悄吞掉。
			URL: "diana-mention:" + mention.UserID,
		})
	}
	return out
}

// applyInline 处理「左右各有固定长度标记」的语法，如 **粗体**、~~删除线~~。
func (b *telegramSpanBuffer) applyInline(pattern *regexp.Regexp, openLen, closeLen int, build func(string) telegramEntitySpec) {
	for {
		bounds := pattern.FindStringSubmatchIndex(b.text)
		if bounds == nil {
			return
		}
		body := b.text[bounds[2]:bounds[3]]
		offset := utf16Length(b.text[:bounds[0]])
		b.text = b.text[:bounds[0]] + body + b.text[bounds[1]:]
		// 左右标记各删掉一段，后面的内容整体前移。
		b.entities = shiftEntitiesAfter(b.entities, offset, -openLen)
		b.entities = shiftEntitiesAfter(b.entities, offset+utf16Length(body), -closeLen)
		spec := build(body)
		spec.Offset = offset
		spec.Length = utf16Length(body)
		b.entities = append(b.entities, spec)
	}
}

// applyWrapped 处理需要看前后一个字符才能判定的语法（*斜体*、_斜体_），
// 避免把 a*b*c 这种乘号和 snake_case 当成格式。
func (b *telegramSpanBuffer) applyWrapped(pattern *regexp.Regexp, build func(string) telegramEntitySpec) {
	searchFrom := 0
	for {
		bounds := pattern.FindStringSubmatchIndex(b.text[searchFrom:])
		if bounds == nil {
			return
		}
		for index := range bounds {
			if bounds[index] >= 0 {
				bounds[index] += searchFrom
			}
		}
		lead := b.text[bounds[2]:bounds[3]]
		body := b.text[bounds[4]:bounds[5]]
		trail := b.text[bounds[6]:bounds[7]]
		offset := utf16Length(b.text[:bounds[0]]) + utf16Length(lead)
		b.text = b.text[:bounds[0]] + lead + body + trail + b.text[bounds[1]:]
		b.entities = shiftEntitiesAfter(b.entities, offset, -1)
		b.entities = shiftEntitiesAfter(b.entities, offset+utf16Length(body), -1)
		spec := build(body)
		spec.Offset = offset
		spec.Length = utf16Length(body)
		b.entities = append(b.entities, spec)
		searchFrom = bounds[0] + len(lead) + len(body) + len(trail)
		if searchFrom > len(b.text) {
			return
		}
	}
}

// applyLink 把 [文字](链接) 收成一条 text_link，正文只留文字。
func (b *telegramSpanBuffer) applyLink(pattern *regexp.Regexp) {
	for {
		bounds := pattern.FindStringSubmatchIndex(b.text)
		if bounds == nil {
			return
		}
		label := b.text[bounds[2]:bounds[3]]
		target := b.text[bounds[4]:bounds[5]]
		// Diana 自己的标记不是链接：[diana-at:10002] 后面正好跟着括号时会被这条
		// 规则拆掉方括号，出站就认不出提及了。
		if dianaMarkerLabelPattern.MatchString(label) || normalizedHTTPURL(target) == "" {
			b.text = b.text[:bounds[0]] + "\x01" + b.text[bounds[0]+1:]
			continue
		}
		offset := utf16Length(b.text[:bounds[0]])
		b.text = b.text[:bounds[0]] + label + b.text[bounds[1]:]
		removed := utf16Length("["+label+"]("+target+")") - utf16Length(label)
		b.entities = shiftEntitiesAfter(b.entities, offset, -1)
		b.entities = shiftEntitiesAfter(b.entities, offset+utf16Length(label), -(removed - 1))
		b.entities = append(b.entities, telegramEntitySpec{
			Type:   "text_link",
			Offset: offset,
			Length: utf16Length(label),
			URL:    target,
		})
	}
}

func (b *telegramSpanBuffer) result() (string, []telegramEntitySpec) {
	return strings.ReplaceAll(b.text, "\x01", "["), b.entities
}

// shiftEntitiesAfter 把起点在 offset 之后的区间整体平移 delta；跨越 offset 的
// 区间只改长度，起点不动。
func shiftEntitiesAfter(entities []telegramEntitySpec, offset, delta int) []telegramEntitySpec {
	if delta == 0 {
		return entities
	}
	for index := range entities {
		entity := &entities[index]
		switch {
		case entity.Offset >= offset:
			entity.Offset += delta
		case entity.Offset+entity.Length > offset:
			entity.Length += delta
		}
		if entity.Offset < 0 {
			entity.Offset = 0
		}
		if entity.Length < 0 {
			entity.Length = 0
		}
	}
	return entities
}

// utf16IndexOf 返回子串在文本中的 UTF-16 偏移。
func utf16IndexOf(text, sub string) (int, bool) {
	position := strings.Index(text, sub)
	if position < 0 {
		return 0, false
	}
	return utf16Length(text[:position]), true
}

// telegramEntityParams 把 entity 规格转成 Bot API 参数。
//
// text_mention 的 user.id 必须是数字：拿到非数字（脱敏别名没还原干净，或这条消息
// 其实来自别的平台）就丢掉这一条——宁可少一个可点击的提及，也不能让整条消息因为
// 参数非法发不出去。正文里的「@昵称」照常留着。
func telegramEntityParams(entities []telegramEntitySpec) []map[string]any {
	if len(entities) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(entities))
	for _, entity := range entities {
		if entity.Length <= 0 {
			continue
		}
		if entity.Type == "text_mention" {
			userID, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(entity.URL, "diana-mention:")), 10, 64)
			if err != nil {
				continue
			}
			entity.UserID = userID
			entity.URL = ""
		}
		out = append(out, entity.toParam())
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
