package assistant

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var inlineReplyListPrefix = regexp.MustCompile(`^(?:[-*+•·|#]|[0-9]+[.、)）:：])`)

var inlineReplyOrdinal = regexp.MustCompile(`第[ \t]*([0-9]+|[零一二三四五六七八九十]+)[ \t]*(代|步|阶段|点|项)`)

// formatInlineReplyLists only separates consecutive, explicitly numbered items.
// It preserves the wording and punctuation, and leaves code and existing lists alone.
func formatInlineReplyLists(text string) string {
	// Skip replies containing code fences, including fences encoded with control markers.
	if strings.Contains(text, "```") || strings.Contains(text, "~~~") {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inlineReplyListPrefix.MatchString(trimmed) || strings.ContainsAny(line, "`") || strings.Contains(line, "://") || strings.Contains(line, "<diana-") || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "\"") || strings.HasPrefix(trimmed, "“") {
			continue
		}
		matches := inlineReplyOrdinal.FindAllStringSubmatchIndex(line, -1)
		if len(matches) < 2 {
			continue
		}
		previous := -1
		unit := ""
		valid := true
		for j, m := range matches {
			prefix := strings.TrimRightFunc(line[:m[0]], unicode.IsSpace)
			if (j == 0 && prefix != "" && !strings.HasSuffix(prefix, "：") && !strings.HasSuffix(prefix, ":")) || (j > 0 && !strings.HasSuffix(prefix, "；") && !strings.HasSuffix(prefix, ";")) {
				valid = false
				break
			}
			n := inlineReplyOrdinalNumber(line[m[2]:m[3]])
			currentUnit := line[m[4]:m[5]]
			if n < 0 || (j > 0 && (n != previous+1 || unit != currentUnit)) {
				valid = false
				break
			}
			end := len(line)
			if j+1 < len(matches) {
				end = matches[j+1][0]
			}
			if strings.Trim(line[m[1]:end], " \t：:；;") == "" {
				valid = false
				break
			}
			previous, unit = n, currentUnit
		}
		if !valid {
			continue
		}
		var out strings.Builder
		cursor := 0
		for _, m := range matches {
			out.WriteString(line[cursor:m[0]])
			if m[0] > 0 {
				out.WriteByte('\n')
			}
			out.WriteString("- ")
			cursor = m[0]
		}
		out.WriteString(line[cursor:])
		lines[i] = out.String()
	}
	return strings.Join(lines, "\n")
}

func inlineReplyOrdinalNumber(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	for n, value := range []string{"零", "一", "二", "三", "四", "五", "六", "七", "八", "九", "十"} {
		if s == value {
			return n
		}
	}
	return -1
}
