// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// repositoryPullRequestTestGitHub 在 Issue 假服务外面补上 PR 相关接口。PR #85 同时是
// Issue 接口上的一个对象（带 pull_request 字段），和真实 GitHub 一致。
type repositoryPullRequestTestGitHub struct {
	*repositoryPublishTestGitHub

	pullMu       sync.Mutex
	files        []githubPullRequestFile
	reviews      []githubPullRequestReview
	reviewStatus int
	searchQuery  string
}

func newRepositoryPullRequestTestGitHub() *repositoryPullRequestTestGitHub {
	base := newRepositoryPublishTestGitHub()
	base.issues = []githubRepositoryIssue{
		{Number: 85, Title: "refactor: lazy worktree", Body: "PR 描述", State: "open", HTMLURL: "https://github.com/acme/demo/pull/85", PullRequest: &struct{}{}, UpdatedAt: time.Now().UTC()},
		{Number: 9, Title: "Tracked issue", State: "open", HTMLURL: "https://github.com/acme/demo/issues/9", UpdatedAt: time.Now().UTC()},
	}
	return &repositoryPullRequestTestGitHub{repositoryPublishTestGitHub: base}
}

func (s *repositoryPullRequestTestGitHub) handler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+repositoryPublishTestToken {
		s.repositoryPublishTestGitHub.handler(w, r)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/repos/acme/demo/pulls/"):
		s.pulls(w, r)
	case strings.HasPrefix(r.URL.Path, "/repos/acme/demo/contents/"):
		s.pullMu.Lock()
		s.searchQuery = r.URL.Query().Get("ref")
		s.pullMu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/repos/acme/demo/contents/")
		if path != "cmd/app.go" {
			http.NotFound(w, r)
			return
		}
		var body strings.Builder
		for i := 1; i <= 500; i++ {
			fmt.Fprintf(&body, "line %d\n", i)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(githubRepositoryContent{Type: "file", Path: path, Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte(body.String()))})
	case r.URL.Path == "/search/issues":
		s.pullMu.Lock()
		s.searchQuery = r.URL.Query().Get("q")
		s.pullMu.Unlock()
		s.repositoryPublishTestGitHub.mu.Lock()
		items := append([]githubRepositoryIssue(nil), s.issues...)
		s.repositoryPublishTestGitHub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/demo/issues/85/comments":
		// 真实 GitHub 给 PR 评论的链接在 /pull/ 下。
		payload := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		s.repositoryPublishTestGitHub.mu.Lock()
		s.requests = append(s.requests, repositoryPublishTestRequest{Method: r.Method, Path: r.URL.RequestURI(), Payload: payload})
		comment := githubIssueComment{Body: stringMapValue(payload, "body"), HTMLURL: fmt.Sprintf("https://github.com/acme/demo/pull/85#issuecomment-%d", len(s.comments[85])+1)}
		s.comments[85] = append(s.comments[85], comment)
		s.repositoryPublishTestGitHub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(comment)
	default:
		s.repositoryPublishTestGitHub.handler(w, r)
	}
}

func (s *repositoryPullRequestTestGitHub) pulls(w http.ResponseWriter, r *http.Request) {
	payload := map[string]any{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&payload)
	}
	s.repositoryPublishTestGitHub.mu.Lock()
	s.requests = append(s.requests, repositoryPublishTestRequest{Method: r.Method, Path: r.URL.RequestURI(), Payload: payload})
	s.repositoryPublishTestGitHub.mu.Unlock()
	s.pullMu.Lock()
	defer s.pullMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	rest := strings.TrimPrefix(r.URL.Path, "/repos/acme/demo/pulls/")
	parts := strings.Split(rest, "/")
	if number, _ := strconv.Atoi(parts[0]); number != 85 {
		http.NotFound(w, r)
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		mergeable := true
		pull := githubPullRequest{Number: 85, HTMLURL: "https://github.com/acme/demo/pull/85", State: "open", Mergeable: &mergeable, Additions: 587, Deletions: 236, ChangedFiles: len(s.files), Commits: 3}
		pull.Head.Ref, pull.Head.SHA, pull.Base.Ref = "perf/worktree-on-delegation", "abc123", "main"
		_ = json.NewEncoder(w).Encode(pull)
	case len(parts) == 2 && parts[1] == "files":
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		if page < 1 {
			page = 1
		}
		start, end := (page-1)*perPage, page*perPage
		lastPage := (len(s.files) + perPage - 1) / perPage
		if lastPage > 1 {
			w.Header().Set("Link", fmt.Sprintf(`<https://api.github.com/x?page=%d>; rel="last"`, lastPage))
		}
		if start > len(s.files) {
			start = len(s.files)
		}
		if end > len(s.files) {
			end = len(s.files)
		}
		_ = json.NewEncoder(w).Encode(s.files[start:end])
	case len(parts) == 2 && parts[1] == "reviews" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(s.reviews)
	case len(parts) == 2 && parts[1] == "reviews" && r.Method == http.MethodPost:
		if s.reviewStatus != 0 {
			http.Error(w, `{"message":"Unprocessable Entity"}`, s.reviewStatus)
			return
		}
		review := githubPullRequestReview{
			ID: int64(len(s.reviews) + 1), Body: stringMapValue(payload, "body"), State: "COMMENTED",
			HTMLURL: fmt.Sprintf("https://github.com/acme/demo/pull/85#pullrequestreview-%d", len(s.reviews)+1),
		}
		s.reviews = append(s.reviews, review)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(review)
	default:
		http.NotFound(w, r)
	}
}

