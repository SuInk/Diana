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
	"sync"
	"testing"
	"time"
)

const repositoryPublishTestToken = "gh" + "p_abcdefghijklmnopqrstuvwxyz123456"

type repositoryPublishTestRequest struct {
	Method  string
	Path    string
	Payload map[string]any
}

type repositoryPublishTestGitHub struct {
	mu sync.Mutex

	requests []repositoryPublishTestRequest
	issues   []githubRepositoryIssue
	comments map[int][]githubIssueComment

	nextIssueNumber int
	createStatus    int
	commentStatus   int
	rateLimit       bool
}

func newRepositoryPublishTestGitHub() *repositoryPublishTestGitHub {
	return &repositoryPublishTestGitHub{
		comments:        map[int][]githubIssueComment{},
		nextIssueNumber: 41,
	}
}

func (s *repositoryPublishTestGitHub) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	payload := map[string]any{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&payload)
	}
	s.requests = append(s.requests, repositoryPublishTestRequest{Method: r.Method, Path: r.URL.RequestURI(), Payload: payload})
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer "+repositoryPublishTestToken {
		http.Error(w, `{"message":"bad token"}`, http.StatusUnauthorized)
		return
	}
	if s.rateLimit {
		w.Header().Set("X-RateLimit-Remaining", "0")
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/repos/acme/demo/issues")
	switch {
	case r.Method == http.MethodGet && path == "":
		_ = json.NewEncoder(w).Encode(s.issues)
	case r.Method == http.MethodPost && path == "":
		s.nextIssueNumber++
		issue := githubRepositoryIssue{
			Number:    s.nextIssueNumber,
			Title:     stringMapValue(payload, "title"),
			Body:      stringMapValue(payload, "body"),
			State:     "open",
			HTMLURL:   fmt.Sprintf("https://github.com/acme/demo/issues/%d", s.nextIssueNumber),
			UpdatedAt: time.Now().UTC(),
		}
		s.issues = append([]githubRepositoryIssue{issue}, s.issues...)
		if s.createStatus != 0 {
			http.Error(w, `{"message":"confirmation interrupted"}`, s.createStatus)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(issue)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/comments"):
		number := repositoryPublishTestPathNumber(strings.TrimSuffix(path, "/comments"))
		_ = json.NewEncoder(w).Encode(s.comments[number])
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/comments"):
		number := repositoryPublishTestPathNumber(strings.TrimSuffix(path, "/comments"))
		comment := githubIssueComment{
			Body:    stringMapValue(payload, "body"),
			HTMLURL: fmt.Sprintf("https://github.com/acme/demo/issues/%d#issuecomment-%d", number, len(s.comments[number])+1),
		}
		s.comments[number] = append(s.comments[number], comment)
		if s.commentStatus != 0 {
			http.Error(w, `{"message":"confirmation interrupted"}`, s.commentStatus)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(comment)
	case r.Method == http.MethodGet && path != "":
		number := repositoryPublishTestPathNumber(path)
		issue, ok := s.issue(number)
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(issue)
	case r.Method == http.MethodPatch && path != "":
		number := repositoryPublishTestPathNumber(path)
		issue, ok := s.issue(number)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if value, ok := payload["title"].(string); ok {
			issue.Title = value
		}
		if value, ok := payload["body"].(string); ok {
			issue.Body = value
		}
		if value, ok := payload["state"].(string); ok {
			issue.State = value
		}
		issue.UpdatedAt = time.Now().UTC()
		s.replaceIssue(issue)
		_ = json.NewEncoder(w).Encode(issue)
	default:
		http.NotFound(w, r)
	}
}

func (s *repositoryPublishTestGitHub) issue(number int) (githubRepositoryIssue, bool) {
	for _, issue := range s.issues {
		if issue.Number == number {
			return issue, true
		}
	}
	return githubRepositoryIssue{}, false
}

func (s *repositoryPublishTestGitHub) replaceIssue(updated githubRepositoryIssue) {
	for index := range s.issues {
		if s.issues[index].Number == updated.Number {
			s.issues[index] = updated
			return
		}
	}
}

func (s *repositoryPublishTestGitHub) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, request := range s.requests {
		if request.Method == method {
			count++
		}
	}
	return count
}

func (s *repositoryPublishTestGitHub) last(method string) repositoryPublishTestRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := len(s.requests) - 1; index >= 0; index-- {
		if s.requests[index].Method == method {
			return s.requests[index]
		}
	}
	return repositoryPublishTestRequest{}
}

func repositoryPublishTestPathNumber(path string) int {
	var number int
	_, _ = fmt.Sscanf(strings.Trim(path, "/"), "%d", &number)
	return number
}

func stringMapValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func repositoryPublishTestTool(server *httptest.Server, rawMessage string, logs *captureAppLogs) *dianaRepositoryIssuesTool {
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if logs != nil {
		runtime.SetAppLogWriter(logs)
	}
	return newDianaRepositoryIssuesTool(
		runtime,
		MessageEvent{Kind: EventKindPrivate, UserID: "owner", RawMessage: rawMessage},
		newRepositoryPublishPlugin(server.Client(), server.URL),
		SettingValues{
			repositoryPublishSettingToken:     repositoryPublishTestToken,
			repositoryPublishSettingAllowlist: "acme/demo",
			repositoryPublishSettingTimeout:   5,
		},
	)
}

