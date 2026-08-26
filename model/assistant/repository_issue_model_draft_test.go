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
	result := runRepositoryPublishToolOnce(t, tool, map[string]any{
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
	drafted := runRepositoryPublishToolOnce(t, toolFor("在 acme/demo 的 #9 下面回复一下，说这个已经修好了"), map[string]any{
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

	// 第二步：用户原样打出确认码 —— 这时才真正发出去，且发的是评论而不是新建 Issue。
	approved := runRepositoryPublishToolOnce(t, toolFor("确认 "+repositoryIssueConfirmationCode(drafted.Draft.ID)), map[string]any{
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

	drafted := runRepositoryPublishToolOnce(t, toolFor("在 acme/demo 的 #9 下面回复一下"), map[string]any{
		"operation": "comment", "repository": "acme/demo", "number": 9, "body": "模型自己写的一句话",
	})
	if drafted.Draft == nil {
		t.Fatalf("没有生成草稿：%#v", drafted)
	}
	// 消息里没有确认码，草稿不得执行。以前这里判的是「有没有说同意」，靠一张同意/
	// 拒绝词表；换成确认码之后，「再想想吧」和任何其他措辞一样都不放行。
	rejected := runRepositoryPublishToolOnce(t, toolFor("再想想吧"), map[string]any{
		"operation": "approve", "draft_id": drafted.Draft.ID,
	})
	if rejected.OK || rejected.FailureCode != "explicit_approval_required" {
		t.Fatalf("没有明确同意时不得执行草稿：%#v", rejected)
	}
	if len(github.requests) != 0 {
		t.Fatalf("未批准的草稿到达了 GitHub：%#v", github.requests)
	}
}

// 两条审批消息并发到达：第一条已经创建成功并把草稿标记为 created，第二条按
// pending 查就查不到。此前照直报「本群找不到草稿」，用户看到的是「其实建好了
// 却说失败」。现在必须照实说明已经提交过，并带上 Issue 编号。
func TestApprovingAnAlreadyAppliedDraftReportsTheTruth(t *testing.T) {
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

	drafted := runRepositoryPublishToolOnce(t, toolFor("帮我给 acme/demo 提个 issue，说转发有问题"), map[string]any{
		"operation": "create", "repository": "acme/demo",
		"title": "转发有问题，需要排查", "body": "模型自己组织的正文。",
	})
	if drafted.Draft == nil {
		t.Fatalf("没有生成草稿：%#v", drafted)
	}

	code := repositoryIssueConfirmationCode(drafted.Draft.ID)
	first := runRepositoryPublishToolOnce(t, toolFor("确认 "+code), map[string]any{
		"operation": "approve", "draft_id": drafted.Draft.ID,
	})
	if !first.OK || first.Issue == nil {
		t.Fatalf("第一条审批应当创建成功：%#v", first)
	}
	createdNumber := first.Issue.Number
	writesAfterFirst := len(github.requests)

	// 第二条审批：草稿已被消费，但 Issue 确实已经建好了。
	second := runRepositoryPublishToolOnce(t, toolFor("确认 "+code), map[string]any{
		"operation": "approve", "draft_id": drafted.Draft.ID,
	})
	if !second.OK || second.Outcome != "already_applied" || !second.Idempotent {
		t.Fatalf("重复审批应当照实报告已提交过：%#v", second)
	}
	if second.RequestedNumber != createdNumber {
		t.Fatalf("重复审批没有指出实际创建的 Issue：want #%d, got %#v", createdNumber, second)
	}
	for _, request := range github.requests[writesAfterFirst:] {
		if request.Method == http.MethodPost {
			t.Fatalf("重复审批产生了第二次写入：%#v", request)
		}
	}
}

// 列草稿只报「共找到 N 条」，用户看完还是不知道拿什么去确认——确认码藏在草稿 ID 的
// 前六位，指望模型自己截字符串不可靠。待审批的草稿必须自带 confirmation_code。
func TestListDraftsCarriesConfirmationCode(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	settings := SettingValues{
		repositoryPublishSettingToken:      repositoryPublishTestToken,
		repositoryPublishSettingAllowlist:  "acme/demo",
		repositoryPublishSettingTimeout:    5,
		repositoryPublishSettingUserAccess: "owner = acme/demo",
	}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	toolFor := func(rawMessage string) *dianaRepositoryIssuesTool {
		return newDianaRepositoryIssuesTool(runtime,
			MessageEvent{Kind: EventKindPrivate, UserID: "owner", RawMessage: rawMessage},
			plugin, settings)
	}

	drafted := runRepositoryPublishToolOnce(t, toolFor("帮我给 acme/demo 提个 issue，说转发有问题"), map[string]any{
		"operation": "create", "repository": "acme/demo",
		"title": "转发有问题，需要排查", "body": "模型自己组织的正文。",
	})
	if drafted.Draft == nil {
		t.Fatalf("没有生成草稿：%#v", drafted)
	}
	code := repositoryIssueConfirmationCode(drafted.Draft.ID)
	if drafted.Draft.ConfirmationCode != code {
		t.Fatalf("建草稿时没带确认码：%#v", drafted.Draft)
	}
	// 「确认码要发出去」和「确认码只能由用户自己打出来」是两件事。原先合成一句
	// 「不要替用户说出确认码」，模型可以读成「别把确认码说出来」，于是真的不发，
	// 管理员无从确认。文案必须明确要求写进回复。
	if !strings.Contains(drafted.Message, code) || !strings.Contains(drafted.Message, "写进这次回复") {
		t.Fatalf("建草稿文案没有要求把确认码发出去：%q", drafted.Message)
	}
	for _, message := range []string{drafted.Message} {
		if strings.Contains(message, "不要替用户说出确认码") {
			t.Fatalf("文案仍然含有会被读成「别说出确认码」的措辞：%q", message)
		}
	}

	listed := runRepositoryPublishToolOnce(t, toolFor("看看有哪些草稿"), map[string]any{"operation": "list_drafts"})
	if len(listed.Drafts) != 1 {
		t.Fatalf("草稿列表 = %#v", listed)
	}
	if listed.Drafts[0].ConfirmationCode != code {
		t.Fatalf("草稿列表里没有确认码：%#v", listed.Drafts[0])
	}
	// 光把码放进载荷不够，还得让模型知道要把它写进回复。
	if !strings.Contains(listed.Message, "confirmation_code") || !strings.Contains(listed.Message, "原样写进回复") {
		t.Fatalf("列表文案没有要求把确认码发出去：%q", listed.Message)
	}
	if strings.Contains(listed.Message, "不要替用户说出确认码") {
		t.Fatalf("列表文案仍然含有会被读成「别说出确认码」的措辞：%q", listed.Message)
	}

	// 已经提交过的草稿不再给确认码：报一个码只会让人以为还能确认。
	approved := runRepositoryPublishToolOnce(t, toolFor("确认 "+code), map[string]any{
		"operation": "approve", "draft_id": drafted.Draft.ID,
	})
	if !approved.OK {
		t.Fatalf("审批失败：%#v", approved)
	}
	done := runRepositoryPublishToolOnce(t, toolFor("看看有哪些草稿"), map[string]any{
		"operation": "list_drafts", "status": "all",
	})
	for _, draft := range done.Drafts {
		if draft.Status != "pending" && draft.ConfirmationCode != "" {
			t.Fatalf("已处理的草稿不该带确认码：%#v", draft)
		}
	}
}
