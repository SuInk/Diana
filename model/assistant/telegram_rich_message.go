package assistant

import (
	"bufio"
	"context"
	"errors"
	"html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	goldhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// Only an explicit, rejected unsupported-method response is safe to retry via
// another method. A timeout or 5xx may have already delivered the rich message.
func telegramRichUnsupported(err error) bool {
	var apiErr *telegramAPIError
	if !errors.As(err, &apiErr) || (apiErr.Code != 400 && apiErr.Code != 404) {
		return false
	}
	description := strings.ToLower(strings.TrimSpace(apiErr.Description))
	return description == "not found" || description == "method not found" ||
		strings.Contains(description, "unknown method") ||
		strings.Contains(description, "method not available") ||
		strings.Contains(description, "rich messages are not supported")
}

func (c *TelegramChannel) sendTextWithRichFallback(ctx context.Context, msg OutgoingMessage, plain map[string]any) (map[string]any, error) {
	c.mu.RLock()
	enabled := c.cfg.RichMessages
	c.mu.RUnlock()
	if enabled && !msg.LiteralText {
		if blocks, ok := telegramRichBlocks(msg.Text); ok {
			params := map[string]any{
				"chat_id":      plain["chat_id"],
				"rich_message": map[string]any{"blocks": blocks, "skip_entity_detection": true},
			}
			for _, key := range []string{"message_thread_id", "reply_parameters"} {
				if value, ok := plain[key]; ok {
					params[key] = value
				}
			}
			result, err := c.CallAPI(ctx, "sendRichMessage", params)
			if err == nil || !telegramRichUnsupported(err) {
				return result, err
			}
		}
	}
	return c.CallAPI(ctx, "sendMessage", plain)
}

// Typed blocks avoid Telegram's Markdown-specific math and escaping rules.
// Unsupported constructs use the established entities path, never disappear.
func telegramRichBlocks(markdown string) ([]any, bool) {
	if strings.Contains(markdown, "<diana-") || len(markdown) > 32768 {
		return nil, false
	}
	source := []byte(markdown)
	parser := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.Linkify)).Parser()
	document := parser.Parse(text.NewReader(source))
	r := telegramRichBlockRenderer{source: source, valid: true}
	blocks := r.blocks(document, 0)
	return blocks, r.valid && len(blocks) > 0
}

type telegramRichBlockRenderer struct {
	source []byte
	valid  bool
	count  int
}

