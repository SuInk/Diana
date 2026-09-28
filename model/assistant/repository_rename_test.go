// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newRenamedRepositoryServer 模拟 acme/old 改名成了 acme/demo：旧名的 REST 读请求回 301、
// 写请求回 307，都指向按 ID 寻址的 /repositories/7。withGraphQL 为 true 时 GraphQL
// 会悄悄跟到新仓库，返回新名字下的链接——线上撞到的就是这一种。
func newRenamedRepositoryServer(github *repositoryPublishTestGitHub, withGraphQL bool) *httptest.Server {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/graphql" && withGraphQL:
			if r.Header.Get("Authorization") != "Bearer "+repositoryPublishTestToken {
				http.Error(w, `{"message":"bad token"}`, http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"issues": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false},
				"nodes": []map[string]any{{
					"number": 5, "title": "旧问题", "state": "OPEN", "url": "https://github.com/acme/demo/issues/5",
					"updatedAt": "2026-01-01T00:00:00Z",
				}},
			}}}})
		case strings.HasPrefix(r.URL.Path, "/repos/acme/old"):
			status := http.StatusMovedPermanently
			if r.Method != http.MethodGet {
				status = http.StatusTemporaryRedirect
			}
			http.Redirect(w, r, server.URL+"/repositories/7"+strings.TrimPrefix(r.URL.Path, "/repos/acme/old"), status)
		case r.URL.Path == "/repositories/7":
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "acme/demo"})
		default:
			github.handler(w, r)
		}
	}))
	return server
}

func assertCreatedInRenamedRepository(t *testing.T, github *repositoryPublishTestGitHub, result repositoryIssueResult) {
	t.Helper()
	if !result.OK || result.Outcome != "created" || result.Repository != "acme/demo" || result.RedirectRepository != "acme/demo" || !strings.Contains(result.Message, "acme/old") {
		t.Fatalf("approve on renamed repository=%#v", result)
	}
	if github.count(http.MethodPost) != 1 || github.last(http.MethodPost).Path != "/repos/acme/demo/issues" {
		t.Fatalf("posts=%d last=%q", github.count(http.MethodPost), github.last(http.MethodPost).Path)
	}
}

func approveWithCode(t *testing.T, tool *dianaGitHubTool, draftID string) repositoryIssueResult {
	t.Helper()
	tool.event.RawMessage = repositoryIssueConfirmationCode(draftID)
	return runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "approve", "draft_id": draftID})
}

// 线上那一次：GraphQL 悄悄跟到新仓库，旧名对不上链接，以前报成「无法解析的响应」，
// 确认码回多少遍都卡在同一处。现在同一次 approve 里就跟到新名字写进去。
func TestRepositoryIssueApproveFollowsRenamedRepository(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := newRenamedRepositoryServer(github, true)
	defer server.Close()

	tool := repositoryPublishTestTool(server, "给 acme/old 提个 issue", nil)
	draft := runRepositoryPublishToolOnce(t, tool, map[string]any{
		"operation": "create", "repository": "acme/old", "title": "登录失败", "body": "重置密码后无法登录。",
	})
	if !draft.OK || draft.Draft == nil {
		t.Fatalf("draft=%#v", draft)
	}
	assertCreatedInRenamedRepository(t, github, approveWithCode(t, tool, draft.Draft.ID))
	stored, ok, err := tool.plugin.findResolvedDraft(context.Background(), "private:owner", draft.Draft.ID)
	if err != nil || !ok || stored.Repository != "acme/demo" || stored.Status != "created" {
		t.Fatalf("stored draft=%#v ok=%v err=%v", stored, ok, err)
	}
}

// 群里的审批人不是主人，设置里还写着旧名：旧名过了授权，写入跟到新名字。
func TestRepositoryIssueGroupApproveFollowsRenameWithOldNameSettings(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := newRenamedRepositoryServer(github, false)
	defer server.Close()
	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	settings := SettingValues{
		repositoryPublishSettingAllowlist:      "acme/old",
		repositoryPublishSettingGroupAccess:    "group-1 = acme/old",
		repositoryPublishSettingApproverGroups: "group-1 = approver",
		repositoryPublishSettingUserTokens:     `{"approver":"` + repositoryPublishTestToken + `"}`,
		repositoryPublishSettingTimeout:        5,
	}
	requester := newDianaGitHubTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "member", RawMessage: "提个 issue"}, plugin, settings)
	draft := runRepositoryPublishToolOnce(t, requester, map[string]any{"operation": "create", "repository": "acme/old", "title": "登录失败"})
	if draft.Draft == nil {
		t.Fatalf("draft=%#v", draft)
	}
	approver := newDianaGitHubTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "approver"}, plugin, settings)
	assertCreatedInRenamedRepository(t, github, approveWithCode(t, approver, draft.Draft.ID))
}

