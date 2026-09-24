// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Pull Request 支持。
//
// 这个工具以前只认 Issue：get 读不了 PR，评论 PR 直接报「目标编号属于 Pull Request」。
// 线上有人让机器人 review 两个 PR 并把建议评论上去，它只能退回去用网页渲染读 PR 描述页，
// 看不到 diff，建议写得像读过代码，评论也发不出去。这里补上三件事：get 读 PR 的分支、
// 合并状态、改动统计和已有 review；pull_files 读改动文件和 patch；comment 与 review 能
// 写到 PR 上。写入仍然走草稿加确认码。改 PR 本身（合并、关闭、改标题）不开放。
const (
	repositoryPullRequestReviewLimit     = 10
	repositoryPullRequestReviewBodyLimit = 2_000
	repositoryPullRequestFilesPerPage    = 100
	repositoryPullRequestFilesMaxPages   = 30
	repositoryPullRequestFileListLimit   = 300
	// repositoryPullRequestPatchLimit 是不传 paths 时每个文件给的 patch 字数；传了
	// paths 就把单次结果剩下的预算都给这几个文件，见 renderRepositoryDiffFiles。
	repositoryPullRequestPatchLimit         = 4_000
	repositoryPullRequestReviewCommentLimit = 30
	repositoryPullRequestReviewMaxPages     = 30
	repositoryFileDefaultLines              = 400
	repositoryFileMaxLines                  = 1_500
)

type githubPullRequest struct {
	Number         int    `json:"number"`
	HTMLURL        string `json:"html_url"`
	State          string `json:"state"`
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	Additions      int    `json:"additions"`
	Deletions      int    `json:"deletions"`
	ChangedFiles   int    `json:"changed_files"`
	Commits        int    `json:"commits"`
	User           *struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// repositoryPullRequestView 是 get 读到 PR 时额外给模型看的那部分。
type repositoryPullRequestView struct {
	Author         string `json:"author,omitempty"`
	HeadRef        string `json:"head_ref,omitempty"`
	HeadSHA        string `json:"head_sha,omitempty"`
	BaseRef        string `json:"base_ref,omitempty"`
	Draft          bool   `json:"draft,omitempty"`
	Merged         bool   `json:"merged,omitempty"`
	Mergeable      *bool  `json:"mergeable,omitempty"`
	MergeableState string `json:"mergeable_state,omitempty"`
	Additions      int    `json:"additions"`
	Deletions      int    `json:"deletions"`
	ChangedFiles   int    `json:"changed_files"`
	Commits        int    `json:"commits"`
}

type githubPullRequestFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Patch            string `json:"patch"`
}

type repositoryPullRequestFileView struct {
	Path         string `json:"path"`
	PreviousPath string `json:"previous_path,omitempty"`
	Status       string `json:"status"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	Patch        string `json:"patch,omitempty"`
	// PatchTruncated 表示 patch 只给了开头一段；PatchOmitted 表示总量用完没给。
	// PatchUnavailable 是 GitHub 自己没给 patch（二进制文件或 diff 过大）。
	PatchTruncated   bool `json:"patch_truncated,omitempty"`
	PatchOmitted     bool `json:"patch_omitted,omitempty"`
	PatchUnavailable bool `json:"patch_unavailable,omitempty"`
	// 只给了 patch 的一段时，说明给的是第几行到第几行（从 1 起）、一共多少行；
	// 从 hunk 中间接着读时 PatchHunk 是这段所在的 hunk 头，行号靠它算。
	PatchStartLine  int    `json:"patch_start_line,omitempty"`
	PatchEndLine    int    `json:"patch_end_line,omitempty"`
	PatchTotalLines int    `json:"patch_total_lines,omitempty"`
	PatchHunk       string `json:"patch_hunk,omitempty"`
}

type githubPullRequestReview struct {
	ID          int64     `json:"id"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	HTMLURL     string    `json:"html_url"`
	SubmittedAt time.Time `json:"submitted_at"`
	User        *struct {
		Login string `json:"login"`
	} `json:"user"`
}

type repositoryPullRequestReviewView struct {
	Author      string    `json:"author,omitempty"`
	State       string    `json:"state"`
	Body        string    `json:"body,omitempty"`
	Truncated   bool      `json:"truncated,omitempty"`
	URL         string    `json:"url,omitempty"`
	SubmittedAt time.Time `json:"submitted_at,omitempty"`
}