func (r *telegramRichBlockRenderer) blocks(parent ast.Node, depth int) []any {
	if depth > 16 {
		r.valid = false
		return nil
	}
	var blocks []any
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		r.count++
		if r.count > 100 {
			r.valid = false
			return nil
		}
		var block map[string]any
		switch n := node.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			block = map[string]any{"type": "paragraph", "text": r.inline(node)}
		case *ast.Heading:
			block = map[string]any{"type": "heading", "size": n.Level, "text": r.inline(node)}
		case *ast.FencedCodeBlock:
			block = map[string]any{"type": "pre", "text": string(n.Lines().Value(r.source))}
			if language := string(n.Language(r.source)); language != "" {
				block["language"] = language
			}
		case *ast.CodeBlock:
			block = map[string]any{"type": "pre", "text": string(n.Lines().Value(r.source))}
		case *ast.Blockquote:
			block = map[string]any{"type": "blockquote", "blocks": r.blocks(node, depth+1)}
		case *ast.List:
			var items []any
			value := n.Start
			for item := n.FirstChild(); item != nil; item = item.NextSibling() {
				entry := map[string]any{"blocks": r.blocks(item, depth+1)}
				if n.IsOrdered() {
					entry["value"], entry["type"] = value, "1"
					value++
				}
				items = append(items, entry)
			}
			block = map[string]any{"type": "list", "items": items}
		case *extast.Table:
			if len(n.Alignments) > 20 {
				r.valid = false
				return nil
			}
			var rows []any
			for row := n.FirstChild(); row != nil; row = row.NextSibling() {
				var cells []any
				for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
					align := "left"
					if c, ok := cell.(*extast.TableCell); ok {
						if c.Alignment == extast.AlignCenter {
							align = "center"
						}
						if c.Alignment == extast.AlignRight {
							align = "right"
						}
					}
					entry := map[string]any{"text": r.inline(cell), "align": align, "valign": "top"}
					if row.Kind() == extast.KindTableHeader {
						entry["is_header"] = true
					}
					cells = append(cells, entry)
				}
				rows = append(rows, cells)
			}
			block = map[string]any{"type": "table", "cells": rows, "is_bordered": true, "is_compact": true}
		default:
			r.valid = false
			return nil
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func (r *telegramRichBlockRenderer) inline(parent ast.Node) []any {
	out := make([]any, 0)
	var literal strings.Builder
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		switch n := node.(type) {
		case *ast.Text:
			value := telegramRichInlineText(n.Value(r.source))
			// Check only visible prose: code and link destinations keep these
			// characters literally, without mention or spoiler semantics.
			if strings.Contains(value, "@") || strings.Contains(value, "||") {
				r.valid = false
				return nil
			}
			// Goldmark's linkifier needs a boundary before bare URLs. Let the
			// legacy Telegram path detect CJK-adjacent URLs rather than flattening them.
			if strings.Contains(value, "https://") || strings.Contains(value, "http://") {
				r.valid = false
				return nil
			}
			out = append(out, value)
			literal.WriteString(value)
			if n.SoftLineBreak() || n.HardLineBreak() {
				out = append(out, "\n")
			}
		case *ast.String:
			if strings.Contains(string(n.Value), "@") || strings.Contains(string(n.Value), "||") {
				r.valid = false
				return nil
			}
			out = append(out, string(n.Value))
			literal.WriteString(string(n.Value))
		case *ast.Emphasis:
			kind := "italic"
			if n.Level == 2 {
				kind = "bold"
			}
			out = append(out, map[string]any{"type": kind, "text": r.inline(node)})
		case *extast.Strikethrough:
			out = append(out, map[string]any{"type": "strikethrough", "text": r.inline(node)})
		case *ast.CodeSpan:
			var body strings.Builder
			for part := n.FirstChild(); part != nil; part = part.NextSibling() {
				if t, ok := part.(*ast.Text); ok {
					body.Write(t.Value(r.source))
				}
			}
			out = append(out, map[string]any{"type": "code", "text": body.String()})
		case *ast.Link:
			url := telegramRichInlineText(n.Destination)
			if normalizedHTTPURL(url) == "" {
				r.valid = false
				return nil
			}
			out = append(out, map[string]any{"type": "url", "url": url, "text": r.inline(node)})
		case *ast.AutoLink:
			url := string(n.URL(r.source))
			if normalizedHTTPURL(url) == "" {
				r.valid = false
				return nil
			}
			out = append(out, map[string]any{"type": "url", "url": url, "text": string(n.Label(r.source))})
		default:
			r.valid = false
			return nil
		}
	}
	// CommonMark 的左右侧定界规则不认中文标点：「**注意：**别这样」里闭合的 **
	// 前面是标点、后面是汉字，不算右侧定界，goldmark 就把两对 ** 当正文留下了。
	// 模型常这么写，富消息又会原样显示星号，所以只要正文里还能配出成对标记，
	// 就整条退回 entities 路径——那边按字面配对，中文标点旁也能正确加粗。
	if telegramRichLeakedDelimiters(literal.String()) {
		r.valid = false
		return nil
	}
	return out
}

func telegramRichLeakedDelimiters(text string) bool {
	return tgBoldPattern.MatchString(text) || tgStrikePattern.MatchString(text) ||
		tgItalicStar.MatchString(text) || tgItalicUnderscore.MatchString(text)
}

// Use Goldmark's escaping semantics, then remove only its HTML transport
// escaping. Escaped ampersands must not turn into a second entity decode.
func telegramRichInlineText(source []byte) string {
	var output strings.Builder
	writer := bufio.NewWriter(&output)
	goldhtml.DefaultWriter.Write(writer, source)
	_ = writer.Flush()
	return html.UnescapeString(output.String())
}
