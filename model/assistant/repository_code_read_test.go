// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
)

// repositoryCodeReadTestGitHub 在 PR 假服务外面补上提交、对比、目录树、大文件和目录路径。
type repositoryCodeReadTestGitHub struct {
	*repositoryPullRequestTestGitHub
	bigFile     string
	commitFiles []githubPullRequestFile
}

func newRepositoryCodeReadTestGitHub() *repositoryCodeReadTestGitHub {
	var big strings.Builder
	for i := 1; i <= 3_000; i++ {
		fmt.Fprintf(&big, "const value%d = \"<%d & %d>\"\n", i, i, i)
	}
	return &repositoryCodeReadTestGitHub{repositoryPullRequestTestGitHub: newRepositoryPullRequestTestGitHub(), bigFile: big.String()}
}

func (s *repositoryCodeReadTestGitHub) handler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+repositoryPublishTestToken {
		s.repositoryPullRequestTestGitHub.handler(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/repos/acme/demo":
		_ = json.NewEncoder(w).Encode(map[string]any{"private": false, "full_name": "acme/demo", "default_branch": "main"})
	case r.URL.Path == "/repos/acme/demo/contents/big.js":
		// 超过 1MB 的文件：默认格式只给元信息，要原始格式才给内容。
		if r.Header.Get("Accept") == "application/vnd.github.raw+json" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(s.bigFile))
			return
		}
		_ = json.NewEncoder(w).Encode(githubRepositoryContent{Type: "file", Path: "big.js", Encoding: "none", Size: len(s.bigFile)})
	case r.URL.Path == "/repos/acme/demo/contents/docs":
		_ = json.NewEncoder(w).Encode([]githubRepositoryContent{{Type: "file", Path: "docs/a.md"}})
	case strings.HasPrefix(r.URL.Path, "/repos/acme/demo/commits/"):
		if strings.TrimPrefix(r.URL.Path, "/repos/acme/demo/commits/") != "deadbeef" {
			http.NotFound(w, r)
			return
		}
		detail := githubCommitDetail{SHA: "deadbeefcafe0123456789", HTMLURL: "https://github.com/acme/demo/commit/deadbeefcafe0123456789", Files: s.commitFiles}
		detail.Commit.Message = "fix: 长文本分页\n\n正文"
		detail.Commit.Author.Name = "Su"
		detail.Stats.Additions = 10
		_ = json.NewEncoder(w).Encode(detail)
	case r.URL.Path == "/repos/acme/demo/compare/v1.0...feature/x":
		compare := githubCompare{Status: "ahead", AheadBy: 25, TotalCommits: 25, Files: s.commitFiles}
		for i := 1; i <= 25; i++ {
			commit := githubCommitDetail{SHA: fmt.Sprintf("%040d", i)}
			commit.Commit.Message = fmt.Sprintf("commit %d\n\nbody", i)
			compare.Commits = append(compare.Commits, commit)
		}
		_ = json.NewEncoder(w).Encode(compare)
	case r.URL.Path == "/repos/acme/demo/git/trees/main":
		if r.URL.Query().Get("recursive") != "1" {
			http.NotFound(w, r)
			return
		}
		tree := githubGitTree{SHA: "tree"}
		add := func(path, kind string, size int64) {
			tree.Tree = append(tree.Tree, struct {
				Path string `json:"path"`
				Type string `json:"type"`
				Size int64  `json:"size"`
			}{path, kind, size})
		}
		add("README.md", "blob", 2048)
		add("go.mod", "blob", 120)
		add("model", "tree", 0)
		add("model/agent", "tree", 0)
		add("model/agent/runner.go", "blob", 50_000)
		add("model/agent/types.go", "blob", 3_000)
		add("model/assistant", "tree", 0)
		for i := 0; i < 400; i++ {
			add(fmt.Sprintf("model/assistant/file%03d.go", i), "blob", 1_000)
		}
		_ = json.NewEncoder(w).Encode(tree)
	default:
		s.repositoryPullRequestTestGitHub.handler(w, r)
	}
}