func (s *repositoryPullRequestTestGitHub) postsTo(suffix string) []repositoryPublishTestRequest {
	s.repositoryPublishTestGitHub.mu.Lock()
	defer s.repositoryPublishTestGitHub.mu.Unlock()
	var out []repositoryPublishTestRequest
	for _, request := range s.requests {
		if request.Method == http.MethodPost && strings.HasSuffix(strings.SplitN(request.Path, "?", 2)[0], suffix) {
			out = append(out, request)
		}
	}
	return out
}

// get 读到 PR 时带上分支、合并状态、改动统计和全部 review，并提醒先读 pull_files。
func TestRepositoryPullRequestGetIncludesBranchesAndReviews(t *testing.T) {
	github := newRepositoryPullRequestTestGitHub()
	for i := 1; i <= 12; i++ {
		github.reviews = append(github.reviews, githubPullRequestReview{ID: int64(i), Body: fmt.Sprintf("review %d", i), State: "COMMENTED", HTMLURL: fmt.Sprintf("https://github.com/acme/demo/pull/85#pullrequestreview-%d", i)})
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "看下 acme/demo 的 PR 85", nil)

	result := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "get", "repository": "acme/demo", "number": 85})
	if !result.OK || result.PullRequest == nil {
		t.Fatalf("result=%#v", result)
	}
	if result.PullRequest.HeadRef != "perf/worktree-on-delegation" || result.PullRequest.BaseRef != "main" || result.PullRequest.Additions != 587 {
		t.Fatalf("pull request=%#v", result.PullRequest)
	}
	if len(result.Reviews) != 12 || result.Reviews[0].Body != "review 1" || result.Reviews[11].Body != "review 12" {
		t.Fatalf("reviews=%#v", result.Reviews)
	}
	if result.IssueBody != "PR 描述" || !strings.Contains(result.Message, "pull_files") {
		t.Fatalf("message=%q body=%q", result.Message, result.IssueBody)
	}
}

// pull_files 从 GitHub 分页读全，再按整个文件分页给模型：给出的 patch 都是完整的，
// 按 file_offset 续读能读全；paths 按目录前缀筛选。
func TestRepositoryPullRequestFilesFocusAndPaging(t *testing.T) {
	github := newRepositoryPullRequestTestGitHub()
	long := strings.Repeat("+line\n", 2_000)
	github.files = append(github.files, githubPullRequestFile{Filename: "cmd/app.go", Status: "modified", Additions: 2000, Patch: long})
	github.files = append(github.files, githubPullRequestFile{Filename: "assets/logo.png", Status: "added"})
	for i := 0; i < 120; i++ {
		github.files = append(github.files, githubPullRequestFile{Filename: fmt.Sprintf("sidecar/pi/file%03d.js", i), Status: "modified", Additions: 1, Patch: "@@ -1 +1 @@\n-a\n+b"})
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "review acme/demo PR 85", nil)

	var files []repositoryPullRequestFileView
	offset := 0
	for calls := 1; ; calls++ {
		if calls > 20 {
			t.Fatal("file_offset 续读没有收敛")
		}
		input := map[string]any{"operation": "pull_files", "repository": "acme/demo", "number": 85}
		if offset > 0 {
			input["file_offset"] = offset
		}
		page := runRepositoryToolWithBudget(t, tool, agent.ToolOutputMaxChars, input)
		if !page.OK || len(page.Files) == 0 {
			t.Fatalf("page=%#v", page)
		}
		files = append(files, page.Files...)
		if !page.FilesTruncated {
			break
		}
		if !strings.Contains(page.Message, fmt.Sprintf("file_offset=%d", page.NextFileOffset)) {
			t.Fatalf("续读提示：%q", page.Message)
		}
		offset = page.NextFileOffset
	}
	if len(files) != len(github.files) {
		t.Fatalf("续读应读到全部 %d 个文件，got %d", len(github.files), len(files))
	}
	for index, file := range files {
		if file.PatchTruncated || file.Patch != strings.TrimSuffix(github.files[index].Patch, "\n") {
			t.Fatalf("%s 的 patch 应完整给出", file.Path)
		}
	}
	if !files[1].PatchUnavailable {
		t.Fatalf("二进制文件应标记 patch_unavailable：%#v", files[1])
	}

	focused := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "pull_files", "repository": "acme/demo", "number": 85, "paths": []any{"cmd"}})
	if !focused.OK || len(focused.Files) != 1 || focused.Files[0].Path != "cmd/app.go" || focused.Files[0].Patch != strings.TrimSuffix(long, "\n") {
		t.Fatalf("paths 按目录前缀筛选：%#v", focused.Files)
	}

	issue := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "pull_files", "repository": "acme/demo", "number": 9})
	if issue.OK || issue.FailureCode != "not_a_pull_request" {
		t.Fatalf("Issue 上读 pull_files 应报 not_a_pull_request：%#v", issue)
	}
}

