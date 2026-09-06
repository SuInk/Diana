package assistant

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

var replyMarkdownParser = goldmark.New(goldmark.WithExtensions(extension.Table)).Parser()

var replySectionHeading = regexp.MustCompile(`(?i)^(?:day[ \t]*[0-9]+|step[ \t]*[0-9]+|第[ \t]*[0-9一二三四五六七八九十百]+[ \t]*(?:天|日|步|章|节|部分|阶段))(?:[ \t:：|｜、.．)）-]|$)`)

// This is a formatting check, not a topic classifier. Without positive document
// structure, ordinary chat keeps its existing newline behavior.
func isDocumentReply(reply string) bool {
	if !strings.Contains(reply, "\n") {
		return false
	}
	source := []byte(reply)
	document := replyMarkdownParser.Parse(text.NewReader(source))
	structured, codeBlocks := false, 0
	ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node.Kind() {
		case ast.KindHeading, ast.KindList, extast.KindTable:
			structured = true
		case ast.KindFencedCodeBlock, ast.KindCodeBlock:
			codeBlocks++
		}
		return ast.WalkContinue, nil
	})
	if structured || codeBlocks > 1 {
		return true
	}
	// Plain and bold-only day/step labels are not Markdown headings. Ignore code
	// bodies while counting these explicit section labels.
	masked, _ := maskFencedCodeBlocks(reply)
	sections := 0
	for _, line := range strings.Split(masked, "\n") {
		if isDocumentSectionLabel(line) {
			sections++
		}
	}
	return sections >= 2
}

// isDocumentSectionLabel 认出一份文档里的小节标题：Markdown 标题、整行加粗，
// 以及「第 2 天」「Step 3」这类没有 Markdown 语法的纯文本小节名。
func isDocumentSectionLabel(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if replyDocumentHeading.MatchString(line) {
		return true
	}
	// 整行加粗不是 Markdown 标题，但在聊天里就是当小节名用的。
	if strings.HasPrefix(line, "**") || strings.HasPrefix(line, "__") {
		block := replyMarkdownParser.Parse(text.NewReader([]byte(line))).FirstChild()
		if block != nil && block.Kind() == ast.KindParagraph {
			if emphasis, ok := block.FirstChild().(*ast.Emphasis); ok && emphasis.Level == 2 && emphasis.NextSibling() == nil {
				return true
			}
		}
	}
	return replySectionHeading.MatchString(strings.TrimSpace(strings.Trim(line, "*_")))
}

var (
	replyDocumentHeading  = regexp.MustCompile(`^#{1,6}[ \t]+\S`)
	replyDocumentTimeItem = regexp.MustCompile(`^[0-9]{1,2}[:：][0-9]{2}`)
)

// isDocumentItemLine 认出小节正文里的条目行：项目符号、编号、表格行和时间点。
//
// 这里不用 isStructuredReplyLine：它把「短标签：内容」也算结构行，而「出行就两个
// 原则：……」是一段说明而不是条目。条目和说明的分界正是下面用来断开消息的信号，
// 判宽了行程末尾的几句提醒就会被粘在最后一天里。
func isDocumentItemLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	runes := []rune(line)
	switch runes[0] {
	case '-', '*', '+', '•', '·', '|':
		return strings.TrimSpace(string(runes[1:])) != ""
	}
	if replyDocumentTimeItem.MatchString(line) {
		return true
	}
	digits := 0
	for digits < len(runes) && unicode.IsDigit(runes[digits]) {
		digits++
	}
	if digits > 0 && digits < len(runes) {
		switch runes[digits] {
		case '.', '、', ')', '）':
			return strings.TrimSpace(string(runes[digits+1:])) != ""
		}
	}
	return false
}

