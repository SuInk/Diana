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

// 写操作要求内容出自用户原话。模型自己组织措辞时这条天然不成立——用户不会把
// 整段 issue 正文打进聊天框——此前一律硬失败，功能等于不可用。现在改为落成
// 待审批草稿：不碰 GitHub，等有权限的人明确同意再写。
func TestModelAuthoredIssueBecomesDraftInsteadOfFailing(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	tool := repositoryPublishTestTool(server, "帮我给 acme/demo 提个 issue，说 @Bot 的消息段被过滤掉了影响理解", nil)
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation":  "create",
		"repository": "acme/demo",
		"title":      "@ Bot 的消息段会被自动过滤，影响语义理解",
		"body":       "问题描述：\n模型自己组织的一大段正文，用户原话里没有。",
	})

	if !result.OK || result.Outcome != "draft_pending" || !result.RequiresApproval {
		t.Fatalf("模型撰写的 issue 应当落成待审批草稿：%#v", result)
	}
	if result.Draft == nil || result.Draft.Operation != "create" {
		t.Fatalf("草稿没有记录操作类型：%#v", result.Draft)
	}
	if len(github.requests) != 0 {
		t.Fatalf("草稿阶段不该碰 GitHub：%#v", github.requests)
	}
}

func TestModelAuthoredCommentBecomesDraftAndPostsAfterApproval(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	github.issues = []githubRepositoryIssue{{
		Number: 9, Title: "转发的 bug", State: "open",
		HTMLURL: "https://github.com/acme/demo/issues/9", UpdatedAt: time.Now().UTC(),
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	settings := SettingValues{
		repositoryPublishSettingToken:     repositoryPublishTestToken,
		repositoryPublishSettingAllowlist: "acme/demo",
		repositoryPublishSettingTimeout:   5,
	}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	toolFor := func(rawMessage string) *dianaRepositoryIssuesTool {
		return newDianaRepositoryIssuesTool(runtime,
			MessageEvent{Kind: EventKindPrivate, UserID: "owner", RawMessage: rawMessage},
			plugin, settings)
	}

	// 第一步：用户只说了大意，正文由模型撰写 —— 落草稿，不碰 GitHub。
	drafted := runRepositoryPublishTestTool(t, toolFor("在 acme/demo 的 #9 下面回复一下，说这个已经修好了"), map[string]any{
		"operation":  "comment",
		"repository": "acme/demo",
		"number":     9,
		"body":       "这个问题在 v0.8.52 已经修好了，更新后可以再试试。",
	})
	if !drafted.OK || drafted.Outcome != "draft_pending" || !drafted.RequiresApproval {
		t.Fatalf("模型撰写的评论应当落成待审批草稿：%#v", drafted)
	}
	if drafted.Draft == nil || drafted.Draft.Operation != "comment" || drafted.Draft.IssueTarget != 9 {
		t.Fatalf("评论草稿没有记录操作与目标：%#v", drafted.Draft)
	}
	if len(github.requests) != 0 {
		t.Fatalf("草稿阶段不该碰 GitHub：%#v", github.requests)
	}

	// 第二步：用户明确同意 —— 这时才真正发出去，且发的是评论而不是新建 Issue。
	approved := runRepositoryPublishTestTool(t, toolFor("同意，发吧"), map[string]any{
		"operation": "approve",
		"draft_id":  drafted.Draft.ID,
	})
	if !approved.OK {
		t.Fatalf("批准后应当发出评论：%#v", approved)
	}
	posted := false
	for _, request := range github.requests {
		if request.Method == http.MethodPost && strings.HasSuffix(request.Path, "/issues/9/comments") {
			posted = true
		}
		if request.Method == http.MethodPost && strings.HasSuffix(request.Path, "/repos/acme/demo/issues") {
			t.Fatalf("评论草稿被当成新建 Issue 执行了：%#v", github.requests)
		}
	}
	if !posted {
		t.Fatalf("没有向 #9 发出评论：%#v", github.requests)
	}
}

func TestUnapprovedDraftNeverReachesGitHub(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	settings := SettingValues{
		repositoryPublishSettingToken:     repositoryPublishTestToken,
		repositoryPublishSettingAllowlist: "acme/demo",
		repositoryPublishSettingTimeout:   5,
	}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	toolFor := func(rawMessage string) *dianaRepositoryIssuesTool {
		return newDianaRepositoryIssuesTool(runtime,
			MessageEvent{Kind: EventKindPrivate, UserID: "owner", RawMessage: rawMessage},
			plugin, settings)
	}

	drafted := runRepositoryPublishTestTool(t, toolFor("在 acme/demo 的 #9 下面回复一下"), map[string]any{
		"operation": "comment", "repository": "acme/demo", "number": 9, "body": "模型自己写的一句话",
	})
	if drafted.Draft == nil {
		t.Fatalf("没有生成草稿：%#v", drafted)
	}
	// 用户没有表示同意，草稿不得执行。
	rejected := runRepositoryPublishTestTool(t, toolFor("再想想吧"), map[string]any{
		"operation": "approve", "draft_id": drafted.Draft.ID,
	})
	if rejected.OK || rejected.FailureCode != "explicit_approval_required" {
		t.Fatalf("没有明确同意时不得执行草稿：%#v", rejected)
	}
	if len(github.requests) != 0 {
		t.Fatalf("未批准的草稿到达了 GitHub：%#v", github.requests)
	}
}
