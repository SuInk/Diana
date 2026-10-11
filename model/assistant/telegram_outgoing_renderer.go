// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

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
	text = dianaMentionMarkerPattern.ReplaceAllStringFunc(text, func(marker string) string {
		id := dianaMentionMarkerPattern.FindStringSubmatch(marker)[1]
		display := mentionDisplayText(id, names[id])
		entity := telegramEntitySpec{Type: "text_mention", URL: "diana-mention:" + id}
		if username := usernames[id]; username != "" && normalizeTelegramMentionUsername(username) == username {
			display = "@" + username
			entity = telegramEntitySpec{Type: "mention"}
		}
		entity.Length = utf16Length(display)
		return renderer.keep(display, []telegramEntitySpec{entity})
	})
	text = convertMarkdownTableRows(text, renderer.table)
	return renderer.markdown(text)
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
	text = tgQuotePattern.ReplaceAllString(text, "")
	buffer := newTelegramSpanBuffer(text, nil)
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
	return buffer.text, compatibleTelegramEntities(buffer.entities)
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
			if (entity.Type == "text_link" && (other.Type == "mention" || other.Type == "text_mention")) ||
				(entity.Type != "code" && entity.Type != "pre" && (other.Type == "code" || other.Type == "pre")) {
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