func runRepositoryPublishTestTool(t *testing.T, tool *dianaRepositoryIssuesTool, input map[string]any) repositoryIssueResult {
	t.Helper()
	if _, present := input["user_confirmed_write"]; !present && normalizeRepositoryIssueOperation(configToolString(input, "operation"), configToolString(input, "state")) != "search" {
		input["user_confirmed_write"] = true
	}
	raw, err := tool.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var result repositoryIssueResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode result %q: %v", raw, err)
	}
	return result
}

func TestRepositoryPublishDefaultPluginAndSecretSettings(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := manager.Get(repositoryPublishPluginID)
	if !ok || !state.Installed || !state.Enabled || !state.Manifest.BuiltIn || !state.Manifest.Official {
		t.Fatalf("repository publish plugin state=%#v found=%v", state, ok)
	}
	var tokenSpec, allowlistSpec *PluginSettingSpec
	for index := range state.Manifest.Settings {
		spec := &state.Manifest.Settings[index]
		switch spec.Key {
		case repositoryPublishSettingToken:
			tokenSpec = spec
		case repositoryPublishSettingAllowlist:
			allowlistSpec = spec
		}
	}
	if tokenSpec == nil || !tokenSpec.Secret || allowlistSpec == nil || allowlistSpec.Secret {
		t.Fatalf("token spec=%#v allowlist spec=%#v", tokenSpec, allowlistSpec)
	}
	if _, err := manager.UpdateSettings(repositoryPublishPluginID, map[string]any{
		repositoryPublishSettingToken:     repositoryPublishTestToken,
		repositoryPublishSettingAllowlist: "acme/demo",
	}); err != nil {
		t.Fatal(err)
	}
	configured, _ := manager.Get(repositoryPublishPluginID)
	redacted := configured.Redacted()
	if !redacted.SecretsConfigured[repositoryPublishSettingToken] || redacted.Settings[repositoryPublishSettingAllowlist] != "acme/demo" {
		t.Fatalf("redacted settings=%#v secrets=%#v", redacted.Settings, redacted.SecretsConfigured)
	}
	encoded, _ := json.Marshal(redacted)
	if strings.Contains(string(encoded), repositoryPublishTestToken) {
		t.Fatalf("secret leaked from plugin state: %s", encoded)
	}
}

func TestRepositoryIssueGroupDraftRequiresAuthorizedMemberApproval(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tokens, err := json.Marshal(map[string]string{"approver": repositoryPublishTestToken})
	if err != nil {
		t.Fatal(err)
	}
	settings := SettingValues{
		repositoryPublishSettingAllowlist:   "acme/demo",
		repositoryPublishSettingUserAccess:  "approver = acme/demo",
		repositoryPublishSettingGroupAccess: "group-1 = acme/demo",
		repositoryPublishSettingUserTokens:  string(tokens),
		repositoryPublishSettingTimeout:     5,
	}
	requester := newDianaRepositoryIssuesTool(runtime, MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "member", RawMessage: "登录失败，请帮我提 Issue",
	}, plugin, settings)
	draft := runRepositoryPublishTestTool(t, requester, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "登录失败", "body": "重置密码后无法登录。",
	})
	if !draft.OK || !draft.RequiresApproval || draft.Outcome != "draft_pending" || draft.Draft == nil {
		t.Fatalf("draft result=%#v", draft)
	}
	if github.count(http.MethodPost) != 0 {
		t.Fatal("ordinary group member wrote to GitHub before approval")
	}
	listed := runRepositoryPublishTestTool(t, requester, map[string]any{"operation": "list_drafts", "status": "all"})
	if !listed.OK || len(listed.Drafts) != 1 || listed.Drafts[0].RequesterID != "member" || listed.Drafts[0].CreatedAt.IsZero() || listed.Drafts[0].Body != "重置密码后无法登录。" {
		t.Fatalf("listed drafts=%#v", listed)
	}

	unauthorized := newDianaRepositoryIssuesTool(runtime, MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "other", RawMessage: "同意创建",
	}, plugin, settings)
	denied := runRepositoryPublishTestTool(t, unauthorized, map[string]any{"operation": "approve", "draft_id": draft.Draft.ID})
	if denied.FailureCode != "permission_denied" || github.count(http.MethodPost) != 0 {
		t.Fatalf("unauthorized approval=%#v", denied)
	}

	approver := newDianaRepositoryIssuesTool(runtime, MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "approver", RawMessage: "同意创建这个 Issue",
	}, plugin, settings)
	approved := runRepositoryPublishTestTool(t, approver, map[string]any{"operation": "approve", "draft_id": draft.Draft.ID})
	if !approved.OK || approved.Outcome != "created" || approved.Issue == nil {
		t.Fatalf("approved result=%#v", approved)
	}
	if github.count(http.MethodPost) != 1 {
		t.Fatalf("GitHub create count=%d, want 1", github.count(http.MethodPost))
	}
	listed = runRepositoryPublishTestTool(t, requester, map[string]any{"operation": "list_drafts", "status": "all"})
	if len(listed.Drafts) != 1 || listed.Drafts[0].Status != "created" || listed.Drafts[0].IssueNumber != approved.Issue.Number {
		t.Fatalf("resolved draft list=%#v", listed)
	}
	requester.event.RawMessage = "搜索结果排序不对，请帮我提 Issue"
	second := runRepositoryPublishTestTool(t, requester, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "搜索结果排序不对", "body": "希望按更新时间倒序。",
	})
	approver.event.RawMessage = "取消这个草稿"
	cancelled := runRepositoryPublishTestTool(t, approver, map[string]any{"operation": "cancel_draft", "draft_id": second.Draft.ID})
	if !cancelled.OK || cancelled.Outcome != "cancelled" || cancelled.Draft == nil || cancelled.Draft.Status != "cancelled" || github.count(http.MethodPost) != 1 {
		t.Fatalf("cancelled draft=%#v posts=%d", cancelled, github.count(http.MethodPost))
	}
}

