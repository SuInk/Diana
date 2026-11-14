// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
)

// 读长代码、长 diff 和目录树。
//
// github 工具的结果回到模型之前会被 Runner 截到单次上限。以前这里各算各的：read_file
// 最多给 6 万字、pull_files 的 patch 合计 6 万字，Runner 却按 8000 字截，JSON 在中间
// 断掉，「后面还有，传 start_line=X」里的 X 是按没截之前算的，模型照着续读就跳过了
// 一大段；每个文件的 patch_truncated 标记也跟着被截没了。
//
// 现在工具先问 Runner 这次给多少（agent.ToolOutputBudget），按整行、按 JSON 编码后
// 的实际字数装到上限为止，续读位置按真正给出的内容算：文件按 start_line，patch 按
// patch_line，文件列表和目录树按 file_offset。
const (
	// repositoryToolOutputChars 是 github 工具向 Runner 要的单次上限。读代码和 diff
	// 一次给太少，模型得来回续读好几轮，步数预算撑不住一次 review。
	repositoryToolOutputChars = agent.ToolOutputMaxChars
	// repositoryOutputReserve 给 message 和续读提示留的字数：内容装完之后才写 message。
	repositoryOutputReserve        = 1_500
	repositoryCompareFileLimitHint = 300
	// repositoryCompareFileRoom 是提交清单列完后至少还要剩多少字才接着在同一页给文件。
	repositoryCompareFileRoom = 4_000
)

// repositoryOutputBudget 是这次结果能用的字数。不在 Runner 里调用（测试、直接调用）
// 时按工具自己声明的上限算。
func repositoryOutputBudget(ctx context.Context) int {
	if budget := agent.ToolOutputBudget(ctx); budget > 0 {
		return budget
	}
	return repositoryToolOutputChars
}

// marshalRepositoryResult 把结果编码成给模型看的 JSON。不转义 <、>、&：结果不进
// HTML，代码里满是这几个字符，转义成 \u003c 一个字符变六个，白白吃掉预算。
func marshalRepositoryResult(result repositoryIssueResult) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

func repositoryResultRunes(result repositoryIssueResult) int {
	body, err := marshalRepositoryResult(result)
	if err != nil {
		return 0
	}
	return utf8.RuneCount(body)
}

// repositoryJSONRunes 算一段文本放进 JSON 字符串后占多少字：换行、引号、反斜杠要
// 转义成两个字符，其余控制字符是六个。Runner 按 rune 截，所以这里也按 rune 算。
func repositoryJSONRunes(text string) int {
	count := 0
	for index, width := 0, 0; index < len(text); index += width {
		var r rune
		r, width = utf8.DecodeRuneInString(text[index:])
		switch {
		case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t':
			count += 2
		case r < 0x20 || r == '\u2028' || r == '\u2029' || (r == utf8.RuneError && width == 1):
			count += 6
		default:
			count++
		}
	}
	return count
}

// repositoryJSONPrefix 取 text 开头、JSON 编码后不超过 budget 字的那一段。
func repositoryJSONPrefix(text string, budget int) string {
	used := 0
	for index, r := range text {
		cost := repositoryJSONRunes(string(r))
		if used+cost > budget {
			return text[:index]
		}
		used += cost
	}
	return text
}

// repositoryLineWindow 是按整行装进预算的一段文本。
type repositoryLineWindow struct {
	Text string
	// Count 是用掉的行数，续读从下一行开始。
	Count int
	// Cut 表示最后一行太长、只给了开头：压缩过的 JS、单行 JSON 常见。这一行仍算用掉，
	// 否则续读会卡在同一行上原地打转。
	Cut bool
}

