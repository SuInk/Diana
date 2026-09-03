// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strconv"
	"strings"
)

// 代码围栏在分条面前必须是一个整体。
//
// 分条按行切（splitReplyLines），长度兜底按字数切（chunkTextByLength），两层都不认
// ```——围栏于是被切进不同气泡：接收端看到的是半个围栏，反引号以字面量显示，代码也
// 不再等宽。以前所有平台都把 Markdown 降级成纯文本，围栏在进分条之前就被 markdownToPlain
// 删掉了，问题一直藏着；平台开始渲染富文本之后它才露出来。
//
// 做法是分条之前把整块围栏换成占位符，分完再填回去。占位符不含换行、不以「话没说完」
// 的标点结尾、也不长得像清单行，所以上面那两层都不会从它中间切开。

// codeFenceSentinel 用 NUL 包裹占位符：正常聊天文本里不会出现，不必担心撞上正文。
const codeFenceSentinel = "\x00"

func codeFencePlaceholder(index int) string {
	return codeFenceSentinel + "C" + strconv.Itoa(index) + codeFenceSentinel
}

// codeFenceIndex 认出占位符行，并取出它对应的围栏下标。
func codeFenceIndex(line string) (int, bool) {
	trimmed := strings.TrimSpace(line)
	prefix := codeFenceSentinel + "C"
	if !strings.HasPrefix(trimmed, prefix) || !strings.HasSuffix(trimmed, codeFenceSentinel) {
		return 0, false
	}
	index, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(trimmed, prefix), codeFenceSentinel))
	if err != nil || index < 0 {
		return 0, false
	}
	return index, true
}

// maskFencedCodeBlocks 把每一块围栏换成占位符，返回替换后的文本和被摘出来的围栏原文。
func maskFencedCodeBlocks(text string) (string, []string) {
	if !strings.Contains(text, "```") {
		return text, nil
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var out, blocks, current []string
	inFence := false
	for _, line := range strings.Split(normalized, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if !inFence {
				inFence = true
				current = []string{line}
				continue
			}
			current = append(current, line)
			blocks = append(blocks, strings.Join(current, "\n"))
			out = append(out, codeFencePlaceholder(len(blocks)-1))
			current, inFence = nil, false
			continue
		}
		if inFence {
			current = append(current, line)
			continue
		}
		out = append(out, line)
	}
	if inFence {
		// 围栏没闭合，通常是模型被 max_tokens 截断了。补一个收尾当成完整块处理，
		// 总比让它一路吞到结尾、把后面的正文也当成代码要好。
		current = append(current, "```")
		blocks = append(blocks, strings.Join(current, "\n"))
		out = append(out, codeFencePlaceholder(len(blocks)-1))
	}
	return strings.Join(out, "\n"), blocks
}

// restoreFencedCodeBlocks 把分条结果里的占位符填回围栏原文。
func restoreFencedCodeBlocks(segments, blocks []string, chunkSize int) []string {
	if len(blocks) == 0 {
		return segments
	}
	out := make([]string, 0, len(segments))
	for _, segment := range segments {
		for _, piece := range expandCodeFences(segment, blocks, chunkSize) {
			if strings.TrimSpace(piece) != "" {
				out = append(out, piece)
			}
		}
	}
	return out
}

// expandCodeFences 展开一条消息里的占位符。围栏本身放得下就跟正文待在同一条；
// 放不下的拆成几条，每条都是自成一体的完整围栏，并且和正文分开发。
func expandCodeFences(segment string, blocks []string, chunkSize int) []string {
	if !strings.Contains(segment, codeFenceSentinel) {
		return []string{segment}
	}
	var out, pending []string
	flush := func() {
		if joined := strings.Join(pending, "\n"); strings.TrimSpace(joined) != "" {
			out = append(out, joined)
		}
		pending = nil
	}
	for _, line := range strings.Split(segment, "\n") {
		index, ok := codeFenceIndex(line)
		if !ok || index >= len(blocks) {
			pending = append(pending, line)
			continue
		}
		pieces := splitFencedBlock(blocks[index], chunkSize)
		if len(pieces) == 1 {
			pending = append(pending, pieces[0])
			continue
		}
		flush()
		out = append(out, pieces...)
	}
	flush()
	return out
}

// splitFencedBlock 把超长的代码块拆成几段，每段都补齐首尾围栏。
//
// 不能按长度直接硬切：切出来的半个围栏在接收端就是一堆字面量反引号，正是这个文件
// 要修的那个毛病。按行切并给每段补上开合围栏，拆出来的每一条都还是能正常渲染的代码块。
func splitFencedBlock(block string, chunkSize int) []string {
	lines := strings.Split(block, "\n")
	if chunkSize <= 0 || len([]rune(block)) <= chunkSize || len(lines) < 3 {
		return []string{block}
	}
	opening := lines[0]
	// 正文预算要扣掉首尾两行围栏本身。
	budget := chunkSize - len([]rune(opening)) - len("\n```")
	if budget <= 0 {
		return []string{block}
	}
	seal := func(body []string) string {
		return opening + "\n" + strings.Join(body, "\n") + "\n```"
	}
	var out, current []string
	size := 0
	for _, line := range lines[1 : len(lines)-1] {
		length := len([]rune(line)) + 1
		if len(current) > 0 && size+length > budget {
			out = append(out, seal(current))
			current, size = nil, 0
		}
		current = append(current, line)
		size += length
	}
	if len(current) > 0 {
		out = append(out, seal(current))
	}
	if len(out) == 0 {
		return []string{block}
	}
	return out
}