// repositoryPullRequestReviewComment 是 review 里的一条行内评论。
type repositoryPullRequestReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side,omitempty"`
	Body string `json:"body"`
}

// getIssueOrPullRequest 读 Issue 接口上的对象，PR 也照样返回。GitHub 的 PR 同时是一个
// Issue：标题、正文、普通评论都在 Issue 接口上。
func (t *dianaGitHubTool) getIssueOrPullRequest(ctx context.Context, repository string, number int) (githubRepositoryIssue, *repositoryIssueAPIError) {
	var issue githubRepositoryIssue
	apiErr := t.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", repository, number), nil, &issue)
	return issue, apiErr
}

func repositoryIssueResource(issue githubRepositoryIssue) string {
	if issue.PullRequest != nil {
		return "pull"
	}
	return "issues"
}

func (t *dianaGitHubTool) getPullRequest(ctx context.Context, repository string, number int) (githubPullRequest, *repositoryIssueAPIError) {
	var pull githubPullRequest
	if apiErr := t.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repository, number), nil, &pull); apiErr != nil {
		return pull, apiErr
	}
	if pull.Number != number || !validRepositoryIssueCanonicalURL(pull.HTMLURL, repository, "pull", number) {
		return pull, &repositoryIssueAPIError{Code: "invalid_response"}
	}
	return pull, nil
}

func repositoryPullRequestViewFromGitHub(pull githubPullRequest) *repositoryPullRequestView {
	view := &repositoryPullRequestView{
		HeadRef: strings.TrimSpace(pull.Head.Ref), HeadSHA: strings.TrimSpace(pull.Head.SHA), BaseRef: strings.TrimSpace(pull.Base.Ref),
		Draft: pull.Draft, Merged: pull.Merged, Mergeable: pull.Mergeable, MergeableState: strings.TrimSpace(pull.MergeableState),
		Additions: pull.Additions, Deletions: pull.Deletions, ChangedFiles: pull.ChangedFiles, Commits: pull.Commits,
	}
	if pull.User != nil {
		view.Author = strings.TrimSpace(pull.User.Login)
	}
	return view
}

// attachPullRequestDetails 给 get 的结果补上 PR 的分支、合并状态和最近几条 review。
func (t *dianaGitHubTool) attachPullRequestDetails(ctx context.Context, repository string, number int, result *repositoryIssueResult) *repositoryIssueAPIError {
	pull, apiErr := t.getPullRequest(ctx, repository, number)
	if apiErr != nil {
		return apiErr
	}
	result.PullRequest = repositoryPullRequestViewFromGitHub(pull)
	values := url.Values{"per_page": {"100"}}
	var reviews []githubPullRequestReview
	if apiErr := t.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/reviews?%s", repository, number, values.Encode()), nil, &reviews); apiErr != nil {
		return apiErr
	}
	// 接口按提交时间从旧到新返回，保留最近的几条。
	if len(reviews) > repositoryPullRequestReviewLimit {
		reviews = reviews[len(reviews)-repositoryPullRequestReviewLimit:]
		result.ReviewsTruncated = true
	}
	result.Reviews = make([]repositoryPullRequestReviewView, 0, len(reviews))
	for _, review := range reviews {
		view := repositoryPullRequestReviewView{State: strings.TrimSpace(review.State), URL: review.HTMLURL, SubmittedAt: review.SubmittedAt}
		if review.User != nil {
			view.Author = review.User.Login
		}
		view.Body, view.Truncated = repositoryIssueDisplayText(review.Body, repositoryPullRequestReviewBodyLimit)
		result.Reviews = append(result.Reviews, view)
	}
	return nil
}