// runRepositoryToolWithBudget 模拟 Runner：带着单次上限调用，核对结果没超上限、是完整的 JSON。
func runRepositoryToolWithBudget(t *testing.T, tool *dianaGitHubTool, budget int, input map[string]any) repositoryIssueResult {
	t.Helper()
	raw, err := tool.Run(agent.WithToolOutputBudget(context.Background(), budget), input)
	if err != nil {
		t.Fatal(err)
	}
	if got := utf8.RuneCountInString(raw); got > budget {
		t.Fatalf("结果 %d 字超出单次上限 %d，Runner 会从中间截断", got, budget)
	}
	var result repositoryIssueResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode result %q: %v", raw, err)
	}
	return result
}

// read_file 按 Runner 给的上限装整行，续读位置按真正给出的内容算，一路续读下来不漏行。
func TestRepositoryReadFileFitsRunnerBudgetAndResumes(t *testing.T) {
	github := newRepositoryCodeReadTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "读 big.js", nil)

	var collected strings.Builder
	start, calls := 1, 0
	for {
		calls++
		if calls > 100 {
			t.Fatal("续读没有收敛")
		}
		result := runRepositoryToolWithBudget(t, tool, 8_000, map[string]any{"operation": "read_file", "repository": "acme/demo", "path": "big.js", "start_line": start, "end_line": start + repositoryFileMaxLines - 1})
		if !result.OK || result.File == nil || result.File.StartLine != start || result.File.TotalLines != 3_000 {
			t.Fatalf("result=%#v", result)
		}
		for _, line := range strings.Split(strings.TrimSuffix(result.File.Content, "\n"), "\n") {
			_, code, _ := strings.Cut(line, "| ")
			collected.WriteString(code + "\n")
		}
		if result.File.EndLine == 3_000 {
			if strings.Contains(result.Message, "start_line=") {
				t.Fatalf("读完了不该再提示续读：%q", result.Message)
			}
			break
		}
		want := fmt.Sprintf("start_line=%d", result.File.EndLine+1)
		if !strings.Contains(result.Message, want) || !strings.Contains(result.Message, "装不下") {
			t.Fatalf("续读提示应按真正给出的行算（%s）：%q", want, result.Message)
		}
		start = result.File.EndLine + 1
	}
	if collected.String() != github.bigFile {
		t.Fatal("按提示续读拼回来的内容和原文件不一致")
	}
	if calls < 3 {
		t.Fatalf("8000 字的上限应该要分几次读完，got %d 次", calls)
	}

	raw, err := tool.Run(context.Background(), map[string]any{"operation": "read_file", "repository": "acme/demo", "path": "big.js"})
	if err != nil || !strings.Contains(raw, `"<1 & 1>`) || strings.Contains(raw, `\u0026`) {
		t.Fatalf("代码里的 & < > 不该被转义成六个字符：%.200s", raw)
	}

	dir := runRepositoryToolWithBudget(t, tool, 8_000, map[string]any{"operation": "read_file", "repository": "acme/demo", "path": "docs"})
	if dir.OK || !strings.Contains(dir.Message, "list_files") {
		t.Fatalf("目录路径应提示用 list_files：%#v", dir)
	}
}

// 一行就超出上限（压缩过的代码）时给开头一段，这一行算读过，续读不会卡在原地。
func TestRepositoryFillLinesCutsOverlongLine(t *testing.T) {
	lines := []string{strings.Repeat("x", 500), "next"}
	window := repositoryFillLines(len(lines), func(i int) string { return lines[i] }, len(lines), 100)
	if !window.Cut || window.Count != 1 || repositoryJSONRunes(window.Text) > 100 {
		t.Fatalf("window=%#v", window)
	}
	if got := repositoryJSONRunes("a\"\n<\x01中"); got != 1+2+2+1+6+1 {
		t.Fatalf("JSON 字数算错：%d", got)
	}
}