// repositoryFillLines 从第 0 行起按整行往 budget 里装，最多装 maxLines 行。
func repositoryFillLines(count int, line func(int) string, maxLines, budget int) repositoryLineWindow {
	var builder strings.Builder
	used := 0
	window := repositoryLineWindow{}
	for index := 0; index < count && index < maxLines; index++ {
		text := line(index) + "\n"
		cost := repositoryJSONRunes(text)
		if used+cost > budget {
			if index == 0 && budget > 0 {
				builder.WriteString(repositoryJSONPrefix(text, budget))
				window.Count, window.Cut = 1, true
			}
			break
		}
		builder.WriteString(text)
		used += cost
		window.Count = index + 1
	}
	window.Text = builder.String()
	return window
}

// repositoryDiffOptions 是 pull_files、commit_files、compare_files 共用的筛选和续读参数。
type repositoryDiffOptions struct {
	focus []string
	// offset 跳过前几个匹配的文件，文件太多、一次列不完时用。
	offset int
	// patchLine 从 patch 的第几行开始给，只在 paths 恰好匹配一个文件时有效。
	patchLine int
}

func parseRepositoryDiffOptions(input map[string]any) (repositoryDiffOptions, string, string) {
	focus, _, code, message := repositoryIssueStringList(input, "paths", 50)
	if code != "" {
		return repositoryDiffOptions{}, code, message
	}
	options := repositoryDiffOptions{focus: focus}
	if value, ok := numberValue(input["file_offset"]); ok && value > 0 {
		options.offset = int(value)
	}
	if value, ok := numberValue(input["patch_line"]); ok && value > 1 {
		options.patchLine = int(value)
	}
	return options, "", ""
}

// renderRepositoryDiffFiles 把改动文件按预算装进 result.Files，返回写进 message 的说明。
// 失败时返回错误码和说明，result 不动。
//
// 照 Pi 的做法按整个文件分页：从 file_offset 起逐个文件连同完整 patch 装进去，下一个
// 装不下就停在它前面，message 写明传 file_offset 从哪继续。以前文件清单占七成、每个
// 文件的 patch 只给开头 4000 字，模型看到的是一堆半截 diff，常把没给的部分当成没改。
// 只有本页第一个文件单独都装不下时才按整行切它，带上 patch_start_line 等字段，
// 续读传同一个 file_offset 加 patch_line。
func renderRepositoryDiffFiles(result *repositoryIssueResult, files []githubPullRequestFile, options repositoryDiffOptions, budget int) (string, string, string) {
	matched := make([]githubPullRequestFile, 0, len(files))
	for _, file := range files {
		if len(options.focus) == 0 || repositoryPullRequestPathMatches(file, options.focus) {
			matched = append(matched, file)
		}
	}
	if options.offset > 0 && options.offset >= len(matched) {
		return "", "invalid_input", fmt.Sprintf("file_offset 超出范围：一共只有 %d 个文件。", len(matched))
	}
	if len(matched) == 0 {
		result.Files = []repositoryPullRequestFileView{}
		if len(options.focus) > 0 {
			return fmt.Sprintf("paths 没有匹配到任何文件（共 %d 个）。", len(files)), "", ""
		}
		return "没有改动的文件。", "", ""
	}
	candidates := matched[options.offset:]
	room := budget - repositoryResultRunes(*result) - repositoryOutputReserve
	views := make([]repositoryPullRequestFileView, 0, len(candidates))
	used := 0
	var partial *repositoryPullRequestFileView
	partialCut := false
	for index, file := range candidates {
		view := repositoryPullRequestFileView{
			Path: file.Filename, PreviousPath: file.PreviousFilename, Status: file.Status,
			Additions: file.Additions, Deletions: file.Deletions, PatchUnavailable: file.Patch == "",
		}
		var lines []string
		from := 1
		if file.Patch != "" {
			lines = strings.Split(strings.TrimSuffix(file.Patch, "\n"), "\n")
			if index == 0 && options.patchLine > 0 {
				from = options.patchLine
			}
			if from > len(lines) {
				return "", "invalid_input", fmt.Sprintf("patch_line 超出范围：%s 的 patch 共 %d 行。", view.Path, len(lines))
			}
			view.Patch = strings.Join(lines[from-1:], "\n")
			if from > 1 {
				view.PatchStartLine, view.PatchEndLine, view.PatchTotalLines = from, len(lines), len(lines)
				view.PatchHunk = repositoryPatchHunkBefore(lines, from)
			}
		}
		cost := repositoryJSONValueRunes(view) + 1
		if used+cost <= room {
			views = append(views, view)
			used += cost
			continue
		}
		if index > 0 {
			break
		}
		// 本页第一个文件单独都装不下：按整行切，下一页从同一个文件的下一行接着读。
		view.Patch = ""
		window := repositoryFillLines(len(lines)-from+1, func(i int) string { return lines[from-1+i] }, len(lines), room-(repositoryJSONValueRunes(view)+1)-80)
		view.Patch = strings.TrimSuffix(window.Text, "\n")
		view.PatchStartLine, view.PatchEndLine, view.PatchTotalLines = from, from-1+window.Count, len(lines)
		view.PatchTruncated = view.PatchEndLine < len(lines) || window.Cut
		view.PatchHunk = repositoryPatchHunkBefore(lines, from)
		views = append(views, view)
		partial, partialCut = &views[0], window.Cut
		break
	}
	result.Files = views
	shownEnd := options.offset + len(views)

	scope := fmt.Sprintf("全部 %d 个文件", len(matched))
	if len(options.focus) > 0 {
		scope = fmt.Sprintf("paths 匹配的 %d 个文件（共 %d 个）", len(matched), len(files))
	}
	note := fmt.Sprintf("改动共%s，这次给了第 %d-%d 个。", scope, options.offset+1, shownEnd)
	switch {
	case partial != nil && partial.PatchTruncated:
		note += fmt.Sprintf(" %s 的 patch 共 %d 行，单次上限只装下第 %d-%d 行，传 file_offset=%d、patch_line=%d（paths 不变）接着读这个文件。", partial.Path, partial.PatchTotalLines, partial.PatchStartLine, partial.PatchEndLine, options.offset, partial.PatchEndLine+1)
		if partialCut {
			note += fmt.Sprintf(" 第 %d 行太长，只给了开头一段，这一行算读过。", partial.PatchEndLine)
		}
	case shownEnd < len(matched):
		result.FilesTruncated = true
		result.NextFileOffset = shownEnd
		note += fmt.Sprintf(" 给出的 patch 都是完整的；后面还有 %d 个文件没给，传 file_offset=%d（paths 不变，不带 patch_line）继续读，没读完之前不要断言其余文件没改什么。", len(matched)-shownEnd, shownEnd)
	default:
		note += " patch 都是完整的。"
	}
	return note, "", ""
}