// pullFiles 读 PR 改动的文件和 patch。review 之前应该先读这里，而不是只看 PR 描述。
func (t *dianaGitHubTool) pullFiles(ctx context.Context, repository string, input map[string]any) repositoryIssueResult {
	number := repositoryIssueNumber(input)
	result := repositoryIssueResult{Operation: "pull_files", Repository: repository, RequestedNumber: number}
	if number <= 0 {
		return result.fail("invalid_input", "pull_files 必须提供有效的 Pull Request number。")
	}
	options, code, message := parseRepositoryDiffOptions(input)
	if code != "" {
		return result.fail(code, message)
	}
	pull, apiErr := t.getPullRequest(ctx, repository, number)
	if apiErr != nil {
		if apiErr.Code == "not_found" {
			return result.fail("not_a_pull_request", repositoryIssueFailureMessage("not_a_pull_request"))
		}
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	var files []githubPullRequestFile
	for page := 1; page <= repositoryPullRequestFilesMaxPages; page++ {
		values := url.Values{"per_page": {strconv.Itoa(repositoryPullRequestFilesPerPage)}, "page": {strconv.Itoa(page)}}
		var batch []githubPullRequestFile
		headers, apiErr := t.doJSONWithHeaders(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/files?%s", repository, number, values.Encode()), nil, &batch)
		if apiErr != nil {
			return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
		}
		files = append(files, batch...)
		lastPage, known := repositoryIssueLastPage(headers.Get("Link"))
		if !known || page >= lastPage || len(batch) == 0 {
			break
		}
	}
	result.OK = true
	result.Outcome = "fetched"
	result.PullRequest = repositoryPullRequestViewFromGitHub(pull)
	note, code, message := renderRepositoryDiffFiles(&result, files, options, repositoryOutputBudget(ctx))
	if code != "" {
		return result.fail(code, message)
	}
	result.Message = fmt.Sprintf("%s#%d：%s review 的行内评论只能落在 patch 里出现过的行上。", repository, number, note)
	return result
}

// repositoryPullRequestPathMatches 按完整路径或目录前缀匹配。
func repositoryPullRequestPathMatches(file githubPullRequestFile, focus []string) bool {
	for _, want := range focus {
		want = strings.Trim(strings.TrimSpace(want), "/")
		if want == "" {
			continue
		}
		for _, path := range []string{file.Filename, file.PreviousFilename} {
			if path != "" && (path == want || strings.HasPrefix(path, want+"/")) {
				return true
			}
		}
	}
	return false
}

// repositoryPullRequestReviewComments 校验并清洗 review 的行内评论。
func repositoryPullRequestReviewComments(input map[string]any) ([]repositoryPullRequestReviewComment, int, string, string) {
	raw, present := input["comments"]
	if !present || raw == nil {
		return nil, 0, "", ""
	}
	var items []any
	switch typed := raw.(type) {
	case []any:
		items = typed
	case []map[string]any:
		for _, item := range typed {
			items = append(items, item)
		}
	case []repositoryPullRequestReviewComment:
		for _, item := range typed {
			items = append(items, map[string]any{"path": item.Path, "line": item.Line, "side": item.Side, "body": item.Body})
		}
	default:
		return nil, 0, "invalid_input", "comments 必须是行内评论数组。"
	}
	if len(items) > repositoryPullRequestReviewCommentLimit {
		return nil, 0, "invalid_input", "一次 review 最多 " + itoa(repositoryPullRequestReviewCommentLimit) + " 条行内评论。"
	}
	comments := make([]repositoryPullRequestReviewComment, 0, len(items))
	redactions := 0
	for index, item := range items {
		fields, ok := item.(map[string]any)
		if !ok {
			return nil, 0, "invalid_input", fmt.Sprintf("comments 第 %d 项必须是对象。", index+1)
		}
		path := strings.Trim(strings.TrimSpace(configToolString(fields, "path")), "/")
		line, lineOK := numberValue(fields["line"])
		body, bodyRedactions := sanitizeRepositoryIssueText(configToolString(fields, "body"), repositoryIssueCommentLimit, false)
		redactions += bodyRedactions
		side := strings.ToUpper(strings.TrimSpace(configToolString(fields, "side")))
		if side == "" {
			side = "RIGHT"
		}
		switch {
		case path == "":
			return nil, 0, "invalid_input", fmt.Sprintf("comments 第 %d 项缺少 path。", index+1)
		case !lineOK || line <= 0 || line != float64(int(line)):
			return nil, 0, "invalid_input", fmt.Sprintf("comments 第 %d 项的 line 必须是正整数。", index+1)
		case body == "":
			return nil, 0, "invalid_input", fmt.Sprintf("comments 第 %d 项缺少 body。", index+1)
		case side != "RIGHT" && side != "LEFT":
			return nil, 0, "invalid_input", fmt.Sprintf("comments 第 %d 项的 side 只能是 RIGHT（新代码）或 LEFT（旧代码）。", index+1)
		}
		comments = append(comments, repositoryPullRequestReviewComment{Path: path, Line: int(line), Side: side, Body: body})
	}
	return comments, redactions, "", ""
}

func repositoryPullRequestReviewCommentsPayload(comments []repositoryPullRequestReviewComment) []any {
	out := make([]any, 0, len(comments))
	for _, comment := range comments {
		out = append(out, map[string]any{"path": comment.Path, "line": comment.Line, "side": comment.Side, "body": comment.Body})
	}
	return out
}

// review 提交一次只评论、不批准也不要求修改的 PR review。
func (t *dianaGitHubTool) review(ctx context.Context, repository string, input map[string]any) repositoryIssueResult {
	number := repositoryIssueNumber(input)
	result := repositoryIssueResult{Operation: "review", Repository: repository, RequestedNumber: number}
	if number <= 0 {
		return result.fail("invalid_input", "review 必须提供有效的 Pull Request number。")
	}
	body, redactions := sanitizeRepositoryIssueText(configToolString(input, "body"), repositoryIssueCommentLimit, false)
	comments, commentRedactions, code, message := repositoryPullRequestReviewComments(input)
	result.Redactions = redactions + commentRedactions
	if code != "" {
		return result.fail(code, message)
	}
	if body == "" {
		return result.fail("invalid_input", "review 必须提供非空 body（总体意见）。")
	}
	issue, apiErr := t.getIssueOrPullRequest(ctx, repository, number)
	if apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	if issue.PullRequest == nil {
		return result.fail("not_a_pull_request", repositoryIssueFailureMessage("not_a_pull_request"))
	}
	operationID := strings.TrimSpace(configToolString(input, "operation_id"))
	fingerprint, payloadHash, code, message := repositoryIssueFingerprint(repository, "review:"+strconv.Itoa(number), operationID, map[string]any{
		"number": number, "body": body, "comments": repositoryPullRequestReviewCommentsPayload(comments),
	})
	if code != "" {
		return result.fail(code, message)
	}
	result.Fingerprint = fingerprint
	marker := repositoryIssueOperationMarkerWithPayload("review", fingerprint, payloadHash)
	markerPrefix := repositoryIssueOperationMarkerPrefix("review", fingerprint)
	operationKey := fmt.Sprintf("%s:review:%d:%s", strings.ToLower(repository), number, fingerprint)
	unlock := t.plugin.operationLock(operationKey)
	defer unlock()
	summary := ptrRepositoryIssueSummary(repositoryIssueSummaryFromGitHub(issue))
	if existing, match, apiErr := t.findReviewMarker(ctx, repository, number, marker, markerPrefix); apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	} else if match == repositoryIssueMarkerExact {
		t.plugin.clearOperationUncertain(operationKey)
		result.OK, result.Outcome, result.Idempotent = true, "reused", true
		result.Message = "这次 review 已经由 GitHub 确认，未重复提交。"
		result.Issue, result.ReviewURL = summary, existing.HTMLURL
		return result
	} else if match == repositoryIssueMarkerConflict && operationID != "" {
		return result.fail("operation_id_conflict", "operation_id 已用于不同的 review 内容；请使用新的 operation_id。")
	}
	if t.plugin.operationUncertain(operationKey) {
		return result.fail("pending_reconciliation", "此前 review 提交结果仍不确定；为避免重复提交，本操作只允许继续对账，请稍后再试或使用新的 operation_id。")
	}
	payload := map[string]any{"body": appendRepositoryIssueMarker(body, marker), "event": "COMMENT"}
	if len(comments) > 0 {
		payload["comments"] = repositoryPullRequestReviewCommentsPayload(comments)
	}
	var created githubPullRequestReview
	apiErr = t.doJSONStatus(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/reviews", repository, number), payload, &created, http.StatusOK)
	if apiErr == nil {
		if !repositoryPullRequestReviewURLValid(created.HTMLURL, repository, number) {
			t.plugin.markOperationUncertain(operationKey)
			return result.fail("invalid_response", "GitHub 返回的 review 链接不属于目标 PR；为避免误报已停止，重试会先对账。")
		}
		result.OK, result.Outcome = true, "reviewed"
		result.Message = fmt.Sprintf("GitHub 已提交 PR review（%d 条行内评论）。", len(comments))
		result.Issue, result.ReviewURL = summary, created.HTMLURL
		return result
	}
	if apiErr.Uncertain {
		t.plugin.markOperationUncertain(operationKey)
		if existing, match, findErr := t.findReviewMarker(context.WithoutCancel(ctx), repository, number, marker, markerPrefix); findErr == nil && match == repositoryIssueMarkerExact {
			t.plugin.clearOperationUncertain(operationKey)
			result.OK, result.Outcome, result.Idempotent, result.Reconciled = true, "reconciled", true, true
			result.Message = "review 提交确认一度不确定，已通过远端操作标记对账，未重复提交。"
			result.Issue, result.ReviewURL = summary, existing.HTMLURL
			return result
		}
	}
	if apiErr.Code == "validation_failed" && len(comments) > 0 {
		return result.fail("validation_failed", "GitHub 拒绝了这次 review：通常是某条行内评论的 path/line 不在这个 PR 的 diff 里。先用 pull_files 核对 patch 中实际出现的行号（新代码用 RIGHT），或把这条意见挪进总体 body。")
	}
	return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
}

func (t *dianaGitHubTool) findReviewMarker(ctx context.Context, repository string, number int, marker, markerPrefix string) (githubPullRequestReview, repositoryIssueMarkerMatch, *repositoryIssueAPIError) {
	for page := 1; page <= repositoryPullRequestReviewMaxPages; page++ {
		values := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		var reviews []githubPullRequestReview
		headers, apiErr := t.doJSONWithHeaders(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/reviews?%s", repository, number, values.Encode()), nil, &reviews)
		if apiErr != nil {
			return githubPullRequestReview{}, repositoryIssueMarkerMissing, apiErr
		}
		for _, review := range reviews {
			if strings.Contains(review.Body, marker) {
				return review, repositoryIssueMarkerExact, nil
			}
			if markerPrefix != "" && strings.Contains(review.Body, markerPrefix) {
				return review, repositoryIssueMarkerConflict, nil
			}
		}
		lastPage, known := repositoryIssueLastPage(headers.Get("Link"))
		if !known {
			return githubPullRequestReview{}, repositoryIssueMarkerMissing, &repositoryIssueAPIError{Code: "idempotency_scan_incomplete"}
		}
		if page >= lastPage {
			return githubPullRequestReview{}, repositoryIssueMarkerMissing, nil
		}
	}
	return githubPullRequestReview{}, repositoryIssueMarkerMissing, &repositoryIssueAPIError{Code: "idempotency_scan_incomplete"}
}

func repositoryPullRequestReviewURLValid(raw, repository string, number int) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.RawQuery != "" || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return false
	}
	return strings.EqualFold(strings.TrimRight(parsed.Path, "/"), "/"+repository+"/pull/"+strconv.Itoa(number)) &&
		strings.HasPrefix(parsed.Fragment, "pullrequestreview-")
}

// repositoryPullRequestReviewCommentsForView 给草稿展示用，按文件和行号排好。
func repositoryPullRequestReviewCommentsForView(input map[string]any) []repositoryPullRequestReviewComment {
	comments, _, code, _ := repositoryPullRequestReviewComments(input)
	if code != "" {
		return nil
	}
	sort.SliceStable(comments, func(i, j int) bool {
		if comments[i].Path != comments[j].Path {
			return comments[i].Path < comments[j].Path
		}
		return comments[i].Line < comments[j].Line
	})
	return comments
}

type githubRepositoryContent struct {
	Type     string `json:"type"`
	Path     string `json:"path"`
	Size     int    `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// readFile 读仓库里一个文件的完整内容（带行号）。pull_files 只有 diff，review 时要看改动
// 周围的代码就得读原文件；以前模型只能去渲染 GitHub 网页。给了 PR number 时默认读 PR
// head 那一版，行号和行内评论要用的 RIGHT 行号一致。
func (t *dianaGitHubTool) readFile(ctx context.Context, repository string, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "read_file", Repository: repository}
	path := strings.Trim(strings.TrimSpace(configToolString(input, "path")), "/")
	if path == "" || strings.Contains(path, "..") {
		return result.fail("invalid_input", "read_file 必须提供仓库内的文件 path。")
	}
	ref := strings.TrimSpace(configToolString(input, "ref"))
	if number := repositoryIssueNumber(input); number > 0 && ref == "" {
		result.RequestedNumber = number
		pull, apiErr := t.getPullRequest(ctx, repository, number)
		if apiErr != nil {
			if apiErr.Code == "not_found" {
				return result.fail("not_a_pull_request", repositoryIssueFailureMessage("not_a_pull_request"))
			}
			return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
		}
		ref = strings.TrimSpace(pull.Head.SHA)
		result.PullRequest = repositoryPullRequestViewFromGitHub(pull)
	}
	escaped := make([]string, 0)
	for _, part := range strings.Split(path, "/") {
		escaped = append(escaped, url.PathEscape(part))
	}
	endpoint := fmt.Sprintf("/repos/%s/contents/%s", repository, strings.Join(escaped, "/"))
	if ref != "" {
		endpoint += "?" + url.Values{"ref": {ref}}.Encode()
	}
	var raw json.RawMessage
	if apiErr := t.doJSON(ctx, http.MethodGet, endpoint, nil, &raw); apiErr != nil {
		if apiErr.Code == "not_found" {
			return result.fail("not_found", "这个版本里没有该文件。被 PR 删除的文件在 PR head 上读不到，要看删除前的内容传 ref 为 base 分支；也可能是路径写错了，按 pull_files 返回的 path 填，不确定就先用 list_files 列目录。")
		}
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		return result.fail("invalid_input", "path 指向的是目录，不是文件；用 list_files 看里面有哪些文件。")
	}
	var content githubRepositoryContent
	if err := json.Unmarshal(raw, &content); err != nil {
		return result.fail("invalid_response", "GitHub 返回的文件信息无法解析。")
	}
	var data []byte
	switch {
	case content.Type == "file" && content.Encoding == "base64":
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(content.Content, "\n", ""))
		if err != nil {
			return result.fail("invalid_response", "GitHub 返回的文件内容无法解码。")
		}
		data = decoded
	case content.Type == "file" && content.Encoding == "none":
		// 超过 1MB 的文件 contents 接口不给内容，按原始格式再取一次。生成的代码、
		// 大 lockfile、单文件的大模块都在这一档，以前一律报「读不了」。
		var body repositoryRawBody
		if apiErr := t.doJSON(ctx, http.MethodGet, endpoint, nil, &body); apiErr != nil {
			return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
		}
		data = body.data
	default:
		return result.fail("invalid_input", "path 指向的不是可读取的文本文件（可能是子模块、符号链接或超过 GitHub 上限的大文件）。")
	}
	text := string(data)
	if strings.ContainsRune(text, 0) {
		return result.fail("invalid_input", "这是二进制文件，不能按文本读取。")
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	start := 1
	if value, ok := numberValue(input["start_line"]); ok && value >= 1 {
		start = int(value)
	}
	end := start + repositoryFileDefaultLines - 1
	if value, ok := numberValue(input["end_line"]); ok && int(value) >= start {
		end = int(value)
	}
	if end-start+1 > repositoryFileMaxLines {
		end = start + repositoryFileMaxLines - 1
	}
	if start > len(lines) {
		return result.fail("invalid_input", fmt.Sprintf("start_line 超出文件范围（共 %d 行）。", len(lines)))
	}
	if end > len(lines) {
		end = len(lines)
	}
	result.OK = true
	result.Outcome = "fetched"
	result.File = &repositoryFileView{Path: content.Path, Ref: ref, TotalLines: len(lines), StartLine: start, EndLine: end}
	// 先按空内容量出结果本身占多少，剩下的才是给代码的；续读位置按真正装进去的行算，
	// 不然 Runner 再截一刀，模型照着提示续读就跳过了中间那段。
	room := repositoryOutputBudget(ctx) - repositoryResultRunes(result) - repositoryOutputReserve
	window := repositoryFillLines(end-start+1, func(i int) string {
		return strconv.Itoa(start+i) + "| " + lines[start-1+i]
	}, end-start+1, room)
	shownEnd := start + window.Count - 1
	result.File.EndLine, result.File.Content = shownEnd, window.Text
	result.Message = fmt.Sprintf("已读取 %s 第 %d-%d 行（共 %d 行）。", content.Path, start, shownEnd, len(lines))
	if window.Cut {
		result.Message += fmt.Sprintf(" 第 %d 行太长（压缩过的代码或单行数据），只给了开头一段。", shownEnd)
	}
	if shownEnd < len(lines) {
		result.Message += fmt.Sprintf(" 后面还有，要继续读传 start_line=%d。", shownEnd+1)
		if shownEnd < end {
			result.Message += " 这次的输出上限装不下你要的全部行，已按上限给到这里。"
		}
	}
	return result
}

// repositoryRawBody 让 doJSON 按原始格式取文件内容，不做 JSON 解码。
type repositoryRawBody struct {
	data []byte
}

type repositoryFileView struct {
	Path       string `json:"path"`
	Ref        string `json:"ref,omitempty"`
	TotalLines int    `json:"total_lines"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	// Content 每行前面带「行号| 」，写行内评论时直接用这个行号。
	Content string `json:"content"`
}
