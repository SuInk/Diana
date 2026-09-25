// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"sort"
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

// collapseReplyBlankLinesOutsideCode adapts Markdown paragraph spacing to
// plain-text chat bubbles while leaving code samples byte-for-byte intact.
func collapseReplyBlankLinesOutsideCode(text string) string {
	masked, blocks := maskFencedCodeBlocks(text)
	masked = collapseBlankLines(masked)
	for index, block := range blocks {
		masked = strings.ReplaceAll(masked, codeFencePlaceholder(index), block)
	}
	return masked
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
	if chunkSize <= 0 || len([]rune(block)) <= chunkSize {
		return []string{block}
	}
	pieces := splitFencedBlockToFit(block, func(piece string) bool { return len([]rune(piece)) <= chunkSize })
	if len(pieces) == 0 {
		return []string{block}
	}
	return pieces
}

// splitFencedBlockToFit 是按「放不放得下」拆围栏的通用版：fits 由调用方给，
// 可以是字数，也可以是平台渲染后的容量（Telegram 按 UTF-16 算，HTML 转义还会变长），
// 所以这里不自己估算围栏开销，每一段都补齐围栏之后整段去问 fits。
//
// 优先在行边界切；只有一行本身就放不下时才在这一行中间硬切——代码行被拆开总比整条
// 回复发不出去好，而且每段仍是完整围栏，复制回去拼起来就是原文。连一个字符加上
// 围栏都放不下时返回 nil，由调用方决定怎么报错。
func splitFencedBlockToFit(block string, fits func(string) bool) []string {
	lines := strings.Split(block, "\n")
	if len(lines) < 3 {
		return nil
	}
	// 收尾沿用原来的那一行：开头是四个反引号时，收尾也得是四个，否则接收端配不上对。
	opening, closing := lines[0], lines[len(lines)-1]
	seal := func(body ...string) string {
		return opening + "\n" + strings.Join(body, "\n") + "\n" + closing
	}
	var out, current []string
	for _, line := range lines[1 : len(lines)-1] {
		candidate := append(append([]string(nil), current...), line)
		if fits(seal(candidate...)) {
			current = candidate
			continue
		}
		if len(current) > 0 {
			out = append(out, seal(current...))
			current = nil
		}
		runes := []rune(line)
		for !fits(seal(string(runes))) {
			// 前缀越长越放不下，二分找出这一段最多能装多少个字符。
			cut := sort.Search(len(runes), func(n int) bool { return !fits(seal(string(runes[:n+1]))) })
			if cut == 0 {
				return nil
			}
			out = append(out, seal(string(runes[:cut])))
			runes = runes[cut:]
		}
		// 硬切剩下的尾巴留在当前段里，后面的短行还能接着装进来。
		current = []string{string(runes)}
	}
	if len(current) > 0 {
		out = append(out, seal(current...))
	}
	return out
}
