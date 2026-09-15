// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// graphQLRepoServer 同时模拟 GitHub 的 GraphQL 和 REST：GraphQL 只返回 issue；REST 的
// issues 接口只返回 PR（模拟 PR 远多于 issue 的仓库），用来确认有 Token 时不再走 REST。
type graphQLRepoServer struct {
	mu            sync.Mutex
	issuePages    [][]map[string]any
	graphQLVars   []map[string]any
	restIssueHits int
	failGraphQL   bool
	pullQueries   []string
	events        [][]map[string]any
	eventPages    []string
}

func (s *graphQLRepoServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/graphql":
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, `{"message":"auth"}`, http.StatusUnauthorized)
			return
		}
		if s.failGraphQL {
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "boom"}}})
			return
		}
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.graphQLVars = append(s.graphQLVars, body.Variables)
		page := 0
		if after, _ := body.Variables["after"].(string); after != "" {
			page, _ = strconv.Atoi(strings.TrimPrefix(after, "cursor-"))
		}
		nodes := []map[string]any{}
		if page < len(s.issuePages) {
			nodes = s.issuePages[page]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"issues": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": page+1 < len(s.issuePages), "endCursor": fmt.Sprintf("cursor-%d", page+1)},
			"nodes":    nodes,
		}}}})
	case r.URL.Path == "/repos/acme/demo/issues":
		s.restIssueHits++
		items := make([]map[string]any, 0, 100)
		for i := 0; i < 100; i++ {
			items = append(items, map[string]any{"number": 1000 + i, "updated_at": time.Now().UTC().Format(time.RFC3339), "created_at": "2026-01-01T00:00:00Z", "pull_request": map[string]any{"url": "x"}})
		}
		_ = json.NewEncoder(w).Encode(items)
	case r.URL.Path == "/repos/acme/demo/pulls":
		s.pullQueries = append(s.pullQueries, r.URL.RawQuery)
		_ = json.NewEncoder(w).Encode([]any{})
	case r.URL.Path == "/repos/acme/demo/events":
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		s.eventPages = append(s.eventPages, r.URL.Query().Get("page"))
		if page < 1 || page > len(s.events) {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		_ = json.NewEncoder(w).Encode(s.events[page-1])
	case r.URL.Path == "/repos/acme/demo":
		_ = json.NewEncoder(w).Encode(map[string]any{"stargazers_count": 8, "html_url": "https://github.com/acme/demo"})
	default:
		http.NotFound(w, r)
	}
}

func graphQLIssueNode(number int, state string, updatedAt time.Time, reopenedAt *time.Time) map[string]any {
	timeline := []any{}
	if reopenedAt != nil {
		timeline = append(timeline, map[string]any{"createdAt": reopenedAt.Format(time.RFC3339)})
	}
	return map[string]any{
		"number": number, "title": fmt.Sprintf("issue %d", number), "body": "", "state": state, "stateReason": map[bool]string{true: "REOPENED", false: ""}[reopenedAt != nil],
		"url": fmt.Sprintf("https://github.com/acme/demo/issues/%d", number), "createdAt": "2026-01-01T00:00:00Z",
		"updatedAt": updatedAt.Format(time.RFC3339), "author": map[string]any{"login": "alice"},
		"timelineItems": map[string]any{"nodes": timeline},
	}
}

func newGraphQLRepoPlugin(t *testing.T, server *graphQLRepoServer, now time.Time) *RepositoryWatchPlugin {
	t.Helper()
	httpServer := httptest.NewServer(http.HandlerFunc(server.handler))
	t.Cleanup(httpServer.Close)
	plugin := newRepositoryWatchPlugin(httpServer.Client(), httpServer.URL)
	plugin.now = func() time.Time { return now }
	return plugin
}