func TestRepositoryIssueApprovalRequiresApproverToken(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	settings := SettingValues{
		repositoryPublishSettingAllowlist:   "acme/demo",
		repositoryPublishSettingUserAccess:  "approver = acme/demo",
		repositoryPublishSettingGroupAccess: "group-1 = acme/demo",
		repositoryPublishSettingToken:       repositoryPublishTestToken,
	}
	requester := newDianaRepositoryIssuesTool(runtime, MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "member", RawMessage: "登录失败，请帮我提 Issue",
	}, plugin, settings)
	draft := runRepositoryPublishTestTool(t, requester, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "登录失败",
	})
	approver := newDianaRepositoryIssuesTool(runtime, MessageEvent{
		Kind: EventKindGroup, GroupID: "group-1", UserID: "approver", RawMessage: "同意创建",
	}, plugin, settings)
	result := runRepositoryPublishTestTool(t, approver, map[string]any{"operation": "approve", "draft_id": draft.Draft.ID})
	if result.OK || result.FailureCode != "user_token_required" {
		t.Fatalf("approval without personal token=%#v", result)
	}
	if github.count(http.MethodPost) != 0 {
		t.Fatalf("approval without personal token reached GitHub: %#v", github.requests)
	}
}

func TestRepositoryIssueSearchRequiresAuthorizedUsersOwnToken(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaRepositoryIssuesTool(
		runtime,
		MessageEvent{Kind: EventKindPrivate, UserID: "member", RawMessage: "搜索 acme/demo 的登录问题"},
		newRepositoryPublishPlugin(server.Client(), server.URL),
		SettingValues{
			repositoryPublishSettingAllowlist:  "acme/demo",
			repositoryPublishSettingUserAccess: "member = acme/demo",
			repositoryPublishSettingToken:      repositoryPublishTestToken,
		},
	)
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "search", "repository": "acme/demo", "query": "login",
	})
	if result.OK || result.FailureCode != "user_token_required" {
		t.Fatalf("search without personal token=%#v", result)
	}
	if github.count(http.MethodGet) != 0 {
		t.Fatalf("search without personal token reached GitHub: %#v", github.requests)
	}
}

func TestRepositoryIssueAuthorizedUserCanInheritGlobalToken(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := newDianaRepositoryIssuesTool(
		NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil),
		MessageEvent{Kind: EventKindPrivate, UserID: "member", RawMessage: "搜索 acme/demo"},
		newRepositoryPublishPlugin(server.Client(), server.URL),
		SettingValues{
			repositoryPublishSettingAllowlist:  "acme/demo",
			repositoryPublishSettingUserAccess: "member = acme/demo",
			repositoryPublishSettingToken:      repositoryPublishTestToken,
			repositoryPublishSettingUserAuth:   `{"member":"inherit"}`,
		},
	)
	result := runRepositoryPublishTestTool(t, tool, map[string]any{"operation": "search", "repository": "acme/demo", "query": "login"})
	if result.FailureCode == "user_token_required" || github.count(http.MethodGet) == 0 {
		t.Fatalf("inherited token result=%#v requests=%#v", result, github.requests)
	}
}

func TestRepositoryIssueAuthorizedUserCanSelectGH(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	plugin.ghAuthToken = func(context.Context) (string, error) { return repositoryPublishTestToken, nil }
	tool := newDianaRepositoryIssuesTool(
		NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil),
		MessageEvent{Kind: EventKindPrivate, UserID: "member", RawMessage: "搜索 acme/demo"},
		plugin,
		SettingValues{
			repositoryPublishSettingAllowlist:  "acme/demo",
			repositoryPublishSettingUserAccess: "member = acme/demo",
			repositoryPublishSettingUserAuth:   `{"member":"gh"}`,
		},
	)
	result := runRepositoryPublishTestTool(t, tool, map[string]any{"operation": "search", "repository": "acme/demo", "query": "login"})
	if result.FailureCode == "user_token_required" || github.count(http.MethodGet) == 0 {
		t.Fatalf("gh user result=%#v requests=%#v", result, github.requests)
	}
}

func TestRepositoryPublishUserAuthModesRejectInvalidMode(t *testing.T) {
	if _, err := repositoryPublishUserAuthModes(`{"member":"unknown"}`); err == nil {
		t.Fatal("invalid user auth mode accepted")
	}
}

func TestRepositoryIssueUserTokenCannotBypassGlobalAllowlist(t *testing.T) {
	settings := SettingValues{
		repositoryPublishSettingAllowlist:  "acme/other",
		repositoryPublishSettingUserAccess: "member = acme/demo",
		repositoryPublishSettingUserTokens: `{"member":"member-token"}`,
	}
	event := MessageEvent{Kind: EventKindPrivate, UserID: "member"}
	if _, _, code, _ := repositoryPublishAccessForEvent(event, "acme/demo", false, settings); code != "repository_not_allowed" {
		t.Fatalf("access code=%q, want repository_not_allowed", code)
	}
}

