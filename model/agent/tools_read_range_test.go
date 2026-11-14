// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeNumberedFile(t *testing.T, root, name string, lines int) {
	t.Helper()
	body := make([]string, 0, lines)
	for index := 1; index <= lines; index++ {
		body = append(body, fmt.Sprintf("line-%04d", index))
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(strings.Join(body, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 一个几千行的文件，以前只能从头读、读到 runner 的截断处为止，剩下的再也够不着。
// 现在要能按 offset 翻到文件任意位置，并且明确告诉模型下一段从哪开始。
func TestReadFileToolReadsRangesAndPointsAtTheRest(t *testing.T) {
	root := t.TempDir()
	writeNumberedFile(t, root, "big.txt", 1000)
	tool := &ReadFileTool{root: root}

	// 不传 limit 时读到单次上限为止，按整行切，并说清下一段从哪行起。
	budget := 2000
	first, err := tool.Run(WithToolOutputBudget(context.Background(), budget), map[string]any{"path": "big.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(first)) > budget {
		t.Fatalf("result %d 字，超过上限 %d", len([]rune(first)), budget)
	}
	if !strings.Contains(first, "共 1000 行") {
		t.Fatalf("total line count missing: %s", firstLines(first, 3))
	}
	body := first[strings.Index(first, "\n\n")+2:]
	lines := strings.Split(body, "\n")
	last := lines[len(lines)-1]
	if lines[0] != "line-0001" || len(last) != len("line-0000") {
		t.Fatalf("window not cut on whole lines: first=%q last=%q", lines[0], last)
	}
	var end int
	fmt.Sscanf(last, "line-%d", &end)
	if end != len(lines) || end >= 1000 {
		t.Fatalf("window wrong: %d lines, last=%q", len(lines), last)
	}
	// 关键：不能只说「截断了」，得说清怎么接着读。
	if !strings.Contains(first, fmt.Sprintf("offset=%d", end+1)) {
		t.Fatalf("continuation offset missing: %s", firstLines(first, 3))
	}
	// 没有条数硬上限：预算够时一次读完整个文件。
	whole, err := tool.Run(context.Background(), map[string]any{"path": "big.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(whole, "line-1000") || strings.Contains(whole, "还有") {
		t.Fatalf("whole file not returned: %s", firstLines(whole, 3))
	}

	// 文件尾部以前完全够不着，现在要能直接跳过去。
	tail, err := tool.Run(context.Background(), map[string]any{"path": "big.txt", "offset": 990, "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tail, "line-1000") || strings.Contains(tail, "line-0989") {
		t.Fatalf("tail window wrong: %s", firstLines(tail, 3))
	}
	if strings.Contains(tail, "还有") {
		t.Fatalf("last window should not advertise more lines: %s", firstLines(tail, 3))
	}

	past, err := tool.Run(context.Background(), map[string]any{"path": "big.txt", "offset": 5000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(past, "越过文件末尾") {
		t.Fatalf("out-of-range offset not reported: %s", past)
	}
}

// 同样的 8000 字预算，正文里能装下多少真正的文件内容。
// 以前整包 JSON.MarshalIndent，换行被转义成 \n、外加信封和缩进，白白吃掉一截。
func TestReadFileToolSpendsBudgetOnContentNotEnvelope(t *testing.T) {
	root := t.TempDir()
	writeNumberedFile(t, root, "src.txt", 400)
	tool := &ReadFileTool{root: root}

	out, err := tool.Run(context.Background(), map[string]any{"path": "src.txt", "limit": 300})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `\n`) || strings.Contains(out, `"content"`) {
		t.Fatalf("output still wrapped in escaped JSON: %s", firstLines(out, 3))
	}
	header := strings.Index(out, "\n\n")
	if header < 0 {
		t.Fatalf("header/body separator missing: %s", firstLines(out, 3))
	}
	// 表头只占一两行，剩下的都该是正文。
	if overhead := header + 2; overhead > 120 {
		t.Fatalf("header overhead too large: %d 字符", overhead)
	}
	if got := strings.Count(out[header+2:], "\n") + 1; got != 300 {
		t.Fatalf("expected 300 content lines, got %d", got)
	}
}

func firstLines(value string, count int) string {
	lines := strings.Split(value, "\n")
	if len(lines) > count {
		lines = lines[:count]
	}
	return strings.Join(lines, "\n")
}

// 一行就超过上限的文件（压缩过的 JS）按字符给开头一段，并说明这一行没给全。
func TestReadFileToolExplainsOverlongLine(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "min.js"), []byte(strings.Repeat("一", 5000)+"\nnext\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := &ReadFileTool{root: root}
	out, err := tool.Run(WithToolOutputBudget(context.Background(), 1000), map[string]any{"path": "min.js"})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(out)) > 1000 || !strings.Contains(out, "只给了开头一段") || !strings.Contains(out, "offset=2") {
		t.Fatalf("overlong line not explained (%d 字): %s", len([]rune(out)), firstLines(out, 3))
	}
}
