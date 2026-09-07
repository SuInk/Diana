// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"unicode"
)

// normalizeChatBubbleNewlines removes model-authored soft line wrapping from a
// single chat bubble. Message boundaries have already been decided before this
// function runs; any remaining newline is therefore layout, not delivery.
// Lists, quotations, documents and fenced code keep their layout.
func normalizeChatBubbleNewlines(text string, preserveLayout bool) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if !strings.Contains(text, "\n") || preserveLayout || requiresChatLineLayout(text) {
		return text
	}
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) < 2 {
		return strings.Join(kept, "")
	}
	var out strings.Builder
	out.WriteString(kept[0])
	for _, line := range kept[1:] {
		out.WriteString(chatSoftLineSeparator(out.String(), line))
		out.WriteString(line)
	}
	return out.String()
}

func requiresChatLineLayout(text string) bool {
	if strings.Contains(text, codeFenceSentinel) || looksStructuredBlock(text) {
		return true
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ">") || isDocumentSectionLabel(line) || isDocumentItemLine(line) {
			return true
		}
	}
	return false
}

func chatSoftLineSeparator(left, right string) string {
	leftRunes, rightRunes := []rune(strings.TrimSpace(left)), []rune(strings.TrimSpace(right))
	if len(leftRunes) == 0 || len(rightRunes) == 0 {
		return ""
	}
	last, first := leftRunes[len(leftRunes)-1], rightRunes[0]
	if strings.ContainsRune(".,;:!?", last) && isChatASCIIWordRune(first) {
		return " "
	}
	if strings.ContainsRune("，,、；;：:。！？!?…", last) ||
		strings.ContainsRune("，,、；;：:。！？!?…)]}）】》」』”", first) {
		return ""
	}
	if isChatASCIIWordRune(last) && isChatASCIIWordRune(first) {
		return ". "
	}
	return "，"
}

func isChatASCIIWordRune(r rune) bool {
	return r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r))
}