// PR 的评论走 Issue 评论接口，GitHub 返回的链接在 /pull/ 下，不能被当成异常响应拒掉。
func TestRepositoryPullRequestCommentAcceptsPullURL(t *testing.T) {
	github := newRepositoryPullRequestTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "在 acme/demo PR 85 下面评论", nil)

	result := runRepositoryPublishTestTool(t, tool, map[string]any{"operation": "comment", "repository": "acme/demo", "number": 85, "body": "LGTM，补一个超时测试"})
	if !result.OK || result.Outcome != "commented" || !strings.Contains(result.CommentURL, "/pull/85#issuecomment-") || !strings.Contains(result.Message, "Pull Request") {
		t.Fatalf("result=%#v", result)
	}
}

// review 先出草稿，草稿里能看到每条行内评论；确认后按 COMMENT 提交，同一份内容重试不重复提交。
func TestRepositoryPullRequestReviewDraftSubmitAndIdempotency(t *testing.T) {
	github := newRepositoryPullRequestTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "把 review 意见发到 acme/demo PR 85", nil)
	input := map[string]any{
		"operation": "review", "repository": "acme/demo", "number": 85, "operation_id": "review-85",
		"body": "整体方向没问题，两处需要确认。",
		"comments": []any{
			map[string]any{"path": "sidecar/pi/bridge.js", "line": 1305, "body": "并行两次委托会同时进 prepare，这里要加锁"},
			map[string]any{"path": "cmd/milksu-backend/app_coding_collaboration.go", "line": 88, "side": "right", "body": "已有 1 个 writer 时再委托 2 个写入角色不会补建"},
		},
	}

	draft := runRepositoryPublishToolOnce(t, tool, cloneRepositoryPullRequestInput(input))
	if draft.Outcome != "draft_pending" || draft.Draft == nil || draft.Draft.Operation != "review" || len(draft.Draft.ReviewComments) != 2 || draft.Draft.ConfirmationCode == "" {
		t.Fatalf("draft=%#v", draft)
	}
	if len(github.postsTo("/reviews")) != 0 {
		t.Fatal("草稿阶段不该提交 review")
	}

	first := runRepositoryPublishTestTool(t, tool, cloneRepositoryPullRequestInput(input))
	if !first.OK || first.Outcome != "reviewed" || !strings.Contains(first.ReviewURL, "/pull/85#pullrequestreview-") {
		t.Fatalf("first=%#v", first)
	}
	posts := github.postsTo("/pulls/85/reviews")
	if len(posts) != 1 {
		t.Fatalf("review POST=%d, want 1", len(posts))
	}
	payload := posts[0].Payload
	comments, _ := payload["comments"].([]any)
	if payload["event"] != "COMMENT" || len(comments) != 2 || !strings.Contains(stringMapValue(payload, "body"), "diana-operation:review:") {
		t.Fatalf("review payload=%#v", payload)
	}
	if side := stringMapValue(comments[1].(map[string]any), "side"); side != "RIGHT" {
		t.Fatalf("side 应规范成 RIGHT，got %q", side)
	}

	second := runRepositoryPublishTestTool(t, tool, cloneRepositoryPullRequestInput(input))
	if !second.OK || second.Outcome != "reused" || !second.Idempotent {
		t.Fatalf("second=%#v", second)
	}
	if len(github.postsTo("/pulls/85/reviews")) != 1 {
		t.Fatal("同一份 review 重试不该重复提交")
	}
}