// repositoryJSONValueRunes 是一个值按结果的编码方式（不转义 <>&）编码后占的字数。
func repositoryJSONValueRunes(value any) int {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 0
	}
	return utf8.RuneCount(bytes.TrimRight(buffer.Bytes(), "\n"))
}

// repositoryPatchHunkBefore 从 hunk 中间接着读时，找出这段所在的 hunk 头，行号靠它算。
func repositoryPatchHunkBefore(lines []string, from int) string {
	if from <= 1 || strings.HasPrefix(lines[from-1], "@@") {
		return ""
	}
	for back := from - 2; back >= 0; back-- {
		if strings.HasPrefix(lines[back], "@@") {
			return lines[back]
		}
	}
	return ""
}

// repositoryRefPath 把分支、标签或提交拼进接口路径。分支名可以带斜杠，逐段转义。
func repositoryRefPath(ref string) string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(ref), "/"), "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

type githubCommitDetail struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
			Date string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
	Stats struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
	} `json:"stats"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
	Files []githubPullRequestFile `json:"files"`
}

type repositoryCommitView struct {
	SHA       string   `json:"sha"`
	URL       string   `json:"url,omitempty"`
	Author    string   `json:"author,omitempty"`
	Date      string   `json:"date,omitempty"`
	Message   string   `json:"message"`
	Additions int      `json:"additions"`
	Deletions int      `json:"deletions"`
	Parents   []string `json:"parents,omitempty"`
}

func repositoryCommitViewFromGitHub(commit githubCommitDetail) *repositoryCommitView {
	view := &repositoryCommitView{
		SHA: commit.SHA, URL: commit.HTMLURL, Date: commit.Commit.Author.Date,
		Additions: commit.Stats.Additions, Deletions: commit.Stats.Deletions,
	}
	view.Author = strings.TrimSpace(commit.Commit.Author.Name)
	if commit.Author != nil && strings.TrimSpace(commit.Author.Login) != "" {
		view.Author = strings.TrimSpace(commit.Author.Login)
	}
	view.Message = repositoryIssueDisplayText(commit.Commit.Message)
	for _, parent := range commit.Parents {
		view.Parents = append(view.Parents, repositoryShortSHA(parent.SHA))
	}
	return view
}

func repositoryShortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// commitFiles 读单个提交改了什么。以前只有 PR 能读 diff，用户贴一个 commit 链接，
// 模型只能去渲染网页，看到的是折叠过的 diff 页面。
func (t *dianaGitHubTool) commitFiles(ctx context.Context, repository string, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "commit_files", Repository: repository}
	ref := strings.TrimSpace(configToolString(input, "ref"))
	if ref == "" {
		return result.fail("invalid_input", "commit_files 必须在 ref 里给出提交 SHA（也可以是分支或标签，读它最新的那个提交）。")
	}
	options, code, message := parseRepositoryDiffOptions(input)
	if code != "" {
		return result.fail(code, message)
	}
	var detail githubCommitDetail
	var files []githubPullRequestFile
	for page := 1; page <= repositoryPullRequestFilesMaxPages; page++ {
		values := url.Values{"per_page": {strconv.Itoa(repositoryPullRequestFilesPerPage)}, "page": {strconv.Itoa(page)}}
		var batch githubCommitDetail
		headers, apiErr := t.doJSONWithHeaders(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s?%s", repository, repositoryRefPath(ref), values.Encode()), nil, &batch)
		if apiErr != nil {
			if apiErr.Code == "not_found" || apiErr.Code == "validation_failed" {
				return result.fail("not_found", "这个仓库里找不到该提交。ref 填完整或至少 7 位的提交 SHA，也可以填分支或标签名。")
			}
			return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
		}
		if page == 1 {
			detail = batch
		}
		files = append(files, batch.Files...)
		lastPage, known := repositoryIssueLastPage(headers.Get("Link"))
		if !known || page >= lastPage || len(batch.Files) == 0 {
			break
		}
	}
	result.OK, result.Outcome = true, "fetched"
	result.Commit = repositoryCommitViewFromGitHub(detail)
	note, code, message := renderRepositoryDiffFiles(&result, files, options, repositoryOutputBudget(ctx))
	if code != "" {
		return result.fail(code, message)
	}
	result.Message = fmt.Sprintf("提交 %s：%s", repositoryShortSHA(detail.SHA), note)
	return result
}

type githubCompare struct {
	HTMLURL      string                  `json:"html_url"`
	Status       string                  `json:"status"`
	AheadBy      int                     `json:"ahead_by"`
	BehindBy     int                     `json:"behind_by"`
	TotalCommits int                     `json:"total_commits"`
	Commits      []githubCommitDetail    `json:"commits"`
	Files        []githubPullRequestFile `json:"files"`
}

type repositoryCompareView struct {
	Base         string `json:"base"`
	Head         string `json:"head"`
	URL          string `json:"url,omitempty"`
	Status       string `json:"status,omitempty"`
	AheadBy      int    `json:"ahead_by"`
	BehindBy     int    `json:"behind_by"`
	TotalCommits int    `json:"total_commits"`
	// Commits 每条是「短 SHA 标题」，按时间从旧到新，从 commit_offset 起按整条装进单次
	// 上限；装不下的留给下一页，见 compareFiles。
	Commits []string `json:"commits,omitempty"`
	// CommitStart、CommitEnd 是这次列出的提交范围（从 1 起）。
	CommitStart int `json:"commit_start,omitempty"`
	CommitEnd   int `json:"commit_end,omitempty"`
}

// compareFiles 读两个分支、标签或提交之间的改动，对应 GitHub 的 /compare/base...head。
func (t *dianaGitHubTool) compareFiles(ctx context.Context, repository string, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "compare_files", Repository: repository}
	base := strings.TrimSpace(configToolString(input, "base"))
	head := strings.TrimSpace(configToolString(input, "head"))
	if base == "" && head == "" {
		// 模型照着链接把 a...b 整段填进 ref 也认。
		if left, right, ok := strings.Cut(strings.TrimSpace(configToolString(input, "ref")), "..."); ok {
			base, head = strings.TrimSpace(left), strings.TrimSpace(right)
		}
	}
	if base == "" || head == "" {
		return result.fail("invalid_input", "compare_files 必须同时给出 base 和 head（分支、标签或提交 SHA）。")
	}
	options, code, message := parseRepositoryDiffOptions(input)
	if code != "" {
		return result.fail(code, message)
	}
	var compare githubCompare
	endpoint := fmt.Sprintf("/repos/%s/compare/%s...%s", repository, repositoryRefPath(base), repositoryRefPath(head))
	if apiErr := t.doJSON(ctx, http.MethodGet, endpoint, nil, &compare); apiErr != nil {
		if apiErr.Code == "not_found" {
			return result.fail("not_found", "找不到这两个版本：base、head 要填这个仓库里存在的分支、标签或提交 SHA。")
		}
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	view := &repositoryCompareView{
		Base: base, Head: head, URL: compare.HTMLURL, Status: compare.Status,
		AheadBy: compare.AheadBy, BehindBy: compare.BehindBy, TotalCommits: compare.TotalCommits,
	}
	result.OK, result.Outcome = true, "fetched"
	result.Comparison = view
	// 提交清单只在文件的第一页给：按整条从 commit_offset 装起，装不下就这页只给提交，
	// 文件留到列完提交之后。以前只留最新 20 条，更早的提交模型根本看不到。
	commitOffset := 0
	if value, ok := numberValue(input["commit_offset"]); ok && value > 0 {
		commitOffset = int(value)
	}
	commitsPending, filesDeferred := false, false
	// commit_offset 不小于提交总数表示跳过提交清单，直接读文件。
	if options.offset == 0 && options.patchLine == 0 && commitOffset < len(compare.Commits) {
		rest := compare.Commits[commitOffset:]
		room := repositoryOutputBudget(ctx) - repositoryResultRunes(result) - repositoryOutputReserve
		used := 0
		for _, commit := range rest {
			title, _, _ := strings.Cut(strings.TrimSpace(commit.Commit.Message), "\n")
			line := repositoryShortSHA(commit.SHA) + " " + repositoryIssueDisplayText(title)
			cost := repositoryJSONRunes(line) + 3
			if len(view.Commits) > 0 && used+cost > room {
				break
			}
			view.Commits = append(view.Commits, line)
			used += cost
		}
		view.CommitStart, view.CommitEnd = commitOffset+1, commitOffset+len(view.Commits)
		commitsPending = view.CommitEnd < len(compare.Commits)
		// 提交列完剩的地方太少就不硬塞半个文件进来，下一页专门读文件。
		filesDeferred = !commitsPending && room-used < repositoryCompareFileRoom
	}
	commitNote := ""
	if view.CommitEnd > 0 {
		commitNote = fmt.Sprintf("提交共 %d 条，这次列了第 %d-%d 条。", len(compare.Commits), view.CommitStart, view.CommitEnd)
	}
	if len(compare.Commits) < compare.TotalCommits {
		commitNote += fmt.Sprintf(" GitHub 对比接口这次只返回了 %d 条提交（total_commits=%d），其余提交不在这里；要看就换更近的 base 再比。", len(compare.Commits), compare.TotalCommits)
	}
	switch {
	case commitsPending:
		result.Message = fmt.Sprintf("%s...%s：%s 后面还有 %d 条提交，传 commit_offset=%d 继续列。这页没给改动文件，列完提交后传 commit_offset=%d 跳过提交清单读文件。", base, head, commitNote, len(compare.Commits)-view.CommitEnd, view.CommitEnd, len(compare.Commits))
		return result
	case filesDeferred:
		result.Message = fmt.Sprintf("%s...%s：%s 改动共 %d 个文件，这页装不下了，传 commit_offset=%d 跳过提交清单读文件。", base, head, commitNote, len(compare.Files), len(compare.Commits))
		return result
	}
	note, code, message := renderRepositoryDiffFiles(&result, compare.Files, options, repositoryOutputBudget(ctx))
	if code != "" {
		return result.fail(code, message)
	}
	result.Message = fmt.Sprintf("%s...%s：%s%s", base, head, commitNote, note)
	if len(compare.Files) >= repositoryCompareFileLimitHint {
		result.Message += fmt.Sprintf(" GitHub 的对比接口最多只返回 %d 个文件，实际改动可能更多；缩小范围就换更近的 base，或者逐个提交用 commit_files 读。", repositoryCompareFileLimitHint)
	}
	return result
}

type githubGitTree struct {
	SHA       string `json:"sha"`
	Truncated bool   `json:"truncated"`
	Tree      []struct {
		Path string `json:"path"`
		Type string `json:"type"`
		Size int64  `json:"size"`
	} `json:"tree"`
}

type repositoryTreeView struct {
	Ref  string `json:"ref"`
	Path string `json:"path,omitempty"`
	// Total 是这一层（recursive 时是所有层）一共多少项，Start、End 是这次列出的范围（从 1 起）。
	Total     int  `json:"total"`
	Start     int  `json:"start"`
	End       int  `json:"end"`
	Recursive bool `json:"recursive,omitempty"`
	// Entries 每行一项：目录以 / 结尾并带文件数，文件带大小。比逐项 JSON 对象省一半字数。
	Entries string `json:"entries"`
	// Incomplete 表示仓库太大，GitHub 只返回了部分目录树。
	Incomplete bool `json:"incomplete,omitempty"`
}

type repositoryTreeEntry struct {
	path  string
	dir   bool
	size  int64
	files int
}

// listFiles 列仓库目录。以前只能靠 read_file 猜路径，猜错了就是一句「没有该文件」，
// 模型只好去渲染 GitHub 网页，而网页上的目录树是折叠的。
func (t *dianaGitHubTool) listFiles(ctx context.Context, repository string, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "list_files", Repository: repository}
	dir := strings.Trim(strings.TrimSpace(configToolString(input, "path")), "/")
	if strings.Contains(dir, "..") {
		return result.fail("invalid_input", "path 必须是仓库内的目录。")
	}
	recursive, _ := input["recursive"].(bool)
	offset := 0
	if value, ok := numberValue(input["file_offset"]); ok && value > 0 {
		offset = int(value)
	}
	ref := strings.TrimSpace(configToolString(input, "ref"))
	if ref == "" {
		var meta struct {
			DefaultBranch string `json:"default_branch"`
		}
		if apiErr := t.doJSON(ctx, http.MethodGet, "/repos/"+repository, nil, &meta); apiErr != nil {
			return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
		}
		ref = strings.TrimSpace(meta.DefaultBranch)
		if ref == "" {
			return result.fail("not_found", "这个仓库还没有任何提交。")
		}
	}
	var tree githubGitTree
	if apiErr := t.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/git/trees/%s?recursive=1", repository, repositoryRefPath(ref)), nil, &tree); apiErr != nil {
		if apiErr.Code == "not_found" {
			return result.fail("not_found", "找不到这个版本：ref 要填这个仓库里存在的分支、标签或提交 SHA，不填就是默认分支。")
		}
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	entries := map[string]*repositoryTreeEntry{}
	isFile := false
	for _, item := range tree.Tree {
		if dir != "" && item.Path == dir && item.Type == "blob" {
			isFile = true
		}
		if !strings.HasPrefix(item.Path, prefix) || item.Path == dir {
			continue
		}
		rel := strings.TrimPrefix(item.Path, prefix)
		if recursive {
			if item.Type != "tree" {
				entries[rel] = &repositoryTreeEntry{path: rel, size: item.Size, dir: item.Type == "commit"}
			}
			continue
		}
		top, rest, nested := strings.Cut(rel, "/")
		entry := entries[top]
		if entry == nil {
			entry = &repositoryTreeEntry{path: top, dir: nested || item.Type == "tree" || item.Type == "commit"}
			entries[top] = entry
		}
		if !nested && item.Type == "blob" {
			entry.size = item.Size
		}
		if nested && rest != "" && item.Type == "blob" {
			entry.files++
		}
	}
	if len(entries) == 0 {
		if isFile {
			return result.fail("invalid_input", "path 指向的是文件，不是目录；用 read_file 读它。")
		}
		if dir != "" {
			return result.fail("not_found", "这个版本里没有该目录。先不传 path 列出根目录，再按列出的路径往下找。")
		}
		return result.fail("not_found", "这个版本的目录树是空的。")
	}
	sorted := make([]*repositoryTreeEntry, 0, len(entries))
	for _, entry := range entries {
		sorted = append(sorted, entry)
	}
	sort.Slice(sorted, func(i, j int) bool {
		// 不递归时目录排前面，和 GitHub 网页一致；递归时按路径排，同一目录的文件挨在一起。
		if !recursive && sorted[i].dir != sorted[j].dir {
			return sorted[i].dir
		}
		return sorted[i].path < sorted[j].path
	})
	if offset >= len(sorted) {
		return result.fail("invalid_input", fmt.Sprintf("file_offset 超出范围：一共只有 %d 项。", len(sorted)))
	}
	view := &repositoryTreeView{Ref: ref, Path: dir, Total: len(sorted), Start: offset + 1, Recursive: recursive, Incomplete: tree.Truncated}
	result.OK, result.Outcome, result.Tree = true, "fetched", view
	room := repositoryOutputBudget(ctx) - repositoryResultRunes(result) - repositoryOutputReserve
	rest := sorted[offset:]
	window := repositoryFillLines(len(rest), func(i int) string { return rest[i].line() }, len(rest), room)
	view.Entries = window.Text
	view.End = offset + window.Count
	location := "根目录"
	if dir != "" {
		location = dir
	}
	result.Message = fmt.Sprintf("%s@%s 的%s共 %d 项，这次列了第 %d-%d 项。", repository, ref, location, view.Total, view.Start, view.End)
	if recursive {
		result.Message = fmt.Sprintf("%s@%s 的%s下（含子目录）共 %d 个文件，这次列了第 %d-%d 个。", repository, ref, location, view.Total, view.Start, view.End)
	}
	if view.End < view.Total {
		result.Message += fmt.Sprintf(" 后面还有，传 file_offset=%d 继续列；也可以把 path 换成某个子目录只看那一块。", view.End)
	}
	if tree.Truncated {
		result.Message += " 仓库太大，GitHub 只返回了一部分目录树，这里可能不全；按子目录传 path 缩小范围。"
	}
	result.Message += " 读文件用 read_file，path 填列出的路径（前面拼上当前目录）。"
	return result
}

func (e *repositoryTreeEntry) line() string {
	switch {
	case e.dir && e.files > 0:
		return fmt.Sprintf("%s/  (%d 个文件)", e.path, e.files)
	case e.dir:
		return e.path + "/"
	default:
		return fmt.Sprintf("%s  %s", e.path, repositoryHumanSize(e.size))
	}
}

func repositoryHumanSize(size int64) string {
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(size)/(1<<10))
	default:
		return fmt.Sprintf("%dB", size)
	}
}
