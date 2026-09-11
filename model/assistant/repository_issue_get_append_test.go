// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// get 是只读的：不落草稿、不要确认码，直接把正文和评论读回来，隐藏的对账标记
// 不给模型看。没有它，模型改已有 Issue 只能凭记忆重写整段正文。
func TestRepositoryIssueGetReturnsBodyAndCommentsWithoutMarkers(t *testing.T) {
	fingerprint := strings.Repeat("b", 64)
	createMarker := repositoryIssueOperationMarker("create", fingerprint)
	commentMarker := repositoryIssueOperationMarkerWithPayload("comment", fingerprint, strings.Repeat("c", 64))
	github := newRepositoryPublishTestGitHub()
	github.issues = []githubRepositoryIssue{{
		Number: 7, Title: "下拉框透明", Body: "问题描述在这里。\n\n" + createMarker, State: "open",
		HTMLURL: "https://github.com/acme/demo/issues/7", UpdatedAt: time.Now().UTC(),
		Labels: []struct {
			Name string `json:"name"`
		}{{Name: "bug"}},
	}}
	github.comments[7] = []githubIssueComment{{
		Body: "复现版本 26.905.2\n\n" + commentMarker, HTMLURL: "https://github.com/acme/demo/issues/7#issuecomment-1",
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "看看 acme/demo #7 现在写了什么", nil)

	result := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "get", "repository": "acme/demo", "number": 7})
	if !result.OK || result.Outcome != "fetched" || result.Draft != nil {
		t.Fatalf("result=%#v", result)
	}
	if result.Issue == nil || result.Issue.Number != 7 || result.Issue.Title != "下拉框透明" || len(result.Issue.Labels) != 1 {
		t.Fatalf("issue summary=%#v", result.Issue)
	}
	if result.IssueBody != "问题描述在这里。" {
		t.Fatalf("issue body=%q", result.IssueBody)
	}
	if len(result.Comments) != 1 || result.Comments[0].Body != "复现版本 26.905.2" || result.Comments[0].URL == "" {
		t.Fatalf("comments=%#v", result.Comments)
	}
	if github.count(http.MethodPost) != 0 || github.count(http.MethodPatch) != 0 {
		t.Fatalf("get issued writes: %#v", github.requests)
	}
	for _, alias := range []string{"view", "read", "show", "get_issue"} {
		if normalizeRepositoryIssueOperation(alias, "") != "get" {
			t.Fatalf("alias %q did not normalize to get", alias)
		}
	}
}

// append_body 只往末尾加内容：原正文一字不动，对账标记仍然留在最末尾且只有一份。
func TestRepositoryIssueUpdateAppendBodyKeepsExistingBody(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	marker := repositoryIssueOperationMarker("create", fingerprint)
	github := newRepositoryPublishTestGitHub()
	github.issues = []githubRepositoryIssue{{
		Number: 5, Title: "Old", Body: "原来的正文\n\n" + marker, State: "open",
		HTMLURL: "https://github.com/acme/demo/issues/5", UpdatedAt: time.Now().UTC(),
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "给 acme/demo #5 补一句复现版本", nil)

	draft := runRepositoryPublishToolOnce(t, tool, map[string]any{
		"operation": "update", "repository": "acme/demo", "number": 5, "append_body": "复现版本：26.905.2",
	})
	if draft.Outcome != "draft_pending" || draft.Draft == nil || draft.Draft.AppendBody != "复现版本：26.905.2" || draft.Draft.Body != "" {
		t.Fatalf("draft=%#v", draft)
	}
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "update", "repository": "acme/demo", "number": 5, "append_body": "复现版本：26.905.2",
	})
	if !result.OK || result.Outcome != "updated" {
		t.Fatalf("result=%#v", result)
	}
	body := stringMapValue(github.last(http.MethodPatch).Payload, "body")
	if body != "原来的正文\n\n复现版本：26.905.2\n\n"+marker {
		t.Fatalf("appended body = %q", body)
	}
	if _, present := github.last(http.MethodPatch).Payload["title"]; present {
		t.Fatalf("append_body must not touch the title: %#v", github.last(http.MethodPatch).Payload)
	}
}