// pull_files 在上限内分页：文件清单按 file_offset 续列，单个文件的 patch 按 patch_line 续读，
// 从 hunk 中间接着读时带上 hunk 头。
func TestRepositoryPullFilesPagesWithinBudget(t *testing.T) {
	github := newRepositoryCodeReadTestGitHub()
	var patch strings.Builder
	patch.WriteString("@@ -1,3 +1,900 @@\n")
	for i := 1; i <= 900; i++ {
		fmt.Fprintf(&patch, "+func added%d() { return \"%d\" }\n", i, i)
	}
	github.files = append(github.files, githubPullRequestFile{Filename: "cmd/app.go", Status: "modified", Additions: 900, Patch: strings.TrimSuffix(patch.String(), "\n")})
	for i := 0; i < 250; i++ {
		github.files = append(github.files, githubPullRequestFile{Filename: fmt.Sprintf("web/src/components/generated/file%03d.ts", i), Status: "modified", Additions: 1, Patch: "@@ -1 +1 @@\n-a\n+b"})
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "review acme/demo PR 85", nil)

	seen := map[string]bool{}
	offset, calls := 0, 0
	for {
		calls++
		if calls > 20 {
			t.Fatal("文件清单续列没有收敛")
		}
		input := map[string]any{"operation": "pull_files", "repository": "acme/demo", "number": 85}
		if offset > 0 {
			input["file_offset"] = offset
		}
		result := runRepositoryToolWithBudget(t, tool, 8_000, input)
		if !result.OK || len(result.Files) == 0 {
			t.Fatalf("result=%#v", result)
		}
		for _, file := range result.Files {
			if seen[file.Path] {
				t.Fatalf("文件 %s 列了两次", file.Path)
			}
			seen[file.Path] = true
		}
		if !result.FilesTruncated {
			break
		}
		if !strings.Contains(result.Message, fmt.Sprintf("file_offset=%d", result.NextFileOffset)) {
			t.Fatalf("续列提示：%q", result.Message)
		}
		offset = result.NextFileOffset
	}
	if len(seen) != len(github.files) || calls < 2 {
		t.Fatalf("按 file_offset 续列应该列全 %d 个文件，got %d（%d 次）", len(github.files), len(seen), calls)
	}

	var lines []string
	patchLine := 0
	for calls = 0; ; {
		calls++
		if calls > 20 {
			t.Fatal("patch 续读没有收敛")
		}
		input := map[string]any{"operation": "pull_files", "repository": "acme/demo", "number": 85, "paths": []any{"cmd/app.go"}}
		if patchLine > 0 {
			input["patch_line"] = patchLine
		}
		result := runRepositoryToolWithBudget(t, tool, 8_000, input)
		if !result.OK || len(result.Files) != 1 {
			t.Fatalf("result=%#v", result)
		}
		file := result.Files[0]
		if patchLine > 0 && file.PatchHunk != "@@ -1,3 +1,900 @@" {
			t.Fatalf("从 hunk 中间续读要带上 hunk 头：%q", file.PatchHunk)
		}
		lines = append(lines, strings.Split(file.Patch, "\n")...)
		if !file.PatchTruncated {
			break
		}
		if file.PatchTotalLines != 901 || !strings.Contains(result.Message, fmt.Sprintf("patch_line=%d", file.PatchEndLine+1)) {
			t.Fatalf("file=%#v message=%q", file, result.Message)
		}
		patchLine = file.PatchEndLine + 1
	}
	if strings.Join(lines, "\n") != github.files[0].Patch || calls < 2 {
		t.Fatalf("按 patch_line 续读拼回来的 patch 和原文不一致（%d 次）", calls)
	}

	multi := runRepositoryToolWithBudget(t, tool, 8_000, map[string]any{"operation": "pull_files", "repository": "acme/demo", "number": 85, "paths": []any{"web"}, "patch_line": 2})
	if multi.OK || multi.FailureCode != "invalid_input" {
		t.Fatalf("patch_line 配多个文件应报错：%#v", multi)
	}
}

