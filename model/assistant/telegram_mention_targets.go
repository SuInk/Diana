package assistant

import (
	"strconv"
	"strings"
	"unicode/utf16"
)

func telegramMentionTargets(text string, entities []telegramEntity, selfID, botUsername string) []MessageMention {
	units := utf16.Encode([]rune(text))
	var out []MessageMention
	for _, e := range entities {
		m := MessageMention{Target: "unknown"}
		switch e.Type {
		case "text_mention":
			if e.User == nil {
				continue
			}
			m.UserID = strconv.FormatInt(e.User.ID, 10)
			if selfID != "" {
				m.Target = "other"
				if m.UserID == selfID {
					m.Target = "self"
				}
			}
		case "mention":
			if e.Offset < 0 || e.Length <= 0 || e.Offset > len(units) || e.Length > len(units)-e.Offset {
				continue
			}
			m.Username = strings.TrimPrefix(string(utf16.Decode(units[e.Offset:e.Offset+e.Length])), "@")
			if botUsername != "" {
				m.Target = "other"
				if strings.EqualFold(m.Username, strings.TrimPrefix(botUsername, "@")) {
					m.Target = "self"
					m.UserID = selfID
				}
			}
		default:
			continue
		}
		out = append(out, m)
	}
	return out
}