// review 的边界：只能对 PR；行内评论参数不合法在草稿阶段就拒；GitHub 422 时说明怎么修。
func TestRepositoryPullRequestReviewBoundaries(t *testing.T) {
	github := newRepositoryPullRequestTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "review", nil)

	onIssue := runRepositoryPublishTestTool(t, tool, map[string]any{"operation": "review", "repository": "acme/demo", "number": 9, "body": "看过了"})
	if onIssue.OK || onIssue.FailureCode != "not_a_pull_request" {
		t.Fatalf("对 Issue 提交 review 应拒绝：%#v", onIssue)
	}

	badLine := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "review", "repository": "acme/demo", "number": 85, "body": "看过了",
		"comments": []any{map[string]any{"path": "a.go", "line": 0, "body": "x"}}})
	if badLine.OK || badLine.FailureCode != "invalid_input" {
		t.Fatalf("line=0 应在草稿阶段拒绝：%#v", badLine)
	}

	github.reviewStatus = http.StatusUnprocessableEntity
	rejected := runRepositoryPublishTestTool(t, tool, map[string]any{"operation": "review", "repository": "acme/demo", "number": 85, "body": "看过了",
		"comments": []any{map[string]any{"path": "a.go", "line": 999, "body": "这行不在 diff 里"}}})
	if rejected.OK || rejected.FailureCode != "validation_failed" || !strings.Contains(rejected.Message, "pull_files") {
		t.Fatalf("422 应说明行号不在 diff 里：%#v", rejected)
	}

	update := runRepositoryPublishTestTool(t, tool, map[string]any{"operation": "update", "repository": "acme/demo", "number": 85, "append_body": "补充"})
	if update.OK || !strings.Contains(update.Message, "review") {
		t.Fatalf("update 仍只能用于 Issue，报错要指出 PR 能用的操作：%#v", update)
	}
}

// search 按 kind 区分 Issue 和 PR，限定符由工具加，不让模型注入。
func TestRepositoryIssueSearchByKind(t *testing.T) {
	github := newRepositoryPullRequestTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "搜 PR", nil)

	pulls := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "search", "repository": "acme/demo", "query": "worktree", "kind": "pull_request"})
	if !pulls.OK || len(pulls.Items) != 1 || pulls.Items[0].Number != 85 || !strings.Contains(github.searchQuery, "is:pr") {
		t.Fatalf("pulls=%#v query=%q", pulls, github.searchQuery)
	}
	issues := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "search", "repository": "acme/demo", "query": "worktree"})
	if !issues.OK || len(issues.Items) != 1 || issues.Items[0].Number != 9 || !strings.Contains(github.searchQuery, "is:issue") {
		t.Fatalf("issues=%#v query=%q", issues, github.searchQuery)
	}
}

func cloneRepositoryPullRequestInput(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

// read_file 传 PR number 时读 PR head 那一版，带行号分段读，超出范围说清楚。
func TestRepositoryReadFileAtPullRequestHead(t *testing.T) {
	github := newRepositoryPullRequestTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "看下 cmd/app.go", nil)

	first := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "read_file", "repository": "acme/demo", "number": 85, "path": "cmd/app.go"})
	if !first.OK || first.File == nil || first.File.Ref != "abc123" || github.searchQuery != "abc123" {
		t.Fatalf("应读 PR head：%#v ref=%q", first.File, github.searchQuery)
	}
	if first.File.TotalLines != 500 || first.File.StartLine != 1 || first.File.EndLine != repositoryFileDefaultLines || !strings.HasPrefix(first.File.Content, "1| line 1\n") || !strings.Contains(first.Message, "start_line=401") {
		t.Fatalf("file=%#v message=%q", first.File, first.Message)
	}
	tail := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "read_file", "repository": "acme/demo", "number": 85, "path": "cmd/app.go", "start_line": 480, "end_line": 900})
	if !tail.OK || tail.File.StartLine != 480 || tail.File.EndLine != 500 || !strings.HasPrefix(tail.File.Content, "480| line 480") {
		t.Fatalf("tail=%#v", tail.File)
	}
	outside := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "read_file", "repository": "acme/demo", "path": "cmd/app.go", "start_line": 600})
	if outside.OK || !strings.Contains(outside.Message, "共 500 行") {
		t.Fatalf("outside=%#v", outside)
	}
	escape := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "read_file", "repository": "acme/demo", "path": "../secrets"})
	if escape.OK || escape.FailureCode != "invalid_input" {
		t.Fatalf("escape=%#v", escape)
	}
}
