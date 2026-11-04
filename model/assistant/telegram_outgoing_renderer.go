// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type telegramOpaqueText struct {
	marker   string
	text     string
	entities []telegramEntitySpec
}

type telegramOutgoingRenderer struct {
	namespace string
	opaque    []telegramOpaqueText
}

func renderTelegramOutgoingText(text string, names, usernames map[string]string) (string, []telegramEntitySpec) {
	reserved := []string{text}
	for _, name := range names {
		reserved = append(reserved, name)
	}
	for _, username := range usernames {
		reserved = append(reserved, username)
	}
	renderer := newTelegramOutgoingRenderer(reserved)
	text = renderer.protectCode(text)
	text = spaceAfterTelegramMentions(text)
	text = dianaMentionMarkerPattern.ReplaceAllStringFunc(text, func(marker string) string {
		id := dianaMentionMarkerPattern.FindStringSubmatch(marker)[1]
		display := mentionDisplayText(id, names[id])
		entity := telegramEntitySpec{Type: "text_mention", URL: "diana-mention:" + id}
		if username := usernames[id]; username != "" && normalizeTelegramMentionUsername(username) == username {
			display = "@" + username
			// Bot API ignores input "mention" entities. Keep the explicit user
			// target even when the display is a username adjacent to other text.
		}
		entity.Length = utf16Length(display)
		return renderer.keep(display, []telegramEntitySpec{entity})
	})
	text = convertMarkdownTableRows(text, renderer.table)
	return renderer.markdown(text)
}

// spaceAfterTelegramMentions 让 @ 后面像客户端里手打的那样跟一个空格：
// 「@alice你好」读起来像一个词，用户名还会和后文粘在一起。紧跟强调符号时
// 空格补在符号之后，免得「**@a **」让粗体闭合失效；后面是空白、标点或结尾就不补。
func spaceAfterTelegramMentions(text string) string {
	matches := dianaMentionMarkerPattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	last := 0
	for _, match := range matches {
		end := match[1]
		for end < len(text) && strings.IndexByte("*_~", text[end]) >= 0 {
			end++
		}
		out.WriteString(text[last:end])
		last = end
		if end == len(text) {
			continue
		}
		if strings.HasPrefix(text[end:], dianaMentionMarkerPrefix) {
			out.WriteByte(' ')
			continue
		}
		next, _ := utf8.DecodeRuneInString(text[end:])
		if !unicode.IsSpace(next) && !unicode.IsPunct(next) {
			out.WriteByte(' ')
		}
	}
	out.WriteString(text[last:])
	return out.String()
}

func newTelegramOutgoingRenderer(reserved []string) *telegramOutgoingRenderer {
	for index := 0; ; index++ {
		namespace := "T" + itoa(index) + "X"
		collision := false
		for _, text := range reserved {
			if strings.Contains(text, namespace) {
				collision = true
				break
			}
		}
		if !collision {
			return &telegramOutgoingRenderer{namespace: namespace}
		}
	}
}

func (r *telegramOutgoingRenderer) keep(text string, entities []telegramEntitySpec) string {
	marker := r.namespace + itoa(len(r.opaque)) + ""
	r.opaque = append(r.opaque, telegramOpaqueText{marker: marker, text: text, entities: entities})
	return marker
}

func (r *telegramOutgoingRenderer) protectCode(text string) string {
	var out strings.Builder
	for cursor := 0; cursor < len(text); {
		start := cursor
		char := text[cursor]
		if char != '`' && char != '~' {
			out.WriteByte(char)
			cursor++
			continue
		}
		for cursor < len(text) && text[cursor] == char {
			cursor++
		}
		run := cursor - start
		if run < 3 && char == '~' {
			out.WriteString(text[start:cursor])
			continue
		}
		marker := text[start:cursor]
		bodyStart, end, after := cursor, -1, -1
		language, kind := "", "code"
		if run >= 3 {
			kind = "pre"
			if newline := strings.IndexByte(text[cursor:], '\n'); newline >= 0 {
				header := strings.TrimSpace(text[cursor : cursor+newline])
				if telegramFenceLanguage(header) {
					language = header
					bodyStart = cursor + newline + 1
				}
			}
		}
		for search := bodyStart; search < len(text); {
			relative := strings.Index(text[search:], marker)
			if relative < 0 {
				break
			}
			candidate := search + relative
			closeEnd := candidate + run
			for closeEnd < len(text) && text[closeEnd] == char {
				closeEnd++
			}
			if run >= 3 || closeEnd-candidate == run {
				end, after = candidate, closeEnd
				break
			}
			search = closeEnd
		}
		if end < 0 {
			if run < 3 {
				out.WriteString(marker)
				continue
			}
			end, after = len(text), len(text)
		}
		body := text[bodyStart:end]
		if kind == "code" && strings.Contains(body, "\n") {
			out.WriteString(marker)
			continue
		}
		if kind == "pre" {
			body = strings.TrimRight(body, "\n")
		}
		out.WriteString(r.keep(body, []telegramEntitySpec{{Type: kind, Length: utf16Length(body), Language: language}}))
		cursor = after
	}
	return out.String()
}