// 草稿是改名前起的，管理员已经把设置改成新名字（线上就是这样）：新名字过了授权也算。
func TestRepositoryIssueGroupApproveFollowsRenameWithNewNameSettings(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := newRenamedRepositoryServer(github, true)
	defer server.Close()
	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	settingsFor := func(repository string) SettingValues {
		return SettingValues{
			repositoryPublishSettingToken:         repositoryPublishTestToken,
			repositoryPublishSettingAllowlist:     repository,
			repositoryPublishSettingManagerGroups: "group-1 = " + repository,
			repositoryPublishSettingTimeout:       5,
		}
	}
	requester := newDianaGitHubTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "member", RawMessage: "提个 issue"}, plugin, settingsFor("acme/old"))
	draft := runRepositoryPublishToolOnce(t, requester, map[string]any{"operation": "create", "repository": "acme/old", "title": "登录失败"})
	if draft.Draft == nil {
		t.Fatalf("draft=%#v", draft)
	}
	approver := newDianaGitHubTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "member"}, plugin, settingsFor("acme/demo"))
	assertCreatedInRenamedRepository(t, github, approveWithCode(t, approver, draft.Draft.ID))
}

// 私聊里按用户授权的是旧名，写入要用本人的 Token：跟到新名字后凭据也得认得旧名的授权，
// 否则会掉到公共凭据上（这里没配，直接失败）。
func TestRepositoryIssuePersonalCredentialFollowsRename(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := newRenamedRepositoryServer(github, false)
	defer server.Close()
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	settings := SettingValues{
		repositoryPublishSettingAllowlist:  "acme/old",
		repositoryPublishSettingUserAccess: "member = acme/old",
		repositoryPublishSettingUserTokens: `{"member":"` + repositoryPublishTestToken + `"}`,
		repositoryPublishSettingTimeout:    5,
	}
	tool := newDianaGitHubTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "member", RawMessage: "提个 issue"}, newRepositoryPublishPlugin(server.Client(), server.URL), settings)
	draft := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "create", "repository": "acme/old", "title": "登录失败"})
	if draft.Draft == nil {
		t.Fatalf("draft=%#v", draft)
	}
	assertCreatedInRenamedRepository(t, github, approveWithCode(t, tool, draft.Draft.ID))
}

// 旧名下有一次说不清落没落地的写入时不自动改投：幂等标记带仓库名，换了名字查不到它，
// 跟过去可能写出第二份。
func TestRepositoryIssueRenameNotFollowedAfterUncertainWrite(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := newRenamedRepositoryServer(github, false)
	defer server.Close()
	tool := repositoryPublishTestTool(server, "提个 issue", nil)
	draft := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "create", "repository": "acme/old", "title": "登录失败"})
	if draft.Draft == nil {
		t.Fatalf("draft=%#v", draft)
	}
	tool.plugin.markOperationUncertain("acme/old:create:earlier")
	result := approveWithCode(t, tool, draft.Draft.ID)
	if result.OK || result.FailureCode != "repository_renamed" || result.RedirectRepository != "acme/demo" || !strings.Contains(result.Message, "不确定") {
		t.Fatalf("uncertain approve=%#v", result)
	}
	if github.count(http.MethodPost) != 0 {
		t.Fatalf("wrote despite uncertain earlier write: %#v", github.requests)
	}
}

// 读操作直接换新名字读，结果里说一声改名了。
func TestRepositoryIssueReadFollowsRenamedRepository(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	github.issues = []githubRepositoryIssue{{
		Number: 5, Title: "旧问题", State: "open", HTMLURL: "https://github.com/acme/demo/issues/5", UpdatedAt: time.Now().UTC(),
	}}
	server := newRenamedRepositoryServer(github, false)
	defer server.Close()
	tool := repositoryPublishTestTool(server, "看看 acme/old 的 5 号", nil)
	result := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "get", "repository": "acme/old", "number": 5})
	if !result.OK || result.Issue == nil || result.Issue.Number != 5 || result.RedirectRepository != "acme/demo" || !strings.Contains(result.Message, "acme/demo") {
		t.Fatalf("read on renamed repository=%#v", result)
	}
}

// 后台直接发布草稿走的是同一个 executeDraft，也跟得过去。
func TestRepositoryIssueWebPublishFollowsRenamedRepository(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := newRenamedRepositoryServer(github, true)
	defer server.Close()
	tool := repositoryPublishTestTool(server, "提个 issue", nil)
	draft := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "create", "repository": "acme/old", "title": "登录失败"})
	if draft.Draft == nil {
		t.Fatalf("draft=%#v", draft)
	}
	published, err := tool.plugin.PublishDraftFromWeb(context.Background(), tool.settings, draft.Draft.ID)
	if err != nil || !published.OK || published.Repository != "acme/demo" || published.Issue == nil {
		t.Fatalf("web publish=%#v err=%v", published, err)
	}
	created := tool.plugin.CreateIssueFromWeb(context.Background(), tool.settings, RepositoryIssueCreateInput{Repository: "acme/old", Title: "另一个问题"})
	if !created.OK || created.Repository != "acme/demo" {
		t.Fatalf("web create=%#v", created)
	}
}

// 没改名的仓库照常报原来的失败，不多出一个 redirect_repository。
func TestRepositoryIssueInvalidResponseWithoutRenameStaysUnchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/acme/demo" {
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "acme/demo"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "", nil)
	result := tool.explainRepositoryRename(context.Background(), repositoryIssueResult{Repository: "acme/demo"}.fail("invalid_response", "x"))
	if result.FailureCode != "invalid_response" || result.RedirectRepository != "" {
		t.Fatalf("result=%#v", result)
	}
}
