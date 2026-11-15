// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFitLinesCutOnWholeLines(t *testing.T) {
	text := "aaa\nbbb\nccc\n"
	if n := fitLeadingLines(text, 9, false); text[:n] != "aaa\nbbb\n" {
		t.Fatalf("leading = %q", text[:n])
	}
	if n := fitTrailingLines(text, 9, false); text[n:] != "bbb\nccc\n" {
		t.Fatalf("trailing = %q", text[n:])
	}
	// 一行就装不下时按字符切，不切坏多字节字符。
	if n := fitLeadingLines("一二三四五\n", 3, false); "一二三四五"[:n] != "一二三" {
		t.Fatalf("partial leading = %d", n)
	}
	// 按 JSON 转义计：换行、引号占 2，<> 占 6。
	if got := TextCost("a\n\"<", true); got != 1+2+2+6 {
		t.Fatalf("json cost = %d", got)
	}
}

func TestFitEntriesFindsLargestPage(t *testing.T) {
	out, n, err := fitEntries(100, 50, func(n int) (string, error) { return strings.Repeat("x", n*3), nil })
	if err != nil || n != 16 || len(out) != 48 {
		t.Fatalf("n=%d len=%d err=%v", n, len(out), err)
	}
	// 一条都装不下时仍给第一条，交给 Runner 兜底并说明。
	if _, n, _ := fitEntries(5, 2, func(n int) (string, error) { return strings.Repeat("x", n*3), nil }); n != 1 {
		t.Fatalf("n=%d", n)
	}
}

