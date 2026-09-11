// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 上一轮刚批准提交的 Issue，下一轮模型在上下文里看不到结果时会再建一份同标题草稿。
// 工具这一层要把它拦下来：直接报已经建好的编号，不再建草稿、不再要确认码。
func TestRepositoryIssueCreateAfterApprovalReportsExistingIssue(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "嘉然提个 issue：区分 CVE 本地同步数据源与实际跟踪项", nil)
	input := map[string]any{"operation": "create", "repository": "acme/demo", "title": "区分 CVE 本地同步数据源与实际跟踪项", "body": "问题背景……"}

	created := runRepositoryPublishTestTool(t, tool, input)
	if !created.OK || created.Outcome != "created" || created.Issue == nil {
		t.Fatalf("created=%#v", created)
	}

	again := runRepositoryPublishToolOnce(t, tool, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "  区分 CVE 本地同步数据源与实际跟踪项 ", "body": "换了措辞的正文",
	})
	if !again.OK || again.Outcome != "already_created" || again.Issue == nil || again.Issue.Number != created.Issue.Number || again.RequiresApproval {
		t.Fatalf("second create=%#v", again)
	}
	if github.count(http.MethodPost) != 1 {
		t.Fatalf("duplicate create reached GitHub: %d POSTs", github.count(http.MethodPost))
	}

	// 默认草稿列表要能看见「刚提交过」这件事，而不是空列表。
	listed := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "list_drafts"})
	if len(listed.Drafts) != 1 || listed.Drafts[0].Status != "created" || listed.Drafts[0].IssueNumber != created.Issue.Number || listed.Drafts[0].ConfirmationCode != "" {
		t.Fatalf("listed=%#v", listed)
	}
	if listed.Message == "" || !containsAll(listed.Message, "已处理", "不要再") {
		t.Fatalf("list message does not warn against re-creating: %q", listed.Message)
	}

	// 用户明确要再建一份时才放行。
	forced := runRepositoryPublishToolOnce(t, tool, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "区分 CVE 本地同步数据源与实际跟踪项", "body": "第二份", "allow_duplicate": true,
	})
	if forced.Outcome != "draft_pending" || forced.Draft == nil {
		t.Fatalf("forced=%#v", forced)
	}
}

// 同标题的草稿还在等确认时，再建一次只是把原草稿和确认码再给一遍。
func TestRepositoryIssueSameTitlePendingDraftIsReused(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "嘉然提个 issue：下拉框不能透明", nil)
	input := map[string]any{"operation": "create", "repository": "acme/demo", "title": "下拉框不能透明", "body": "会和背景重叠"}

	first := runRepositoryPublishToolOnce(t, tool, input)
	second := runRepositoryPublishToolOnce(t, tool, input)
	if first.Outcome != "draft_pending" || second.Outcome != "draft_pending" || first.Draft == nil || second.Draft == nil {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if first.Draft.ID != second.Draft.ID || second.Draft.ConfirmationCode != first.Draft.ConfirmationCode {
		t.Fatalf("a second draft was created: first=%s second=%s", first.Draft.ID, second.Draft.ID)
	}
	listed := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "list_drafts", "status": "pending"})
	if len(listed.Drafts) != 1 {
		t.Fatalf("pending drafts=%#v", listed.Drafts)
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
