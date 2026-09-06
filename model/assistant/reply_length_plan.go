package assistant

import (
	"fmt"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

func (r *Runtime) replyPartLimitIssue(cfg BotConfig, event MessageEvent, part string) string {
	part = normalizeReply(part, 0, markdownToPlainForConfig(cfg))
	if cfg.MaxReplyChars > 0 && replyCompressionRunes(part) > cfg.MaxReplyChars {
		return fmt.Sprintf("正文超过单条 %d 字符上限", cfg.MaxReplyChars)
	}
	if NormalizePlatformID(event.Platform) == PlatformTelegram {
		message := r.resolveOutgoingMentionNames(event, OutgoingMessage{Text: part})
		value, mentions := renderDianaMentions(part, message.MentionNames)
		value, _ = telegramRichText(value, mentions)
		if length := utf16Length(value); length > telegramTextLimit {
			return fmt.Sprintf("渲染后为 %d 个 UTF-16 码元，超过 Telegram 单条 %d 上限", length, telegramTextLimit)
		}
	}
	return ""
}

func (r *Runtime) replyLengthPlan(cfg BotConfig, event MessageEvent, body string) []string {
	limits := chatSplitLimitsForEvent(cfg, event)
	parts := splitChatReply(body, limits)
	if limits.SingleMessage || limits.MarkerOnly {
		return parts
	}
	var out []string
	for _, part := range parts {
		if r.replyPartLimitIssue(cfg, event, part) == "" {
			out = append(out, part)
			continue
		}
		out = append(out, splitOversizedReplyNaturally(part, func(candidate string) bool {
			return r.replyPartLimitIssue(cfg, event, candidate) == ""
		})...)
	}
	return out
}

type replyNaturalUnit struct {
	text    string
	heading bool
}

// Only oversized messages use this finer grouping. Markdown containers remain
// atomic; plain prose may split at existing sentence boundaries, never by bytes.
func splitOversizedReplyNaturally(part string, fits func(string) bool) []string {
	units := replyNaturalUnits(part)
	var atoms []string
	headings := ""
	for _, unit := range units {
		if unit.heading {
			headings += unit.text
			continue
		}
		atoms = append(atoms, headings+unit.text)
		headings = ""
	}
	if headings != "" {
		if len(atoms) == 0 {
			return []string{part}
		}
		atoms[len(atoms)-1] += headings
	}
	var out []string
	current := ""
	for _, atom := range atoms {
		if current != "" && !fits(strings.TrimSpace(current+atom)) {
			out = append(out, strings.TrimSpace(current))
			current = ""
		}
		current += atom
	}
	if strings.TrimSpace(current) != "" {
		out = append(out, strings.TrimSpace(current))
	}
	if len(out) == 0 {
		return []string{part}
	}
	return out
}

func replyNaturalUnits(source string) []replyNaturalUnit {
	document := replyMarkdownParser.Parse(text.NewReader([]byte(source)))
	var nodes []ast.Node
	var starts []int
	for node := document.FirstChild(); node != nil; node = node.NextSibling() {
		start := replyBlockSourceStart(source, node)
		if start < 0 || (len(starts) > 0 && start <= starts[len(starts)-1]) {
			return []replyNaturalUnit{{text: source}}
		}
		if len(starts) == 0 {
			start = 0
		}
		starts = append(starts, start)
		nodes = append(nodes, node)
	}
	var units []replyNaturalUnit
	for i, node := range nodes {
		end := len(source)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		value := source[starts[i]:end]
		if node.Kind() != ast.KindParagraph || strings.ContainsAny(value, "`[\"'") || strings.Contains(value, "://") {
			units = append(units, replyNaturalUnit{text: value, heading: node.Kind() == ast.KindHeading})
			continue
		}
		runes := []rune(value)
		start := 0
		for _, end := range boundaryPositions(runes, isSentenceEnd) {
			units = append(units, replyNaturalUnit{text: string(runes[start:end])})
			start = end
		}
		if start < len(runes) {
			units = append(units, replyNaturalUnit{text: string(runes[start:])})
		}
	}
	return units
}

func replyBlockSourceStart(source string, node ast.Node) int {
	lineStart := func(offset int) int { return strings.LastIndex(source[:offset], "\n") + 1 }
	if fence, ok := node.(*ast.FencedCodeBlock); ok {
		if fence.Info != nil {
			return lineStart(fence.Info.Segment.Start)
		}
		if fence.Lines().Len() > 0 {
			start := lineStart(fence.Lines().At(0).Start)
			if start > 0 {
				return lineStart(start - 1)
			}
		}
		return -1
	}
	start := len(source)
	ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if child.Type() == ast.TypeBlock && child.Lines().Len() > 0 {
				start = min(start, child.Lines().At(0).Start)
			}
			if value, ok := child.(*ast.Text); ok {
				start = min(start, value.Segment.Start)
			}
		}
		return ast.WalkContinue, nil
	})
	if start == len(source) {
		return -1
	}
	return lineStart(start)
}
