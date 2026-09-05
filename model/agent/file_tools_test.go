// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 写入必须锁在工作目录内。Pi 的对应工具明确不做包含性校验，绝对路径能写到任何地方；
// 放在一个群里一堆人说话的机器人上，那条线必须在。
func TestWriteFileStaysInsideTheWorkdir(t *testing.T) {
	root := t.TempDir()
	tool := &WriteFileTool{root: root, maxBytes: DefaultFileWriteMaxBytes}
	for _, rel := range []string{"../escape.txt", "a/../../escape.txt", filepath.Join(t.TempDir(), "abs.txt")} {
		if _, err := tool.Run(context.Background(), map[string]any{"path": rel, "content": "x"}); err == nil {
			t.Fatalf("write escaped the workdir via %q", rel)
		}
	}
}

func TestWriteFileCreatesParentDirsAndReportsOverwrite(t *testing.T) {
	root := t.TempDir()
	tool := &WriteFileTool{root: root, maxBytes: DefaultFileWriteMaxBytes}

	out, err := tool.Run(context.Background(), map[string]any{"path": "deep/nested/a.txt", "content": "hello\nworld"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(out), &first); err != nil {
		t.Fatal(err)
	}
	if first["overwrote"] != false {
		t.Fatalf("first write reported overwrote=%v", first["overwrote"])
	}
	data, err := os.ReadFile(filepath.Join(root, "deep", "nested", "a.txt"))
	if err != nil || string(data) != "hello\nworld" {
		t.Fatalf("file content = %q, err = %v", data, err)
	}

	out, err = tool.Run(context.Background(), map[string]any{"path": "deep/nested/a.txt", "content": "again"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(out), &second); err != nil {
		t.Fatal(err)
	}
	if second["overwrote"] != true {
		t.Fatal("overwriting an existing file was not reported as an overwrite")
	}
}

func TestWriteFileRejectsOversizedContent(t *testing.T) {
	root := t.TempDir()
	tool := &WriteFileTool{root: root, maxBytes: 16}
	if _, err := tool.Run(context.Background(), map[string]any{"path": "a.txt", "content": strings.Repeat("x", 17)}); err == nil {
		t.Fatal("oversized write was accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("a rejected write still created the file")
	}
}

// 命中多处必须整批拒绝。这是这个工具最重要的一条：默默改了第一处，调用方看不出
// 改错了地方，而文件已经变了。
func TestEditFileRejectsAmbiguousAndMissingMatches(t *testing.T) {
	root := t.TempDir()
	original := "alpha\nbeta\nalpha\n"
	writeTestFile(t, root, "a.txt", original)
	tool := &EditFileTool{root: root, maxBytes: DefaultFileWriteMaxBytes}

	_, err := tool.Run(context.Background(), map[string]any{
		"path":  "a.txt",
		"edits": []any{map[string]any{"old_text": "alpha", "new_text": "gamma"}},
	})
	if err == nil || !strings.Contains(err.Error(), "命中 2 处") {
		t.Fatalf("ambiguous edit error = %v", err)
	}

	_, err = tool.Run(context.Background(), map[string]any{
		"path":  "a.txt",
		"edits": []any{map[string]any{"old_text": "没有这一段", "new_text": "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "找不到") {
		t.Fatalf("missing edit error = %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != original {
		t.Fatalf("a rejected edit still touched the file: %q", data)
	}
}

// 多个替换都按原文件定位，不是依次生效——否则第二个替换会命中第一个刚写进去的内容。
func TestEditFileAppliesEveryEditAgainstTheOriginal(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "one\ntwo\nthree\n")
	tool := &EditFileTool{root: root, maxBytes: DefaultFileWriteMaxBytes}

	out, err := tool.Run(context.Background(), map[string]any{
		"path": "a.txt",
		"edits": []any{
			map[string]any{"old_text": "three", "new_text": "one"},
			map[string]any{"old_text": "one\n", "new_text": "ONE\n"},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	// 依次生效的话，第二个替换会命中第一个刚写进去的 "one"，结果就不是这个。
	if string(data) != "ONE\ntwo\none\n" {
		t.Fatalf("content = %q", data)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result["first_changed_line"].(float64) != 1 {
		t.Fatalf("first_changed_line = %v", result["first_changed_line"])
	}
}

func TestEditFileRejectsOverlappingEdits(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "abcdef\n")
	tool := &EditFileTool{root: root, maxBytes: DefaultFileWriteMaxBytes}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "a.txt",
		"edits": []any{
			map[string]any{"old_text": "abcd", "new_text": "x"},
			map[string]any{"old_text": "cdef", "new_text": "y"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatalf("overlapping edits error = %v", err)
	}
}

// BOM 和 CRLF 在没被替换的那部分里原样留着——因为替换是在原始字节上做的，
// 这条用例是防止以后有人在这里加「顺手规范化」。
func TestEditFilePreservesBOMAndCRLF(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "\ufeffone\r\ntwo\r\n")
	tool := &EditFileTool{root: root, maxBytes: DefaultFileWriteMaxBytes}
	if _, err := tool.Run(context.Background(), map[string]any{
		"path":  "a.txt",
		"edits": []any{map[string]any{"old_text": "two", "new_text": "2"}},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "\ufeffone\r\n2\r\n" {
		t.Fatalf("content = %q", data)
	}
}

// 模型会把 edits 塞成 JSON 字符串，或者只有一处改动时直接传单个对象。
func TestParseFileEditsToleratesModelShapes(t *testing.T) {
	for name, raw := range map[string]any{
		"JSON 字符串": `[{"old_text":"a","new_text":"b"}]`,
		"单个对象":     map[string]any{"old_text": "a", "new_text": "b"},
		"数组":       []any{map[string]any{"old_text": "a", "new_text": "b"}},
	} {
		edits, err := parseFileEdits(raw)
		if err != nil || len(edits) != 1 || edits[0].oldText != "a" || edits[0].newText != "b" {
			t.Fatalf("%s: parseFileEdits = %#v, err = %v", name, edits, err)
		}
	}
	if _, err := parseFileEdits([]any{}); err == nil {
		t.Fatal("empty edits were accepted")
	}
	if _, err := parseFileEdits([]any{map[string]any{"old_text": "", "new_text": "x"}}); err == nil {
		t.Fatal("an empty old_text was accepted")
	}
}

func TestGrepFindsMatchesAndRespectsGlob(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "src/a.go", "package main\nfunc Target() {}\n")
	writeTestFile(t, root, "src/b.ts", "const Target = 1\n")
	writeTestFile(t, root, "notes.md", "Target 出现在文档里\n")
	tool := &GrepTool{root: root, maxBytes: DefaultReadFileMaxBytes}

	out, err := tool.Run(context.Background(), map[string]any{"pattern": "Target"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{"src/a.go:2", "src/b.ts:1", "notes.md:1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("grep output missing %q:\n%s", want, out)
		}
	}

	out, err = tool.Run(context.Background(), map[string]any{"pattern": "Target", "glob": "*.go"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(out, "src/a.go") || strings.Contains(out, "src/b.ts") {
		t.Fatalf("glob was not applied:\n%s", out)
	}
}

func TestGrepLiteralAndIgnoreCase(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "price is a.b\nPRICE\n")
	tool := &GrepTool{root: root, maxBytes: DefaultReadFileMaxBytes}

	// 字面量模式下 a.b 里的点不再是「任意字符」。
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "a.b", "literal": true})
	if err != nil || !strings.Contains(out, "a.txt:1") {
		t.Fatalf("literal grep = %q, err = %v", out, err)
	}
	out, err = tool.Run(context.Background(), map[string]any{"pattern": "axb", "literal": true})
	if err != nil || strings.Contains(out, "a.txt:1") {
		t.Fatalf("literal grep matched a regex wildcard: %q", out)
	}
	out, err = tool.Run(context.Background(), map[string]any{"pattern": "price", "ignore_case": true})
	if err != nil || !strings.Contains(out, "a.txt:2") {
		t.Fatalf("ignore_case grep = %q, err = %v", out, err)
	}
}

// 二进制文件被跳过而不是把乱码灌进上下文。
func TestGrepSkipsBinaryFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "bin.dat", "needle\x00\x01\x02")
	writeTestFile(t, root, "text.txt", "needle\n")
	tool := &GrepTool{root: root, maxBytes: DefaultReadFileMaxBytes}
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(out, "bin.dat") {
		t.Fatalf("binary file was searched:\n%s", out)
	}
	if !strings.Contains(out, "text.txt") || !strings.Contains(out, "跳过 1 个") {
		t.Fatalf("output = %q", out)
	}
}

func TestGrepRejectsAnInvalidRegex(t *testing.T) {
	tool := &GrepTool{root: t.TempDir(), maxBytes: DefaultReadFileMaxBytes}
	if _, err := tool.Run(context.Background(), map[string]any{"pattern": "("}); err == nil {
		t.Fatal("an invalid regex was accepted")
	}
}

func TestFindFilesMatchesNameAndPathPatterns(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "src/deep/a.ts", "")
	writeTestFile(t, root, "src/b.ts", "")
	writeTestFile(t, root, "vendor/c.ts", "")
	tool := &FindFilesTool{root: root}

	out, err := tool.Run(context.Background(), map[string]any{"pattern": "*.ts"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{"src/deep/a.ts", "src/b.ts", "vendor/c.ts"} {
		if !strings.Contains(out, want) {
			t.Fatalf("find missing %q:\n%s", want, out)
		}
	}

	out, err = tool.Run(context.Background(), map[string]any{"pattern": "src/**/*.ts"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(out, "src/deep/a.ts") || !strings.Contains(out, "src/b.ts") {
		t.Fatalf("** did not span zero and more directories:\n%s", out)
	}
	if strings.Contains(out, "vendor/c.ts") {
		t.Fatalf("path pattern leaked outside its prefix:\n%s", out)
	}
}

func TestMatchGlobPath(t *testing.T) {
	for _, tc := range []struct {
		pattern, rel string
		want         bool
	}{
		{"*.go", "src/deep/a.go", true},
		{"*.go", "src/deep/a.ts", false},
		{"src/*.go", "src/a.go", true},
		{"src/*.go", "src/deep/a.go", false},
		{"src/**/*.go", "src/a.go", true},
		{"src/**/*.go", "src/deep/deeper/a.go", true},
		{"**/*.go", "a.go", true},
		{"src/**", "src/a/b/c.go", true},
		{"", "a.go", false},
	} {
		if got := matchGlobPath(tc.pattern, tc.rel); got != tc.want {
			t.Fatalf("matchGlobPath(%q, %q) = %v, want %v", tc.pattern, tc.rel, got, tc.want)
		}
	}
}

// .git 不参与遍历：体积大、内容是打包过的二进制，检索它既慢又没有意义。
func TestWalkSkipsGitDirectory(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".git/objects/pack.idx", "needle")
	writeTestFile(t, root, "a.txt", "needle")
	tool := &GrepTool{root: root, maxBytes: DefaultReadFileMaxBytes}
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(out, ".git") {
		t.Fatalf(".git was walked:\n%s", out)
	}
}

func TestClipRunesDoesNotSplitMultibyteCharacters(t *testing.T) {
	if got := clipRunes("一二三四五", 3); got != "一二三…" {
		t.Fatalf("clipRunes = %q", got)
	}
	if got := clipRunes("abc", 10); got != "abc" {
		t.Fatalf("clipRunes = %q", got)
	}
}

// 写入工具是单独一档权限：开关关着的时候它们根本不注册，检索和读取不受影响。
func TestFileWriteToolsAreGatedByConfig(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		registry, err := NewDefaultToolRegistry(Config{WorkDir: t.TempDir(), FileWriteEnabled: enabled})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"write_file", "edit_file"} {
			if _, ok := registry.Get(name); ok != enabled {
				t.Fatalf("FileWriteEnabled=%v: %s registered = %v", enabled, name, ok)
			}
		}
		for _, name := range []string{"read_file", "list_files", "grep", "find_files"} {
			if _, ok := registry.Get(name); !ok {
				t.Fatalf("FileWriteEnabled=%v: read-only tool %s went missing", enabled, name)
			}
		}
	}
}