// 什么都不改的 update 在落草稿前就该拒绝，而不是让用户确认一份空草稿。
func TestRepositoryIssueUpdateWithoutChangesIsRejectedBeforeDraft(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	github.issues = []githubRepositoryIssue{{
		Number: 5, Title: "Old", Body: "body", State: "open",
		HTMLURL: "https://github.com/acme/demo/issues/5", UpdatedAt: time.Now().UTC(),
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "改一下 acme/demo #5", nil)
	result := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "update", "repository": "acme/demo", "number": 5})
	if result.OK || result.FailureCode != "invalid_input" || result.Draft != nil {
		t.Fatalf("result=%#v", result)
	}
}

// 对几个 Issue 做同一件事只需要一份草稿、一个确认码；逐条建草稿会让用户连打三次码。
func TestRepositoryIssueBatchCommentUsesOneDraft(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	for _, number := range []int{67, 68, 69} {
		github.issues = append(github.issues, githubRepositoryIssue{
			Number: number, Title: "Issue", Body: "body", State: "open",
			HTMLURL: "https://github.com/acme/demo/issues/" + itoa(number), UpdatedAt: time.Now().UTC(),
		})
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "给 acme/demo 的 #67 #68 #69 都注明版本", nil)
	input := map[string]any{"operation": "comment", "repository": "acme/demo", "numbers": []any{67.0, 68.0, 69.0, 68.0}, "body": "复现版本：26.905.2"}

	draft := runRepositoryPublishToolOnce(t, tool, input)
	if draft.Outcome != "draft_pending" || draft.Draft == nil {
		t.Fatalf("draft=%#v", draft)
	}
	if got := draft.Draft.IssueTargets; len(got) != 3 || got[0] != 67 || got[1] != 68 || got[2] != 69 {
		t.Fatalf("issue targets=%#v", got)
	}
	result := runRepositoryPublishTestTool(t, tool, input)
	if !result.OK || result.Outcome != "commented" || len(result.Items) != 3 || len(result.Failures) != 0 {
		t.Fatalf("result=%#v", result)
	}
	if github.count(http.MethodPost) != 3 {
		t.Fatalf("expected one comment per issue, got %d POSTs", github.count(http.MethodPost))
	}
	for _, number := range []int{67, 68, 69} {
		if comments := github.comments[number]; len(comments) != 1 || !strings.Contains(comments[0].Body, "复现版本：26.905.2") {
			t.Fatalf("issue %d comments=%#v", number, comments)
		}
	}
}

// 批量里有一条失败不拦其余的，结果要逐条说明哪些成了、哪些没成。
func TestRepositoryIssueBatchReportsPartialFailure(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	github.issues = []githubRepositoryIssue{{
		Number: 5, Title: "Only one", Body: "body", State: "open",
		HTMLURL: "https://github.com/acme/demo/issues/5", UpdatedAt: time.Now().UTC(),
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "把 acme/demo #5 和 #6 都补上版本号", nil)
	input := map[string]any{"operation": "update", "repository": "acme/demo", "numbers": []any{5.0, 6.0}, "append_body": "复现版本：26.905.2"}
	result := runRepositoryPublishTestTool(t, tool, input)
	if result.OK || result.Outcome != "partial" || result.FailureCode != "partial_failure" {
		t.Fatalf("result=%#v", result)
	}
	if len(result.Items) != 1 || result.Items[0].Number != 5 || len(result.Failures) != 1 || result.Failures[0].Number != 6 {
		t.Fatalf("items=%#v failures=%#v", result.Items, result.Failures)
	}
	issue, _ := github.issue(5)
	if !strings.HasSuffix(issue.Body, "复现版本：26.905.2") || !strings.HasPrefix(issue.Body, "body") {
		t.Fatalf("issue 5 body=%q", issue.Body)
	}
	// 草稿已经用掉：再拿同一个 draft_id 审批不会把 #5 再改一遍。
	again := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "list_drafts"})
	for _, draft := range again.Drafts {
		if draft.Status == "pending" {
			t.Fatalf("partially executed draft is still pending: %#v", draft)
		}
	}
}