func TestRepositoryPublishMergesPerUserTokenUpdates(t *testing.T) {
	manager := NewDefaultPluginManager()
	first := `{"user-a":"token-a","user-b":"token-b"}`
	if _, err := manager.UpdateSettings(repositoryPublishPluginID, map[string]any{repositoryPublishSettingUserTokens: first}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateSettings(repositoryPublishPluginID, map[string]any{repositoryPublishSettingUserTokens: `{"user-a":"token-a2","user-b":null}`}); err != nil {
		t.Fatal(err)
	}
	state, _ := manager.Get(repositoryPublishPluginID)
	tokens, err := repositoryPublishUserTokens(state.Settings[repositoryPublishSettingUserTokens].(string))
	if err != nil || tokens["user-a"] != "token-a2" || tokens["user-b"] != "" || len(tokens) != 1 {
		t.Fatalf("merged tokens=%#v err=%v", tokens, err)
	}
}

func TestRepositoryIssueCreateSanitizesAndSendsOptionalFields(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	logs := &captureAppLogs{}
	secretTitle := "Login failure " + repositoryPublishTestToken
	secretBody := "contact dev@example.com Authorization: bearer-secret " + repositoryPublishTestToken
	tool := repositoryPublishTestTool(server, "请在 acme/demo 创建 GitHub Issue，标题为 "+secretTitle+"，正文为 "+secretBody+"，加 bug 和 urgent 标签，指派给 octocat，并设置里程碑 7", logs)
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "create", "repository": "acme/demo", "operation_id": "create-optional",
		"title": secretTitle,
		"body":  secretBody, "labels": []string{"bug", "urgent"}, "assignees": []string{"octocat"}, "milestone": 7,
	})
	if !result.OK || result.Outcome != "created" || result.Issue == nil || result.Issue.Number != 42 || result.Issue.URL != "https://github.com/acme/demo/issues/42" || result.Redactions < 3 {
		t.Fatalf("result=%#v", result)
	}
	request := github.last(http.MethodPost)
	if request.Path != "/repos/acme/demo/issues" {
		t.Fatalf("create path=%q", request.Path)
	}
	encoded, _ := json.Marshal(request.Payload)
	if strings.Contains(string(encoded), repositoryPublishTestToken) || strings.Contains(string(encoded), "dev@example.com") || strings.Contains(string(encoded), "bearer-secret") {
		t.Fatalf("create payload leaked sensitive input: %s", encoded)
	}
	if labels, ok := request.Payload["labels"].([]any); !ok || len(labels) != 2 || request.Payload["milestone"] != float64(7) {
		t.Fatalf("optional create fields=%#v", request.Payload)
	}
	if assignees, ok := request.Payload["assignees"].([]any); !ok || len(assignees) != 1 {
		t.Fatalf("assignees=%#v", request.Payload["assignees"])
	}
	entries := logs.entriesSnapshot()
	if len(entries) != 1 || entries[0].Action != "qqbot.repository_issue" {
		t.Fatalf("audit entries=%#v", entries)
	}
	audit, _ := json.Marshal(entries[0])
	if strings.Contains(string(audit), secretBody) || strings.Contains(string(audit), repositoryPublishTestToken) {
		t.Fatalf("audit leaked issue body or token: %s", audit)
	}
}

func TestRepositoryIssueWriteRequiresTokenAndExactAllowlist(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "请在 acme/demo 创建 GitHub Issue，标题为 Exact allowlist", nil)
	input := map[string]any{"operation": "create", "repository": "acme/demo", "title": "Exact allowlist"}

	tool.settings[repositoryPublishSettingToken] = ""
	if result := runRepositoryPublishTestTool(t, tool, input); result.FailureCode != "token_required" {
		t.Fatalf("missing token result=%#v", result)
	}
	tool.settings[repositoryPublishSettingToken] = repositoryPublishTestToken
	tool.settings[repositoryPublishSettingAllowlist] = "acme/demo-extra"
	if result := runRepositoryPublishTestTool(t, tool, input); result.FailureCode != "repository_not_allowed" {
		t.Fatalf("similar allowlist result=%#v", result)
	}
	if github.count(http.MethodGet)+github.count(http.MethodPost) != 0 {
		t.Fatalf("rejected writes reached GitHub: %#v", github.requests)
	}
}

func TestRepositoryIssueCreateBlocksSimilarTitle(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	github.issues = []githubRepositoryIssue{{
		Number: 12, Title: "Login fails after password reset", State: "open",
		HTMLURL: "https://github.com/acme/demo/issues/12", UpdatedAt: time.Now().UTC(),
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "请在 acme/demo 创建 GitHub Issue，标题为 Login fails after password reset", nil)
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "Login fails after password reset",
	})
	if result.OK || result.FailureCode != "duplicate_candidate" || !result.RequiresConfirmation || len(result.Items) != 1 || result.Items[0].Number != 12 {
		t.Fatalf("result=%#v", result)
	}
	if github.count(http.MethodPost) != 0 {
		t.Fatalf("duplicate candidate caused POST: %#v", github.requests)
	}
}

