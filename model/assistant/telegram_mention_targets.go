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
			m.Username = e.User.Username
			m.UsernameKnown = true
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
		case "bot_command":
			// /cmd@username 是群里指定机器人的标准写法，Bot API 只给一个
			// bot_command entity，不会再补 mention。
			command, ok := telegramEntityText(units, e)
			if !ok || e.Length <= 0 {
				continue
			}
			_, username, found := strings.Cut(command, "@")
			if !found || username == "" {
				continue
			}
			m.Username = username
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
	if telegramTextMentionsSelf(text, entities, botUsername) && !telegramMentionsContainSelf(out) {
		out = append(out, MessageMention{Username: strings.TrimPrefix(botUsername, "@"), UserID: selfID, Target: "self"})
	}
	return out
}

func telegramMentionsContainSelf(mentions []MessageMention) bool {
	for _, m := range mentions {
		if m.Target == "self" {
			return true
		}
	}
	return false
}

// telegramTextMentionsSelf 在正文里找没有 entity 的 @本机用户名。
// Telegram 只在 @ 前面是空格或标点时才生成 mention entity，「扔过来@dianawwwbot」
// 这种 @ 紧贴中文的写法收不到 entity，人看着却分明是在叫机器人。
// 左边是 ASCII 字母数字或 ./:=-（邮箱、链接）、右边还接用户名字符（更长的
// 别人用户名）、或落在已有 entity 里（链接、代码、别人的 mention）的都不算。
func telegramTextMentionsSelf(text string, entities []telegramEntity, botUsername string) bool {
	username := strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
	if username == "" || text == "" {
		return false
	}
	units := utf16.Encode([]rune(text))
	needle := utf16.Encode([]rune("@" + username))
	for i := 0; i+len(needle) <= len(units); i++ {
		if !telegramUnitsEqualFold(units[i:i+len(needle)], needle) {
			continue
		}
		if i > 0 && telegramMentionBlockedLeft(units[i-1]) {
			continue
		}
		if end := i + len(needle); end < len(units) && telegramUsernameUnit(units[end]) {
			continue
		}
		if telegramOffsetInEntity(i, entities) {
			continue
		}
		return true
	}
	return false
}

// telegramUnitsEqualFold 只折叠 ASCII 大小写：Telegram 用户名本来就只有 ASCII。
func telegramUnitsEqualFold(a, b []uint16) bool {
	fold := func(u uint16) uint16 {
		if u >= 'A' && u <= 'Z' {
			return u + 'a' - 'A'
		}
		return u
	}
	for i := range a {
		if fold(a[i]) != fold(b[i]) {
			return false
		}
	}
	return true
}

func telegramUsernameUnit(u uint16) bool {
	return u == '_' || (u >= '0' && u <= '9') || (u >= 'a' && u <= 'z') || (u >= 'A' && u <= 'Z')
}

func telegramMentionBlockedLeft(u uint16) bool {
	return telegramUsernameUnit(u) || strings.ContainsRune("./:=-@", rune(u))
}

func telegramOffsetInEntity(offset int, entities []telegramEntity) bool {
	for _, e := range entities {
		if offset >= e.Offset && offset < e.Offset+e.Length {
			return true
		}
	}
	return false
}