func telegramFenceLanguage(text string) bool {
	for _, char := range text {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("_+-", char)) {
			return false
		}
	}
	return true
}

func (r *telegramOutgoingRenderer) markdown(text string) (string, []telegramEntitySpec) {
	text = tgRulePattern.ReplaceAllString(text, "")
	text = normalizeListLines(text)
	buffer := newTelegramSpanBuffer(text, nil)
	buffer.applyBlockquotes()
	for search := 0; search < len(buffer.text); {
		bounds := tgHeadingPattern.FindStringSubmatchIndex(buffer.text[search:])
		if bounds == nil {
			break
		}
		for index := range bounds {
			bounds[index] += search
		}
		body := buffer.text[bounds[2]:bounds[3]]
		offset := utf16Length(buffer.text[:bounds[0]])
		buffer.replace(bounds[0], bounds[1], body)
		buffer.entities = append(buffer.entities, telegramEntitySpec{Type: "bold", Offset: offset, Length: utf16Length(body)})
		search = bounds[0] + len(body)
	}
	buffer.applyInline(tgBoldPattern, 2, 2, func(string) telegramEntitySpec { return telegramEntitySpec{Type: "bold"} })
	buffer.applyInline(tgStrikePattern, 2, 2, func(string) telegramEntitySpec { return telegramEntitySpec{Type: "strikethrough"} })
	buffer.applyWrapped(tgItalicStar, func(string) telegramEntitySpec { return telegramEntitySpec{Type: "italic"} })
	buffer.applyWrapped(tgItalicUnderscore, func(string) telegramEntitySpec { return telegramEntitySpec{Type: "italic"} })
	buffer.applyLink(tgLinkPattern)
	buffer.applySpoilers()
	for index := len(r.opaque) - 1; index >= 0; index-- {
		block := r.opaque[index]
		// Link targets leave the text buffer during Markdown conversion. Restore
		// their literal content too, without adding body entities for the URL.
		for i := range buffer.entities {
			if buffer.entities[i].Type == "text_link" {
				buffer.entities[i].URL = strings.ReplaceAll(buffer.entities[i].URL, block.marker, block.text)
			}
		}
		position := strings.Index(buffer.text, block.marker)
		if position < 0 {
			continue
		}
		offset := utf16Length(buffer.text[:position])
		buffer.replace(position, position+len(block.marker), block.text)
		for _, entity := range block.entities {
			entity.Offset += offset
			buffer.entities = append(buffer.entities, entity)
		}
	}
	// Opaque content may invalidate a URL that passed validation before restoration.
	kept := buffer.entities[:0]
	for _, entity := range buffer.entities {
		if entity.Type == "text_link" && normalizedHTTPURL(entity.URL) == "" {
			continue
		}
		kept = append(kept, entity)
	}
	return buffer.text, compatibleTelegramEntities(kept)
}

// Quote ranges are registered before inline markup is removed so the shared
// span buffer adjusts their UTF-16 offsets together with all other entities.
func (b *telegramSpanBuffer) applyBlockquotes() {
	quoteStart := -1
	quoteEnd := 0
	flush := func() {
		if quoteStart >= 0 && quoteEnd > quoteStart {
			b.entities = append(b.entities, telegramEntitySpec{
				Type: "blockquote", Offset: utf16Length(b.text[:quoteStart]),
				Length: utf16Length(b.text[quoteStart:quoteEnd]),
			})
		}
		quoteStart = -1
	}
	for cursor := 0; cursor < len(b.text); {
		end := strings.IndexByte(b.text[cursor:], '\n')
		if end < 0 {
			end = len(b.text)
		} else {
			end += cursor
		}
		line := b.text[cursor:end]
		prefix := tgQuotePattern.FindStringIndex(line)
		if prefix == nil {
			flush()
		} else {
			if quoteStart < 0 {
				quoteStart = cursor
			}
			// Telegram cannot nest blockquotes; flatten repeated quote markers.
			for prefix != nil {
				b.replace(cursor, cursor+prefix[1], "")
				end -= prefix[1]
				prefix = tgQuotePattern.FindStringIndex(b.text[cursor:end])
			}
			quoteEnd = end
		}
		cursor = end + 1
	}
	flush()
}