func TestRepositoryIssueCreateFingerprintRetryIsIdempotent(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "请在 acme/demo 创建 GitHub Issue，标题为 New issue，正文为 Details", nil)
	input := map[string]any{
		"operation": "create", "repository": "acme/demo", "operation_id": "stable-create", "title": "New issue", "body": "Details",
	}
	first := runRepositoryPublishTestTool(t, tool, input)
	second := runRepositoryPublishTestTool(t, tool, input)
	if !first.OK || first.Outcome != "created" || !second.OK || second.Outcome != "reused" || !second.Idempotent || first.Issue.Number != second.Issue.Number {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if github.count(http.MethodPost) != 1 {
		t.Fatalf("same fingerprint issued %d POSTs", github.count(http.MethodPost))
	}
}

func TestRepositoryIssueCreateUncertainPOSTReconcilesReadOnly(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	github.createStatus = http.StatusInternalServerError
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "请在 acme/demo 创建 GitHub Issue，标题为 Create once", nil)
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "create", "repository": "acme/demo", "operation_id": "uncertain-create", "title": "Create once",
	})
	if !result.OK || result.Outcome != "reconciled" || !result.Reconciled || !result.Idempotent || result.Issue == nil || result.Issue.Number != 42 {
		t.Fatalf("result=%#v", result)
	}
	if github.count(http.MethodPost) != 1 || github.count(http.MethodGet) != 2 {
		t.Fatalf("uncertain create calls=%#v", github.requests)
	}
	if last := github.requests[len(github.requests)-1]; last.Method != http.MethodGet {
		t.Fatalf("reconciliation was not read-only: %#v", github.requests)
	}
}

func TestRepositoryIssueCommentFingerprintRetryIsIdempotent(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	github.issues = []githubRepositoryIssue{{
		Number: 9, Title: "Tracked issue", State: "open", HTMLURL: "https://github.com/acme/demo/issues/9", UpdatedAt: time.Now().UTC(),
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "请评论 acme/demo 的 GitHub Issue #9：Fixed in main", nil)
	input := map[string]any{
		"operation": "comment", "repository": "acme/demo", "number": 9, "operation_id": "stable-comment", "body": "Fixed in main",
	}
	first := runRepositoryPublishTestTool(t, tool, input)
	second := runRepositoryPublishTestTool(t, tool, input)
	if !first.OK || first.Outcome != "commented" || !second.OK || second.Outcome != "reused" || !second.Idempotent || first.CommentURL != second.CommentURL {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if github.count(http.MethodPost) != 1 {
		t.Fatalf("same comment fingerprint issued %d POSTs", github.count(http.MethodPost))
	}
}

func TestRepositoryIssueUpdatePreservesCreateMarker(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	marker := repositoryIssueOperationMarker("create", fingerprint)
	github := newRepositoryPublishTestGitHub()
	github.issues = []githubRepositoryIssue{{
		Number: 5, Title: "Old", Body: "old body\n\n" + marker, State: "open",
		HTMLURL: "https://github.com/acme/demo/issues/5", UpdatedAt: time.Now().UTC(),
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "请修改 acme/demo 的 GitHub Issue #5 正文内容为 replacement body", nil)
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "update", "repository": "acme/demo", "number": 5, "body": "replacement body",
	})
	if !result.OK || result.Outcome != "updated" {
		t.Fatalf("result=%#v", result)
	}
	request := github.last(http.MethodPatch)
	body := stringMapValue(request.Payload, "body")
	if !strings.Contains(body, "replacement body") || !strings.Contains(body, marker) {
		t.Fatalf("update body lost create marker: %q", body)
	}
}

func TestRepositoryIssueCloseReopenRequireMatchingExplicitRequest(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	github.issues = []githubRepositoryIssue{{
		Number: 3, Title: "Lifecycle", State: "open", HTMLURL: "https://github.com/acme/demo/issues/3", UpdatedAt: time.Now().UTC(),
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	closeTool := repositoryPublishTestTool(server, "请关闭 acme/demo 的 GitHub Issue #3", nil)
	if result := runRepositoryPublishTestTool(t, closeTool, map[string]any{"operation": "reopen", "repository": "acme/demo", "number": 3, "user_confirmed_write": false}); result.FailureCode != "explicit_request_required" {
		t.Fatalf("reopen under close request result=%#v", result)
	}
	closed := runRepositoryPublishTestTool(t, closeTool, map[string]any{"operation": "close", "repository": "acme/demo", "number": 3})
	if !closed.OK || closed.Outcome != "closed" || closed.Issue.State != "closed" {
		t.Fatalf("closed=%#v", closed)
	}
	reopenTool := repositoryPublishTestTool(server, "请重新打开 acme/demo 的 GitHub Issue #3", nil)
	reopened := runRepositoryPublishTestTool(t, reopenTool, map[string]any{"operation": "reopen", "repository": "acme/demo", "number": 3})
	if !reopened.OK || reopened.Outcome != "reopened" || reopened.Issue.State != "open" {
		t.Fatalf("reopened=%#v", reopened)
	}
}

func TestRepositoryIssueRateLimitReturnsStructuredFailureCode(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	github.rateLimit = true
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "请在 acme/demo 创建 GitHub Issue，标题为 Rate limited", nil)
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "Rate limited",
	})
	if result.OK || result.Outcome != "failed" || result.FailureCode != "rate_limited" {
		t.Fatalf("result=%#v", result)
	}
}

