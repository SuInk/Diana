// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"regexp"
	"strings"
)

var (
	// tgListItemPattern 拆出列表行的缩进、项目符号，以及可选的待办框。
	tgListItemPattern = regexp.MustCompile(`^([ \t]*)([-*+])[ \t]+(\[([ xX])\][ \t]+)?`)
	// tgTableSeparator 认表格的第二行：|---|:--:|---|，它是「上一行是表头」的唯一凭据。
	tgTableSeparator = regexp.MustCompile(`^[ \t]*\|?[ \t]*:?-{2,}:?[ \t]*(\|[ \t]*:?-{2,}:?[ \t]*)+\|?[ \t]*$`)
)

// tgListBullets 按嵌套层级换符号。Telegram 不认 Markdown 的缩进列表，光靠前导空格
// 在手机上几乎看不出层级；换个符号比缩进更能表达「这条挂在上一条底下」。
var tgListBullets = []string{"•", "◦", "▪"}

// normalizeListLines 把 Markdown 列表整理成 Telegram 能直接显示的样子：
// 项目符号按层级区分，待办框换成实心方框。
func normalizeListLines(text string) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		match := tgListItemPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		marker := tgListBullets[listIndentDepth(match[1])%len(tgListBullets)]
		if match[3] != "" {
			// 待办项用 ☐/☑ 而不是留着 [ ]/[x]：后者在聊天窗口里就是一对方括号，
			// 看不出是个勾选框。
			marker = "☐"
			if strings.EqualFold(match[4], "x") {
				marker = "☑"
			}
		}
		lines[index] = strings.Repeat("  ", listIndentDepth(match[1])) + marker + " " + line[len(match[0]):]
	}
	return strings.Join(lines, "\n")
}

// listIndentDepth 把缩进折算成嵌套层级：一个 Tab 记一级，每两个空格记一级。
func listIndentDepth(indent string) int {
	depth, spaces := 0, 0
	for _, r := range indent {
		if r == '\t' {
			depth++
			continue
		}
		spaces++
	}
	return depth + spaces/2
}

// convertMarkdownTables 把 Markdown 表格换成等宽块。
//
// Telegram 没有表格 entity，原样发出去就是一行行竖线和减号。等宽块是唯一还能让列
// 对齐的表达：把单元格按列宽补齐，再交给调用方登记成 pre。emit 返回顶位用的占位符。
func convertMarkdownTables(text string, emit func(body string) string) string {
	if !strings.Contains(text, "|") {
		return text
	}
	lines := strings.Split(text, "\n")
	var out []string
	for index := 0; index < len(lines); index++ {
		// 表格至少要有表头和分隔行，否则一句带竖线的普通话也会被吃掉。
		if !isTableRow(lines[index]) || index+1 >= len(lines) || !tgTableSeparator.MatchString(lines[index+1]) {
			out = append(out, lines[index])
			continue
		}
		rows := [][]string{splitTableRow(lines[index])}
		cursor := index + 2
		for cursor < len(lines) && isTableRow(lines[cursor]) {
			rows = append(rows, splitTableRow(lines[cursor]))
			cursor++
		}
		out = append(out, emit(renderTableBlock(rows)))
		index = cursor - 1
	}
	return strings.Join(out, "\n")
}

// isTableRow 判断这一行是不是表格行。要求两侧有竖线之外还至少夹着一个，
// 免得把「今天 | 明天」这种随手写的竖线当成表格。
func isTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "|") && strings.Count(trimmed, "|") >= 2
}

func splitTableRow(row string) []string {
	trimmed := strings.TrimSpace(row)
	trimmed = strings.TrimSuffix(strings.TrimPrefix(trimmed, "|"), "|")
	cells := strings.Split(trimmed, "|")
	for index := range cells {
		cells[index] = strings.TrimSpace(cells[index])
	}
	return cells
}

// renderTableBlock 把解析好的单元格排成等宽表格，表头下面补一条分隔线。
func renderTableBlock(rows [][]string) string {
	columns := 0
	for _, row := range rows {
		if len(row) > columns {
			columns = len(row)
		}
	}
	widths := make([]int, columns)
	for _, row := range rows {
		for index, cell := range row {
			if width := displayWidth(cell); width > widths[index] {
				widths[index] = width
			}
		}
	}

	render := func(row []string) string {
		parts := make([]string, 0, columns)
		for index := 0; index < columns; index++ {
			cell := ""
			if index < len(row) {
				cell = row[index]
			}
			parts = append(parts, cell+strings.Repeat(" ", widths[index]-displayWidth(cell)))
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}

	out := make([]string, 0, len(rows)+1)
	for index, row := range rows {
		out = append(out, render(row))
		if index > 0 {
			continue
		}
		// 表头和正文之间补一条横线，长度按整表算，读起来才像张表。
		total := 0
		for _, width := range widths {
			total += width
		}
		out = append(out, strings.Repeat("─", total+2*(columns-1)))
	}
	return strings.Join(out, "\n")
}

// displayWidth 按等宽字体里实际占几格算宽度。中日韩文字、全角标点和 emoji 占两格，
// 按 rune 数补空格的话中文列会整体偏左。
func displayWidth(text string) int {
	width := 0
	for _, r := range text {
		if isWideRune(r) {
			width += 2
			continue
		}
		width++
	}
	return width
}

func isWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // 韩文字母
		r >= 0x2E80 && r <= 0xA4CF, // 汉字、假名、部首
		r >= 0xAC00 && r <= 0xD7A3, // 韩文音节
		r >= 0xF900 && r <= 0xFAFF, // 兼容汉字
		r >= 0xFE30 && r <= 0xFE6F, // 兼容形式
		r >= 0xFF00 && r <= 0xFF60, // 全角
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1FAFF, // emoji
		r >= 0x20000 && r <= 0x3FFFD: // 扩展汉字
		return true
	}
	return false
}