// 失败和成功一样分页：给尾部整行，完整输出存进工作区 tmp/，read_file 读得到。
func TestRunCommandPagesLongOutputBothWays(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	for _, exit := range []string{"0", "7"} {
		root := t.TempDir()
		tool := &RunCommandTool{root: root, timeout: 10 * time.Second, sandboxMode: CommandSandboxOff}
		ctx := WithToolOutputBudget(context.Background(), 4000)
		raw, err := tool.Run(ctx, map[string]any{"command": "sh", "args": []any{"-c", "i=0; while [ $i -lt 3000 ]; do echo \"line $i\"; i=$((i+1)); done; exit " + exit}})
		if exit != "0" {
			if err == nil {
				t.Fatal("expected failure")
			}
			raw = err.Error()
		} else if err != nil {
			t.Fatal(err)
		}
		if utf8.RuneCountInString(raw) > 4000 {
			t.Fatalf("exit %s: result %d 字，超过上限", exit, utf8.RuneCountInString(raw))
		}
		var result struct {
			Output         string `json:"output"`
			Truncated      bool   `json:"truncated"`
			Notice         string `json:"notice"`
			FullOutputPath string `json:"full_output_path"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Truncated || !strings.HasPrefix(result.Output, "line ") || !strings.HasSuffix(result.Output, "line 2999\n") {
			t.Fatalf("exit %s: tail page wrong: %q…", exit, result.Output[:min(30, len(result.Output))])
		}
		if !strings.Contains(result.Notice, "共 3000 行") || !strings.Contains(result.Notice, "read_file path="+result.FullOutputPath) {
			t.Fatalf("exit %s: notice = %q", exit, result.Notice)
		}
		// 完整输出按 notice 说的读法读得到，第一行也在。
		read, err := (&ReadFileTool{root: root}).Run(ctx, map[string]any{"path": result.FullOutputPath, "limit": 2})
		if err != nil || !strings.Contains(read, "共 3000 行") || !strings.Contains(read, "line 0\nline 1") {
			t.Fatalf("exit %s: full output unreadable: %v %s", exit, err, read)
		}
	}
}

// 装得下的输出原样给全，不落盘。
func TestRunCommandShortOutputIsComplete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	root := t.TempDir()
	tool := &RunCommandTool{root: root, timeout: 10 * time.Second, sandboxMode: CommandSandboxOff}
	raw, err := tool.Run(context.Background(), map[string]any{"command": "sh", "args": []any{"-c", "printf 'a<b>\\n'"}})
	if err != nil || !strings.Contains(raw, `"truncated": false`) || strings.Contains(raw, "full_output_path") {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
	if _, err := os.Stat(workspaceCommandOutputDir(root)); !os.IsNotExist(err) {
		t.Fatalf("short output spilled to disk: %v", err)
	}
}

var nextOffsetPattern = regexp.MustCompile(`offset=(\d+)`)

// 按 offset 一页页接着查，拼起来正好是全部匹配，一条不缺、不重。
func TestGrepPagesAllMatchesWithoutHardCap(t *testing.T) {
	root := t.TempDir()
	var body strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&body, "needle %04d %s\n", i, strings.Repeat("长", 400)) // 单行也不再截到 300 字
	}
	writeTestFile(t, root, "big.txt", body.String())
	tool := &GrepTool{root: root}
	ctx := WithToolOutputBudget(context.Background(), 20000)
	seen, offset := 0, 0
	for page := 0; ; page++ {
		out, err := tool.Run(ctx, map[string]any{"pattern": "needle", "offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		if utf8.RuneCountInString(out) > 20000 {
			t.Fatalf("page %d: %d 字超过上限", page, utf8.RuneCountInString(out))
		}
		if !strings.Contains(out, "共 3000 条匹配") {
			t.Fatalf("page %d header: %s", page, firstLines(out, 1))
		}
		for _, line := range strings.Split(out, "\n")[1:] {
			want := fmt.Sprintf("big.txt:%d: needle %04d %s", seen+1, seen, strings.Repeat("长", 400))
			if line != want {
				t.Fatalf("page %d: match %d = %.40q", page, seen, line)
			}
			seen++
		}
		match := nextOffsetPattern.FindStringSubmatch(firstLines(out, 1))
		if match == nil {
			break
		}
		offset, _ = strconv.Atoi(match[1])
	}
	if seen != 3000 {
		t.Fatalf("read %d of 3000 matches", seen)
	}
}

func TestFindFilesAndListFilesPageEverything(t *testing.T) {
	root := t.TempDir()
	for i := range 450 {
		writeTestFile(t, root, filepath.Join("d", fmt.Sprintf("f%03d.txt", i)), "x")
	}
	ctx := WithToolOutputBudget(context.Background(), 2000)
	find := &FindFilesTool{root: root}
	seen, offset := map[string]bool{}, 0
	for {
		out, err := find.Run(ctx, map[string]any{"pattern": "*.txt", "offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(out, "\n")[1:] {
			seen[line] = true
		}
		match := nextOffsetPattern.FindStringSubmatch(firstLines(out, 1))
		if match == nil {
			break
		}
		offset, _ = strconv.Atoi(match[1])
	}
	if len(seen) != 450 {
		t.Fatalf("find_files saw %d of 450", len(seen))
	}

	list := &ListFilesTool{root: root}
	names, offset := map[string]bool{}, 0
	for {
		out, err := list.Run(ctx, map[string]any{"path": "d", "offset": offset})
		if err != nil {
			t.Fatal(err)
		}
		if utf8.RuneCountInString(out) > 2000 {
			t.Fatalf("list_files page %d 字超过上限", utf8.RuneCountInString(out))
		}
		var page struct {
			Total      int `json:"total"`
			NextOffset int `json:"next_offset"`
			Entries    []struct {
				Name string `json:"name"`
			} `json:"entries"`
		}
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatal(err)
		}
		for _, entry := range page.Entries {
			names[entry.Name] = true
		}
		if page.Total != 450 || page.NextOffset == 0 {
			break
		}
		offset = page.NextOffset
	}
	if len(names) != 450 {
		t.Fatalf("list_files saw %d of 450 (以前固定 200 条)", len(names))
	}
}

// 单个大文件里大量匹配：超出这一页的只计数；总数照实给出，续读 offset 接得上。
func TestGrepManyMatchesInOneFile(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "huge.log", strings.Repeat("hit\n", 200000))
	tool := &GrepTool{root: root}
	out, err := tool.Run(WithToolOutputBudget(context.Background(), 2000), map[string]any{"pattern": "hit", "context": 1})
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(out) > 2000 || !strings.Contains(out, "共 200000 条匹配") || nextOffsetPattern.FindStringSubmatch(firstLines(out, 1)) == nil {
		t.Fatalf("(%d 字) %s", utf8.RuneCountInString(out), firstLines(out, 3))
	}
}

// 超长单行：匹配在行尾也能找到（流式匹配），显示时按字切开并写明 read_file 的 char_offset。
func TestGrepOverlongLine(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("长", 300000) + "needle"
	writeTestFile(t, root, "one.txt", "head\n"+line+"\n")
	tool := &GrepTool{root: root}
	out, err := tool.Run(WithToolOutputBudget(context.Background(), 4000), map[string]any{"pattern": "needle$"})
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(out) > 4000 || !strings.Contains(out, "共 1 条匹配") || !strings.Contains(out, "one.txt:2:") {
		t.Fatalf("(%d 字) %.200s", utf8.RuneCountInString(out), out)
	}
	if !regexp.MustCompile(`read_file path=one.txt offset=2 char_offset=\d+`).MatchString(out) {
		t.Fatalf("no char_offset continuation: %.300s", out[len(out)-300:])
	}
}