func TestRepositoryIssueRejectsNonOwnerBeforeGitHub(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaRepositoryIssuesTool(
		runtime,
		MessageEvent{Kind: EventKindPrivate, UserID: "member", RawMessage: "请创建 GitHub Issue"},
		newRepositoryPublishPlugin(server.Client(), server.URL),
		SettingValues{repositoryPublishSettingToken: repositoryPublishTestToken, repositoryPublishSettingAllowlist: "acme/demo"},
	)
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "Forbidden",
	})
	if result.OK || result.FailureCode != "permission_denied" {
		t.Fatalf("result=%#v", result)
	}
	if github.count(http.MethodGet)+github.count(http.MethodPost) != 0 {
		t.Fatalf("non-owner request reached GitHub: %#v", github.requests)
	}
}

func TestRepositoryIssueAllowsConfiguredUserOnlyForMappedRepository(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	settings := SettingValues{
		repositoryPublishSettingToken:      repositoryPublishTestToken,
		repositoryPublishSettingAllowlist:  "acme/demo,acme/other",
		repositoryPublishSettingUserAccess: "member = acme/demo\nother = acme/other",
		repositoryPublishSettingUserTokens: `{"member":"` + repositoryPublishTestToken + `"}`,
	}
	tool := newDianaRepositoryIssuesTool(
		runtime,
		MessageEvent{Kind: EventKindPrivate, UserID: "member", RawMessage: "请在 acme/demo 创建 GitHub Issue，标题为 Delegated write"},
		newRepositoryPublishPlugin(server.Client(), server.URL),
		settings,
	)
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "Delegated write",
	})
	if !result.OK || result.Outcome != "created" {
		t.Fatalf("delegated result=%#v", result)
	}

	tool.event.RawMessage = "请在 acme/other 创建 GitHub Issue，标题为 Forbidden delegated write"
	result = runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "create", "repository": "acme/other", "title": "Forbidden delegated write",
	})
	if result.OK || result.FailureCode != "permission_denied" {
		t.Fatalf("cross-repository result=%#v", result)
	}
	if github.count(http.MethodPost) != 1 {
		t.Fatalf("unauthorized delegated request reached GitHub: %#v", github.requests)
	}
}

func TestRepositoryIssueDelegatedAccessAlsoRequiresGlobalAllowlist(t *testing.T) {
	settings := SettingValues{
		repositoryPublishSettingAllowlist:  "acme/other",
		repositoryPublishSettingUserAccess: "member = acme/demo",
	}
	if code, _ := repositoryPublishValidateEventAccess(MessageEvent{Kind: EventKindPrivate, UserID: "member"}, "acme/demo", false, settings); code != "repository_not_allowed" {
		t.Fatalf("access code=%q, want repository_not_allowed", code)
	}
	if !repositoryPublishUserHasAccess("member", settings) || repositoryPublishUserHasAccess("stranger", settings) {
		t.Fatalf("unexpected mapped access")
	}
}

func TestRepositoryPublishUserAccessRejectsMalformedRules(t *testing.T) {
	for _, raw := range []string{"member", "= acme/demo", "member =", "member = acme/demo="} {
		if _, err := repositoryPublishUserAccess(raw); err == nil {
			t.Fatalf("rule %q accepted", raw)
		}
	}
}

func TestRepositoryIssueMappedGroupOnlyDraftsForMappedRepository(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	settings := SettingValues{
		repositoryPublishSettingToken:       repositoryPublishTestToken,
		repositoryPublishSettingAllowlist:   "acme/demo,acme/other",
		repositoryPublishSettingGroupAccess: "group-1 = acme/demo",
	}
	tool := newDianaRepositoryIssuesTool(
		runtime,
		MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "member", RawMessage: "请在 acme/demo 创建 GitHub Issue，标题为 Group write"},
		newRepositoryPublishPlugin(server.Client(), server.URL),
		settings,
	)
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "Group write",
	})
	if !result.OK || result.Outcome != "draft_pending" || !result.RequiresApproval {
		t.Fatalf("group result=%#v", result)
	}

	tool.event.RawMessage = "请在 acme/other 创建 GitHub Issue，标题为 Cross repository"
	result = runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "create", "repository": "acme/other", "title": "Cross repository",
	})
	if result.OK || result.FailureCode != "permission_denied" {
		t.Fatalf("cross-repository result=%#v", result)
	}

	tool.event = MessageEvent{Kind: EventKindPrivate, UserID: "member", RawMessage: "请在 acme/demo 创建 GitHub Issue，标题为 Private write"}
	result = runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "Private write",
	})
	if result.OK || result.FailureCode != "permission_denied" {
		t.Fatalf("private result=%#v", result)
	}
	if github.count(http.MethodPost) != 0 {
		t.Fatalf("unauthorized group requests reached GitHub: %#v", github.requests)
	}
}

func TestRepositoryPublishGroupAccessRejectsMalformedRules(t *testing.T) {
	for _, raw := range []string{"group-1", "= acme/demo", "group-1 =", "group-1 = acme/demo="} {
		if _, err := repositoryPublishGroupAccess(raw); err == nil {
			t.Fatalf("rule %q accepted", raw)
		}
	}
}

