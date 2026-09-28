// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newRenamedRepositoryServer 模拟 acme/old 改名成了 acme/demo：旧名的 REST 读请求回 301、
// 写请求回 307，都指向按 ID 寻址的 /repositories/7。withGraphQL 为 false 时 GraphQL
// 一律 404，相当于没有 Token 或查询失败。
func newRenamedRepositoryServer(github *repositoryPublishTestGitHub, withGraphQL bool) *httptest.Server {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/graphql":
			if !withGraphQL || r.Header.Get("Authorization") != "Bearer "+repositoryPublishTestToken {
				http.NotFound(w, r)
				return
			}
			var body struct {
				Query string `json:"query"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			repository := map[string]any{"nameWithOwner": "acme/demo"}
			if strings.Contains(body.Query, "issues(") {
				repository["issues"] = map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": []any{}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": repository}})
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

func approveWithCode(t *testing.T, tool *dianaGitHubTool, draftID string) repositoryIssueResult {
	t.Helper()
	tool.event.RawMessage = repositoryIssueConfirmationCode(draftID)
	return runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "approve", "draft_id": draftID})
}

func assertCreatedInRenamedRepository(t *testing.T, github *repositoryPublishTestGitHub, result repositoryIssueResult) {
	t.Helper()
	if !result.OK || result.Outcome != "created" || result.Repository != "acme/demo" || !strings.Contains(result.Message, "已改名为 acme/demo") {
		t.Fatalf("改名后审批应按新名写入：%#v", result)
	}
	if github.count(http.MethodPost) != 1 || github.last(http.MethodPost).Path != "/repos/acme/demo/issues" {
		t.Fatalf("posts=%d last=%q", github.count(http.MethodPost), github.last(http.MethodPost).Path)
	}
}

// 线上那次的后半段：草稿是改名前起的，管理员随后把设置改成了新名字。审批人不是主人，
// 拿旧名检查授权会被拒；新名字过了授权也要算。
func TestRepositoryIssueApproveAcceptsSettingsAlreadyRenamed(t *testing.T) {
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

	// 新旧名字都不在设置里时照样拒绝。
	other := newDianaGitHubTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "member"}, plugin, settingsFor("acme/other"))
	second := runRepositoryPublishToolOnce(t, requester, map[string]any{"operation": "create", "repository": "acme/old", "title": "另一个问题"})
	if denied := approveWithCode(t, other, second.Draft.ID); denied.OK || github.count(http.MethodPost) != 1 {
		t.Fatalf("新旧名都没授权时不该放行：%#v", denied)
	}
}

// 私聊里按用户授权的是旧名，写入要用本人的 Token：跟到新名字后凭据也得认得旧名的
// 授权，否则会掉到公共凭据上（这里没配，直接失败）。
func TestRepositoryIssuePersonalCredentialFollowsRename(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := newRenamedRepositoryServer(github, true)
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

// 同一个工具实例会被并发 Run（消息重投、多人同时打确认码）：改名缓存和凭据来源都是
// 按调用记账的字段，并发读写不能让进程崩掉，也不能记丢。要配合 -race 才稳定暴露。
func TestRepositoryRenameCacheConcurrentUse(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := newRenamedRepositoryServer(github, true)
	defer server.Close()
	tool := repositoryPublishTestTool(server, "提个 issue", nil)
	const workers = 16
	start := make(chan struct{})
	failures := make(chan string, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			renamed, ok := tool.renamedRepository(context.Background(), "acme/old")
			if !ok || renamed != "acme/demo" {
				failures <- "renamed=" + renamed
				return
			}
			if previous := tool.previousName("acme/demo"); previous != "acme/old" {
				failures <- "previous=" + previous
				return
			}
			if message := tool.failureMessage("not_found"); !strings.Contains(message, "本次凭据") {
				failures <- "message=" + message
			}
		}()
	}
	close(start)
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Fatal(failure)
	}
}

// 没有 Token、GraphQL 用不上时，写入撞上旧名的重定向，改用 REST 问出新名字重试一次。
func TestRepositoryIssueRenameDetectedWithoutGraphQL(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := newRenamedRepositoryServer(github, false)
	defer server.Close()
	tool := repositoryPublishTestTool(server, "提个 issue", nil)
	draft := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "create", "repository": "acme/old", "title": "登录失败"})
	if draft.Draft == nil {
		t.Fatalf("draft=%#v", draft)
	}
	assertCreatedInRenamedRepository(t, github, approveWithCode(t, tool, draft.Draft.ID))
	stored, ok, err := tool.plugin.findResolvedDraft(context.Background(), "private:owner", draft.Draft.ID)
	if err != nil || !ok || stored.Repository != "acme/demo" {
		t.Fatalf("草稿记录应更新成新名：%#v ok=%v err=%v", stored, ok, err)
	}
}

// 读操作用旧名时直接换新名字读，结果里说一声改名了。
func TestRepositoryIssueReadFollowsRenamedRepository(t *testing.T) {
	for _, withGraphQL := range []bool{true, false} {
		github := newRepositoryPublishTestGitHub()
		github.issues = []githubRepositoryIssue{{
			Number: 5, Title: "旧问题", State: "open", HTMLURL: "https://github.com/acme/demo/issues/5", UpdatedAt: time.Now().UTC(),
		}}
		server := newRenamedRepositoryServer(github, withGraphQL)
		tool := repositoryPublishTestTool(server, "看看 acme/old 的 5 号", nil)
		result := runRepositoryPublishToolOnce(t, tool, map[string]any{"operation": "get", "repository": "acme/old", "number": 5})
		server.Close()
		if !result.OK || result.Issue == nil || result.Issue.Number != 5 || result.Repository != "acme/demo" || !strings.Contains(result.Message, "已改名为 acme/demo") {
			t.Fatalf("graphql=%v 读改名仓库=%#v", withGraphQL, result)
		}
	}
}