// splitDocumentSections 把一份文档按小节切成几条消息。
//
// 这一层替掉了「文档整条发」。整条发是为了修另一个方向的毛病：按行分条会把一份
// 两天行程打成十几条，每个时间点单独一条。但反过来把一千字的行程塞进一个气泡同样
// 难读——真人发行程也是一天一条。所以既不按行分，也不整条发，按小节分：
//
//	小节标题   另起一条        「第 2 天｜…」「## 检查配置」「**Day 1**」
//	条目转说明 另起一条        一串时间点之后的那几句提醒，是给整份行程的，不属于最后一天
//
// 其余一律留在同一条：清单、表格、代码围栏和连续的说明段落都是一个整体。
func splitDocumentSections(reply string) []string {
	// Markers remain hard boundaries. Within a marked document, only explicit
	// Markdown peer headings can repair an omitted boundary; bold labels and
	// prose transitions must not fragment the model's messages.
	if !strings.Contains(reply, notificationSplitMarker) {
		return documentSectionBlocks(reply, false)
	}
	var out []string
	for _, part := range strings.Split(reply, notificationSplitMarker) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, documentSectionBlocks(part, true)...)
	}
	return out
}

type documentLineKind int

const (
	documentProse documentLineKind = iota // 说明段落
	documentItem                          // 条目：项目符号、编号、表格行、时间点
	documentLabel                         // 小节标题
	documentFence                         // 代码围栏占位符
)

func classifyDocumentLine(line string) documentLineKind {
	if _, fenced := codeFenceIndex(line); fenced {
		return documentFence
	}
	if isDocumentSectionLabel(line) {
		return documentLabel
	}
	if isDocumentItemLine(line) {
		return documentItem
	}
	return documentProse
}

func documentHeadingLevel(line string) int {
	if replyDocumentHeading.MatchString(line) {
		return len(line) - len(strings.TrimLeft(line, "#"))
	}
	// Bold and plain labels carry no explicit hierarchy.
	return 7
}

func documentSectionBlocks(part string, markdownOnly bool) []string {
	var lines []string
	var kinds []documentLineKind
	var levels []int
	rootHeadings := map[int]int{}
	if markdownOnly {
		document := replyMarkdownParser.Parse(text.NewReader([]byte(part)))
		for node := document.FirstChild(); node != nil; node = node.NextSibling() {
			if heading, ok := node.(*ast.Heading); ok && heading.Lines().Len() > 0 {
				lineIndex := strings.Count(part[:heading.Lines().At(0).Start], "\n")
				rootHeadings[lineIndex] = heading.Level
			}
		}
	}
	for lineIndex, line := range strings.Split(part, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		kind := classifyDocumentLine(trimmed)
		level := documentHeadingLevel(trimmed)
		if markdownOnly {
			level = rootHeadings[lineIndex]
			if kind == documentLabel && (level == 0 || !replyDocumentHeading.MatchString(trimmed)) {
				kind = documentProse
			}
		}
		lines = append(lines, strings.TrimRight(line, " \t"))
		kinds = append(kinds, kind)
		levels = append(levels, level)
	}
	if len(lines) == 0 {
		return []string{part}
	}
	var out, current []string
	hasBody, headingLevel := false, 0
	flush := func() {
		if len(current) > 0 {
			out = append(out, strings.Join(current, "\n"))
			current = nil
		}
		hasBody, headingLevel = false, 0
	}
	previousItem := false
	for index, line := range lines {
		switch kinds[index] {
		case documentLabel:
			level := levels[index]
			if hasBody && (headingLevel == 0 || level <= headingLevel) {
				flush()
			}
			// Consecutive headings belong with the first body, never alone.
			if headingLevel == 0 || level < headingLevel {
				headingLevel = level
			}
			previousItem = false
		case documentItem:
			previousItem = true
			hasBody = true
		case documentProse:
			// 一串条目之后重新开始的说明，要成段才另起一条。行程里穿插一句
			// 「中午回市区吃饭」仍属于这一天，末尾那几句提醒才是给整份文档的。
			if !markdownOnly && previousItem && documentProseRun(kinds, index) >= 2 {
				flush()
			}
			previousItem = false
			hasBody = true
		case documentFence:
			// 围栏跟着上下文走，不改变条目状态。
			hasBody = true
		}
		current = append(current, line)
	}
	flush()
	if len(out) == 0 {
		return []string{part}
	}
	return out
}

func documentProseRun(kinds []documentLineKind, start int) int {
	count := 0
	for index := start; index < len(kinds) && kinds[index] == documentProse; index++ {
		count++
	}
	return count
}
