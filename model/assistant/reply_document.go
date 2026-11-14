package assistant

import (
	"regexp"
	"strings"

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

type documentLineKind int

const (
	documentProse documentLineKind = iota // 说明段落
	documentItem                          // 条目：项目符号、编号、表格行、时间点
	documentLabel                         // 小节标题
	documentFence                         // 代码围栏占位符
)