// 有 Token 时 issue 直接用 GraphQL 查：PR 再多也不影响；有游标时带 since 并翻完；
// 重新打开的时间从同一次查询里取，不再读事件流。
func TestRepositoryWatchIssuesUseGraphQLWhenTokenAvailable(t *testing.T) {
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	cursorAt := now.Add(-10 * time.Minute)
	reopened := now.Add(-2 * time.Minute)
	server := &graphQLRepoServer{issuePages: [][]map[string]any{
		{graphQLIssueNode(12, "OPEN", now.Add(-time.Minute), &reopened)},
		{graphQLIssueNode(11, "CLOSED", now.Add(-5*time.Minute), nil)},
	}}
	plugin := newGraphQLRepoPlugin(t, server, now)
	settings := SettingValues{repositoryWatchSettingToken: "test-token", repositoryWatchSettingLimit: 5}
	found, next, err := plugin.fetchIssues(context.Background(), "acme/demo", repositoryWatchPullCursor(cursorAt, 3), time.Time{}, repositoryWatchSelection{Issues: true}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if server.restIssueHits != 0 {
		t.Fatalf("有 Token 时不该再请求 REST issues：%d 次", server.restIssueHits)
	}
	if len(server.graphQLVars) != 2 || server.graphQLVars[0]["since"] != cursorAt.Format(time.RFC3339) {
		t.Fatalf("应带 since 并翻完两页：%#v", server.graphQLVars)
	}
	if len(found) != 2 || found[0].Number != 12 || found[0].Status != "reopened" || !found[0].ReopenedAt.Equal(reopened) || found[1].Status != "closed" {
		t.Fatalf("found=%#v", found)
	}
	if next != repositoryWatchPullCursor(now.Add(-time.Minute), 12) {
		t.Fatalf("next=%q", next)
	}
	for _, page := range server.eventPages {
		t.Fatalf("GraphQL 已带回重开时间，不该再读事件流：%v", page)
	}
}

// GraphQL 出错时退回 REST，不中断订阅。
func TestRepositoryWatchIssuesFallBackToRESTWhenGraphQLFails(t *testing.T) {
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	server := &graphQLRepoServer{failGraphQL: true}
	plugin := newGraphQLRepoPlugin(t, server, now)
	_, _, err := plugin.fetchIssues(context.Background(), "acme/demo", "", time.Time{}, repositoryWatchSelection{Issues: true}, SettingValues{repositoryWatchSettingToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	if server.restIssueHits == 0 {
		t.Fatal("GraphQL 失败后应退回 REST")
	}
}

// PR 的分支过滤交给服务端。
func TestRepositoryWatchPullRequestsFilterBranchOnServer(t *testing.T) {
	server := &graphQLRepoServer{}
	plugin := newGraphQLRepoPlugin(t, server, time.Now())
	if _, _, err := plugin.fetchPullRequests(context.Background(), "acme/demo", "main", "", time.Time{}, repositoryWatchSelection{PullRequests: true}, SettingValues{}); err != nil {
		t.Fatal(err)
	}
	if len(server.pullQueries) != 1 || !strings.Contains(server.pullQueries[0], "base=main") {
		t.Fatalf("请求应带 base=main：%#v", server.pullQueries)
	}
}

// 两次检查之间动态超过一页：往后翻到上次处理过的事件为止，中间的新 star 不漏。
func TestRepositoryWatchStarEventsPageUntilLastSeenEvent(t *testing.T) {
	event := func(id, kind, login string, at time.Time) map[string]any {
		return map[string]any{"id": id, "type": kind, "created_at": at.Format(time.RFC3339), "actor": map[string]any{"login": login}, "payload": map[string]any{"action": "started"}}
	}
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	page1 := make([]map[string]any, 0, 100)
	for i := 0; i < 100; i++ {
		page1 = append(page1, event(fmt.Sprintf("push-%d", i), "PushEvent", "bot", now.Add(-time.Duration(i)*time.Second)))
	}
	page2 := []map[string]any{event("star-new", "WatchEvent", "carol", now.Add(-10*time.Minute))}
	for i := 0; i < 98; i++ {
		page2 = append(page2, event(fmt.Sprintf("pr-%d", i), "PullRequestEvent", "bot", now.Add(-11*time.Minute)))
	}
	page2 = append(page2, event("star-old", "WatchEvent", "bob", now.Add(-time.Hour)))
	server := &graphQLRepoServer{events: [][]map[string]any{page1, page2, {event("never", "PushEvent", "bot", now.Add(-2*time.Hour))}}}
	plugin := newGraphQLRepoPlugin(t, server, now)
	change, _, err := plugin.fetchStars(context.Background(), "acme/demo", repositoryWatchSnapshot{StarEventID: "star-old", StarEventAt: now.Add(-time.Hour), StarCount: 7}, SettingValues{})
	if err != nil {
		t.Fatal(err)
	}
	if change == nil || len(change.AddedUsers) != 1 || change.AddedUsers[0].Login != "carol" {
		t.Fatalf("第二页里的新 star 漏了：%#v", change)
	}
	if strings.Join(server.eventPages, ",") != "1,2" {
		t.Fatalf("应翻到看见上次事件的那一页就停：%v", server.eventPages)
	}
}

// 发布工具的查重扫描：有凭据时用 GraphQL 只列 issue，翻页读全；超过翻页上限照旧报扫描不完整。
func TestRepositoryIssueRecentScanUsesGraphQL(t *testing.T) {
	now := time.Now().UTC()
	pages := [][]map[string]any{{graphQLIssueNode(2, "OPEN", now, nil)}, {graphQLIssueNode(1, "CLOSED", now.Add(-time.Hour), nil)}}
	server := &graphQLRepoServer{issuePages: pages}
	httpServer := httptest.NewServer(http.HandlerFunc(server.handler))
	defer httpServer.Close()
	tool := newDianaRepositoryIssuesTool(NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil),
		MessageEvent{Kind: EventKindPrivate, UserID: "owner"}, newRepositoryPublishPlugin(httpServer.Client(), httpServer.URL),
		SettingValues{repositoryPublishSettingToken: "test-token", repositoryPublishSettingAllowlist: "acme/demo", repositoryPublishSettingTimeout: 5})
	issues, apiErr := tool.listRecentIssues(context.Background(), "acme/demo")
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if len(issues) != 2 || issues[0].Number != 2 || issues[1].State != "closed" || server.restIssueHits != 0 {
		t.Fatalf("issues=%#v rest=%d", issues, server.restIssueHits)
	}

	server.mu.Lock()
	server.issuePages = nil
	for i := 0; i <= repositoryIssueListMaxPages; i++ {
		server.issuePages = append(server.issuePages, []map[string]any{graphQLIssueNode(100+i, "OPEN", now, nil)})
	}
	server.mu.Unlock()
	if _, apiErr := tool.listRecentIssues(context.Background(), "acme/demo"); apiErr == nil || apiErr.Code != "idempotency_scan_incomplete" {
		t.Fatalf("超过翻页上限应报扫描不完整：%#v", apiErr)
	}
}