func TestRepositoryPublishEventHasAccessUsesCurrentGroup(t *testing.T) {
	settings := SettingValues{repositoryPublishSettingGroupAccess: "group-1 = acme/demo"}
	if !repositoryPublishEventHasAccess(MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "member"}, settings) {
		t.Fatal("mapped group did not receive Issue tool access")
	}
	if repositoryPublishEventHasAccess(MessageEvent{Kind: EventKindGroup, GroupID: "group-2", UserID: "member"}, settings) {
		t.Fatal("unmapped group received Issue tool access")
	}
	if repositoryPublishEventHasAccess(MessageEvent{Kind: EventKindPrivate, UserID: "member"}, settings) {
		t.Fatal("private chat inherited group Issue tool access")
	}
}

func TestRepositoryIssueGroupAccessAlsoRequiresGlobalAllowlist(t *testing.T) {
	settings := SettingValues{
		repositoryPublishSettingAllowlist:   "acme/other",
		repositoryPublishSettingGroupAccess: "group-1 = acme/demo",
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "member"}
	if code, _ := repositoryPublishValidateEventAccess(event, "acme/demo", false, settings); code != "repository_not_allowed" {
		t.Fatalf("access code=%q, want repository_not_allowed", code)
	}
}

func TestRepositoryIssueGHAuthenticationModeUsesCLIcredential(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "请在 acme/demo 创建 GitHub Issue，标题为 GH auth", nil)
	tool.settings[repositoryPublishSettingAuthMode] = repositoryPublishAuthGH
	tool.settings[repositoryPublishSettingToken] = "wrong-token"
	calls := 0
	tool.plugin.ghAuthToken = func(context.Context) (string, error) {
		calls++
		return repositoryPublishTestToken, nil
	}
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "GH auth",
	})
	if !result.OK || calls == 0 {
		t.Fatalf("result=%#v gh calls=%d", result, calls)
	}
}

func TestRepositoryIssueAutoAuthenticationPrefersConfiguredToken(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryPublishTestTool(server, "请在 acme/demo 创建 GitHub Issue，标题为 Auto auth", nil)
	tool.settings[repositoryPublishSettingAuthMode] = repositoryPublishAuthAuto
	tool.plugin.ghAuthToken = func(context.Context) (string, error) {
		t.Fatal("gh credential called despite configured token")
		return "", nil
	}
	result := runRepositoryPublishTestTool(t, tool, map[string]any{
		"operation": "create", "repository": "acme/demo", "title": "Auto auth",
	})
	if !result.OK {
		t.Fatalf("result=%#v", result)
	}
}

func TestRepositoryPublishGHEnvironmentIgnoresTokenOverrides(t *testing.T) {
	filtered := repositoryPublishGHEnvironment([]string{"PATH=/usr/bin", "GH_TOKEN=wrong", "github_token=wrong-too", "OTHER=value"})
	if strings.Join(filtered, ",") != "PATH=/usr/bin,OTHER=value" {
		t.Fatalf("filtered environment=%#v", filtered)
	}
}

// 用户常常只说仓库简称，模型得先知道当前会话能碰哪些仓库才不会反问一句 owner/repo。
func TestRepositoryPublishEventRepositoriesListsOnlyGrantedRepositories(t *testing.T) {
	settings := SettingValues{
		repositoryPublishSettingAllowlist:    "MilkSU-Official/milksu, SuInk/Diana\nacme/private",
		repositoryPublishSettingManagerUsers: "owner-user = SuInk/Diana",
		repositoryPublishSettingDraftGroups:  "10497 = MilkSU-Official/milksu",
	}
	group := MessageEvent{Kind: EventKindGroup, GroupID: "10497", UserID: "someone"}

	// 群里被授权草稿的那一个仓库，白名单里的其他仓库不该露出来。
	if got := repositoryPublishEventRepositories(group, false, settings); len(got) != 1 || got[0] != "MilkSU-Official/milksu" {
		t.Fatalf("group repositories = %#v", got)
	}
	// 大小写按配置原样保留，方便直接填进 repository。
	direct := MessageEvent{Kind: EventKindPrivate, UserID: "owner-user"}
	if got := repositoryPublishEventRepositories(direct, false, settings); len(got) != 1 || got[0] != "SuInk/Diana" {
		t.Fatalf("user repositories = %#v", got)
	}
	// 主人看到整份白名单。
	if got := repositoryPublishEventRepositories(direct, true, settings); len(got) != 3 {
		t.Fatalf("owner repositories = %#v", got)
	}
	// 没有任何授权时返回空，描述里会据此说明尚未授权。
	stranger := MessageEvent{Kind: EventKindPrivate, UserID: "nobody"}
	if got := repositoryPublishEventRepositories(stranger, false, settings); len(got) != 0 {
		t.Fatalf("stranger repositories = %#v", got)
	}
}

// 白名单没配时不能凭空给出仓库。
func TestRepositoryPublishEventRepositoriesEmptyWithoutAllowlist(t *testing.T) {
	settings := SettingValues{repositoryPublishSettingManagerUsers: "owner-user = SuInk/Diana"}
	direct := MessageEvent{Kind: EventKindPrivate, UserID: "owner-user"}
	if got := repositoryPublishEventRepositories(direct, false, settings); len(got) != 0 {
		t.Fatalf("repositories without an allowlist = %#v", got)
	}
	if got := repositoryPublishEventRepositories(direct, true, settings); len(got) != 0 {
		t.Fatalf("owner repositories without an allowlist = %#v", got)
	}
}