// commit_files 读单个提交，compare_files 读两个版本之间，都走和 pull_files 一样的分页。
func TestRepositoryCommitAndCompareFiles(t *testing.T) {
	github := newRepositoryCodeReadTestGitHub()
	github.commitFiles = []githubPullRequestFile{
		{Filename: "model/agent/runner.go", Status: "modified", Additions: 3, Deletions: 1, Patch: "@@ -600,3 +600,5 @@\n-old\n+new"},
		{Filename: "logo.png", Status: "added"},
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "看下这个提交", nil)

	commit := runRepositoryToolWithBudget(t, tool, 8_000, map[string]any{"operation": "commit", "repository": "https://github.com/acme/demo", "ref": "deadbeef"})
	if !commit.OK || commit.Operation != "commit_files" || commit.Commit == nil || commit.Commit.Author != "Su" || !strings.HasPrefix(commit.Commit.Message, "fix: 长文本分页") {
		t.Fatalf("commit=%#v", commit)
	}
	if len(commit.Files) != 2 || commit.Files[0].Patch == "" || !commit.Files[1].PatchUnavailable || !strings.Contains(commit.Message, "deadbeefcafe") {
		t.Fatalf("files=%#v message=%q", commit.Files, commit.Message)
	}
	missing := runRepositoryToolWithBudget(t, tool, 8_000, map[string]any{"operation": "commit_files", "repository": "acme/demo", "ref": "nope"})
	if missing.OK || missing.FailureCode != "not_found" {
		t.Fatalf("missing=%#v", missing)
	}

	compare := runRepositoryToolWithBudget(t, tool, 8_000, map[string]any{"operation": "compare_files", "repository": "acme/demo", "ref": "v1.0...feature/x"})
	if !compare.OK || compare.Comparison == nil || compare.Comparison.Base != "v1.0" || compare.Comparison.Head != "feature/x" {
		t.Fatalf("compare=%#v", compare)
	}
	if len(compare.Comparison.Commits) != repositoryCompareCommitLimit || !compare.Comparison.CommitsTruncated || !strings.HasSuffix(compare.Comparison.Commits[len(compare.Comparison.Commits)-1], " commit 25") {
		t.Fatalf("commits=%#v", compare.Comparison.Commits)
	}
	if len(compare.Files) != 2 {
		t.Fatalf("files=%#v", compare.Files)
	}
	incomplete := runRepositoryToolWithBudget(t, tool, 8_000, map[string]any{"operation": "compare_files", "repository": "acme/demo", "base": "v1.0"})
	if incomplete.OK || incomplete.FailureCode != "invalid_input" {
		t.Fatalf("缺 head 应报错：%#v", incomplete)
	}
}

// list_files 默认列一层（目录带文件数），recursive 列所有层，超出上限按 file_offset 续列。
func TestRepositoryListFiles(t *testing.T) {
	github := newRepositoryCodeReadTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "看看仓库结构", nil)

	root := runRepositoryToolWithBudget(t, tool, 8_000, map[string]any{"operation": "list_files", "repository": "acme/demo"})
	if !root.OK || root.Tree == nil || root.Tree.Ref != "main" || root.Tree.Total != 3 {
		t.Fatalf("root=%#v", root)
	}
	if root.Tree.Entries != "model/  (402 个文件)\nREADME.md  2.0KB\ngo.mod  120B\n" {
		t.Fatalf("根目录：目录排前面并带文件数，文件带大小：%q", root.Tree.Entries)
	}

	agentDir := runRepositoryToolWithBudget(t, tool, 8_000, map[string]any{"operation": "list_files", "repository": "acme/demo", "path": "model/agent/"})
	if !agentDir.OK || agentDir.Tree.Entries != "runner.go  48.8KB\ntypes.go  2.9KB\n" {
		t.Fatalf("agentDir=%#v", agentDir.Tree)
	}

	seen := 0
	offset, calls := 0, 0
	for {
		calls++
		if calls > 20 {
			t.Fatal("目录续列没有收敛")
		}
		result := runRepositoryToolWithBudget(t, tool, 4_000, map[string]any{"operation": "list_files", "repository": "acme/demo", "path": "model", "recursive": true, "file_offset": offset})
		if !result.OK || result.Tree.Start != offset+1 {
			t.Fatalf("result=%#v", result)
		}
		seen += strings.Count(result.Tree.Entries, "\n")
		if result.Tree.End == result.Tree.Total {
			break
		}
		if !strings.Contains(result.Message, fmt.Sprintf("file_offset=%d", result.Tree.End)) {
			t.Fatalf("续列提示：%q", result.Message)
		}
		offset = result.Tree.End
	}
	if seen != 402 || calls < 2 {
		t.Fatalf("recursive 续列应列全 402 个文件，got %d（%d 次）", seen, calls)
	}

	file := runRepositoryToolWithBudget(t, tool, 4_000, map[string]any{"operation": "list_files", "repository": "acme/demo", "path": "go.mod"})
	if file.OK || !strings.Contains(file.Message, "read_file") {
		t.Fatalf("文件路径应提示用 read_file：%#v", file)
	}
	missing := runRepositoryToolWithBudget(t, tool, 4_000, map[string]any{"operation": "list_files", "repository": "acme/demo", "path": "nope"})
	if missing.OK || missing.FailureCode != "not_found" {
		t.Fatalf("missing=%#v", missing)
	}
}