func (b *telegramSpanBuffer) applySpoilers() {
	find := func(from int) int {
		for from < len(b.text) {
			i := strings.Index(b.text[from:], "||")
			if i < 0 {
				return -1
			}
			i += from
			slashes := 0
			for j := i - 1; j >= 0 && b.text[j] == '\\'; j-- {
				slashes++
			}
			if slashes%2 == 0 {
				return i
			}
			from = i + 2
		}
		return -1
	}
	for cursor := 0; cursor < len(b.text); {
		start := find(cursor)
		if start < 0 {
			break
		}
		end := find(start + 2)
		if end < 0 {
			break
		}
		if end == start+2 {
			cursor = end + 2
			continue
		}
		offset := utf16Length(b.text[:start])
		length := utf16Length(b.text[start+2 : end])
		b.replace(end, end+2, "")
		b.replace(start, start+2, "")
		b.entities = append(b.entities, telegramEntitySpec{Type: "spoiler", Offset: offset, Length: length})
		cursor = end - 2
	}
}

func (r *telegramOutgoingRenderer) table(rows [][]string) string {
	hasMention := false
	for _, row := range rows {
		for _, cell := range row {
			for _, block := range r.opaque {
				if strings.Contains(cell, block.marker) && len(block.entities) > 0 &&
					(block.entities[0].Type == "mention" || block.entities[0].Type == "text_mention") {
					hasMention = true
				}
			}
		}
	}
	if !hasMention {
		// Inline code is literal inside the table's single pre. Expand it before
		// measuring columns, without nesting its code entity inside the pre.
		for i, row := range rows {
			for j, cell := range row {
				for _, block := range r.opaque {
					cell = strings.ReplaceAll(cell, block.marker, block.text)
				}
				rows[i][j] = cell
			}
		}
		body := renderTableBlock(rows)
		return r.keep(body, []telegramEntitySpec{{Type: "pre", Length: utf16Length(body)}})
	}
	type cellText struct {
		text     string
		entities []telegramEntitySpec
	}
	cells := make([][]cellText, len(rows))
	columns := 0
	for _, row := range rows {
		if len(row) > columns {
			columns = len(row)
		}
	}
	widths := make([]int, columns)
	for i, row := range rows {
		cells[i] = make([]cellText, columns)
		for j, cell := range row {
			body, entities := r.markdown(cell)
			cells[i][j] = cellText{body, entities}
			if width := displayWidth(body); width > widths[j] {
				widths[j] = width
			}
		}
	}
	var out strings.Builder
	var entities []telegramEntitySpec
	offset := 0
	write := func(text string) { out.WriteString(text); offset += utf16Length(text) }
	for i, row := range cells {
		if i > 0 {
			write("\n")
		}
		for j, cell := range row {
			for _, entity := range cell.entities {
				entity.Offset += offset
				entities = append(entities, entity)
			}
			write(cell.text)
			if j+1 < columns {
				write(strings.Repeat(" ", widths[j]-displayWidth(cell.text)+2))
			}
		}
		if i == 0 {
			total := 2 * (columns - 1)
			for _, width := range widths {
				total += width
			}
			write("\n" + strings.Repeat("─", total))
		}
	}
	return r.keep(out.String(), entities)
}

func compatibleTelegramEntities(entities []telegramEntitySpec) []telegramEntitySpec {
	out := make([]telegramEntitySpec, 0, len(entities))
	for _, entity := range entities {
		if entity.Length <= 0 {
			continue
		}
		conflict := false
		for _, other := range entities {
			if entity.Offset >= other.Offset+other.Length || other.Offset >= entity.Offset+entity.Length {
				continue
			}
			// Telegram forbids formatting inside code. Prefer hiding an explicit
			// spoiler over preserving monospace and exposing its contents.
			if (entity.Type == "code" || entity.Type == "pre") && other.Type == "spoiler" {
				conflict = true
				break
			}
			contains := entity.Offset <= other.Offset && entity.Offset+entity.Length >= other.Offset+other.Length
			contained := other.Offset <= entity.Offset && other.Offset+other.Length >= entity.Offset+entity.Length
			if !contains && !contained {
				conflict = true
				break
			}
			if (entity.Type == "text_link" && (other.Type == "mention" || other.Type == "text_mention")) ||
				(entity.Type != "code" && entity.Type != "pre" && entity.Type != "spoiler" && (other.Type == "code" || other.Type == "pre")) {
				conflict = true
				break
			}
		}
		if !conflict {
			out = append(out, entity)
		}
	}
	return out
}

// telegramPrependMention 把出站消息的 MentionUserID 变成正文开头的提及标记。
//
// OneBot 用单独的 at 段 @ 人，Telegram 没有消息段，只认正文里的实体；不在这里
// 转一下，入群欢迎、群规警告、提醒推送在 Telegram 上都 @ 不到人。正文里已经
// 提到这个人就不重复加。
func telegramPrependMention(text, userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return text
	}
	for _, id := range mentionedIDsInText(text) {
		if id == userID {
			return text
		}
	}
	return mentionMarkerFor(userID) + " " + text
}