// 「群友甲是管理员，但整个群没放开」：他在群里能建能批，也必须能列出草稿。
func TestRepositoryPublishUserScopedManagerWorksInsideGroups(t *testing.T) {
	settings := SettingValues{
		repositoryPublishSettingAllowlist:    "SuInk/Diana",
		repositoryPublishSettingManagerUsers: "manager-user = SuInk/Diana",
	}
	group := MessageEvent{Kind: EventKindGroup, GroupID: "10497", UserID: "manager-user"}

	// 群里未授权，但这个人是按用户授权的管理员，直接写入应当放行。
	direct, _, code, message := repositoryPublishAccessForEvent(group, "SuInk/Diana", false, settings)
	if code != "" || !direct {
		t.Fatalf("user-scoped manager denied inside a group: direct=%v code=%q message=%q", direct, code, message)
	}
	// 同一个人也应当出现在可操作仓库清单里。
	if got := repositoryPublishEventRepositories(group, false, settings); len(got) != 1 || got[0] != "SuInk/Diana" {
		t.Fatalf("group repositories for a user-scoped manager = %#v", got)
	}
	// 同群里没有任何授权的其他人仍然被拒。
	stranger := MessageEvent{Kind: EventKindGroup, GroupID: "10497", UserID: "someone-else"}
	if _, _, code, _ := repositoryPublishAccessForEvent(stranger, "SuInk/Diana", false, settings); code == "" {
		t.Fatalf("unauthorized group member should stay denied")
	}
}

// 「GitHub 仓库 · 设置」把两个插件呈现成同一个公共 Token，前端却只在本次重新输入
// 时才把它镜像给发布插件。发布插件这边为空时必须回落到订阅插件，否则界面显示「已
// 配置」而 Issue 仍然用不了。
func TestRepositoryPublishCredentialFallsBackToWatchToken(t *testing.T) {
	manager := NewPluginManager(NewRepositoryWatchPlugin(nil))
	if _, err := manager.UpdateSettings(repositoryWatchPluginID, map[string]any{repositoryWatchSettingToken: "watch-token"}); err != nil {
		t.Fatalf("seed watch token: %v", err)
	}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, manager, nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindPrivate, UserID: "owner"}

	// 发布插件自己的 Token 为空：回落到订阅插件那份。
	tool := newDianaRepositoryIssuesTool(runtime, event, &RepositoryPublishPlugin{}, SettingValues{})
	token, apiErr := tool.repositoryPublishCredential(context.Background())
	if apiErr != nil || token != "watch-token" {
		t.Fatalf("token=%q err=%#v", token, apiErr)
	}
	if !strings.Contains(tool.credentialSource, "仓库订阅") {
		t.Fatalf("credential source should name the fallback: %q", tool.credentialSource)
	}
	// 回落能取到凭据时，写入预检查不能抢先报 token_required。
	gated := newDianaRepositoryIssuesTool(runtime, event, &RepositoryPublishPlugin{},
		SettingValues{repositoryPublishSettingAllowlist: "acme/demo"})
	if code, message := gated.validateWriteAccess("acme/demo", true); code != "" {
		t.Fatalf("precheck rejected a usable fallback token: %s %s", code, message)
	}

	// 发布插件自己配了就用自己的，不被订阅插件覆盖。
	own := newDianaRepositoryIssuesTool(runtime, event, &RepositoryPublishPlugin{}, SettingValues{repositoryPublishSettingToken: "publish-token"})
	token, apiErr = own.repositoryPublishCredential(context.Background())
	if apiErr != nil || token != "publish-token" {
		t.Fatalf("own token=%q err=%#v", token, apiErr)
	}
	if strings.Contains(own.credentialSource, "仓库订阅") {
		t.Fatalf("own token should not be reported as the fallback: %q", own.credentialSource)
	}
}

// 两边都没有 Token 时仍要明确要求配置，而不是发一个匿名请求出去。
func TestRepositoryPublishCredentialRequiresATokenWhenBothAreEmpty(t *testing.T) {
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaRepositoryIssuesTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "owner"}, &RepositoryPublishPlugin{}, SettingValues{})
	if _, apiErr := tool.repositoryPublishCredential(context.Background()); apiErr == nil || apiErr.Code != "token_required" {
		t.Fatalf("expected token_required, got %#v", apiErr)
	}
}

// 404 要带上凭据来源，否则「配了 Token 却 404」无从下手。
func TestRepositoryIssueFailureMessageNamesTheCredential(t *testing.T) {
	tool := &dianaRepositoryIssuesTool{credentialSource: "公共 GitHub Token（来自仓库订阅插件）"}
	message := tool.failureMessage("not_found")
	if !strings.Contains(message, "404") || !strings.Contains(message, "来自仓库订阅插件") {
		t.Fatalf("not_found message = %q", message)
	}
	// 与凭据无关的报错不该被硬塞一句来源。
	if got := tool.failureMessage("rate_limited"); strings.Contains(got, "本次凭据") {
		t.Fatalf("rate_limited message should stay clean: %q", got)
	}
	// 没记录来源时不留下空括号。
	empty := &dianaRepositoryIssuesTool{}
	if got := empty.failureMessage("not_found"); strings.Contains(got, "本次凭据") {
		t.Fatalf("message should omit an unknown source: %q", got)
	}
}
