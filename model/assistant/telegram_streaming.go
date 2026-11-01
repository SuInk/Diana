// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

// Drafts may expand beyond the source rune budget when usernames or tables are
// rendered. Keep complete runes and clickable entities; formatting can be clipped.
func truncateTelegramDraft(text string, entities []telegramEntitySpec) (string, []telegramEntitySpec) {
	if utf16Length(text) <= telegramTextLimit {
		return text, entities
	}
	limit := telegramTextLimit
	for {
		previous := limit
		for _, entity := range entities {
			switch entity.Type {
			case "mention", "text_mention", "text_link":
				if entity.Offset < limit && entity.Offset+entity.Length > limit {
					limit = entity.Offset
				}
			}
		}
		if limit == previous {
			break
		}
	}
	units, end := 0, len(text)
	for index, r := range text {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > limit {
			end = index
			break
		}
		units += width
	}
	kept := make([]telegramEntitySpec, 0, len(entities))
	for _, entity := range entities {
		if entity.Offset < 0 || entity.Offset >= units || entity.Length <= 0 {
			continue
		}
		if entity.Length > units-entity.Offset {
			entity.Length = units - entity.Offset
		}
		kept = append(kept, entity)
	}
	return text[:end], kept
}
