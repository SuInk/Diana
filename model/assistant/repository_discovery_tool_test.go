// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// repositoryDiscoveryTestGitHub 是 repo_search / repo 的夹具：仓库搜索和仓库元信息
// 各自可注入返回内容，并记下查询串供「限定符是后端拼的、注入的被挡在网络之前」
// 一类断言使用。
type repositoryDiscoveryTestGitHub struct {
	mu       sync.Mutex
	queries  []url.Values
	requests []string

	items   []map[string]any
	profile map[string]any
}

func (s *repositoryDiscoveryTestGitHub) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	s.queries = append(s.queries, r.URL.Query())
	items, profile := s.items, s.profile
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/search/repositories":
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case "/repos/acme/demo":
		_ = json.NewEncoder(w).Encode(profile)
	default:
		http.NotFound(w, r)
	}
}

func (s *repositoryDiscoveryTestGitHub) lastQuery() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		return url.Values{}
	}
	return s.queries[len(s.queries)-1]
}

func (s *repositoryDiscoveryTestGitHub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func repositoryDiscoveryTestRepo(fullName string, stars, forks int, pushedAgo time.Duration, extra map[string]any) map[string]any {
	item := map[string]any{
		"full_name":         fullName,
		"html_url":          "https://github.com/" + fullName,
		"description":       fullName + " 的简介",
		"language":          "Go",
		"stargazers_count":  stars,
		"forks_count":       forks,
		"open_issues_count": 7,
		"pushed_at":         time.Now().Add(-pushedAgo).UTC().Format(time.RFC3339),
		"updated_at":        time.Now().UTC().Format(time.RFC3339),
	}
	for key, value := range extra {
		item[key] = value
	}
	return item
}

func repositoryDiscoveryTestTool(server *httptest.Server, userID string, settings SettingValues) *dianaRepositoryIssuesTool {
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	return newDianaRepositoryIssuesTool(
		runtime,
		MessageEvent{Kind: EventKindPrivate, UserID: userID, RawMessage: "有没有好用的库推荐"},
		newRepositoryPublishPlugin(server.Client(), server.URL),
		settings,
	)
}

func repositoryDiscoveryDefaultSettings() SettingValues {
	return SettingValues{
		repositoryPublishSettingToken:     repositoryPublishTestToken,
		repositoryPublishSettingAllowlist: "acme/demo",
		repositoryPublishSettingTimeout:   5,
	}
}

// 推荐仓库靠的就是这三项：star 说明多少人在用，fork 说明多少人真拿去改，
// pushed_ago 说明还有没有人维护。少一项模型就只能拿简介去猜。
func TestRepositoryDiscoverySearchReportsStarsForksAndFreshness(t *testing.T) {
	github := &repositoryDiscoveryTestGitHub{items: []map[string]any{
		repositoryDiscoveryTestRepo("acme/active", 5200, 610, 72*time.Hour, nil),
		repositoryDiscoveryTestRepo("acme/abandoned", 88, 3, 800*24*time.Hour, map[string]any{"archived": true}),
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	result := runRepositoryPublishToolOnce(t, repositoryDiscoveryTestTool(server, "owner", repositoryDiscoveryDefaultSettings()), map[string]any{
		"operation": "repo_search", "query": "http router", "language": "go", "sort": "stars", "limit": 5,
	})
	if !result.OK || result.Outcome != "searched" || len(result.Repositories) != 2 {
		t.Fatalf("repo_search result=%#v", result)
	}

	query := github.lastQuery()
	if got := query.Get("q"); !strings.Contains(got, "http router") || !strings.Contains(got, "is:public") || !strings.Contains(got, "language:go") {
		t.Fatalf("search query=%q", got)
	}
	if query.Get("sort") != "stars" || query.Get("order") != "desc" || query.Get("per_page") != "5" {
		t.Fatalf("search params=%#v", query)
	}

	active := result.Repositories[0]
	if active.Repository != "acme/active" || active.Stars != 5200 || active.Forks != 610 {
		t.Fatalf("active repository=%#v", active)
	}
	if active.PushedAt.IsZero() || active.PushedAgo == "" || active.Stale || active.Archived {
		t.Fatalf("active freshness=%#v", active)
	}

	abandoned := result.Repositories[1]
	if !abandoned.Stale || !abandoned.Archived || abandoned.Forks != 3 {
		t.Fatalf("abandoned repository=%#v", abandoned)
	}
	if !strings.Contains(result.Message, "stale") || !strings.Contains(result.Message, "archived") {
		t.Fatalf("message does not tell the model how to read the metrics: %q", result.Message)
	}
}

// 仓库搜索走公共凭据，GitHub 会把 Token 看得见的私有仓库一并搜出来。查询里钉死
// is:public 之外，返回结果也要再过一遍：主人的私仓不该因为群里有人搜了个词就露名字。
func TestRepositoryDiscoverySearchDropsPrivateAndForeignResults(t *testing.T) {
	github := &repositoryDiscoveryTestGitHub{items: []map[string]any{
		repositoryDiscoveryTestRepo("acme/secret", 4, 0, time.Hour, map[string]any{"private": true}),
		repositoryDiscoveryTestRepo("acme/spoofed", 9000, 900, time.Hour, map[string]any{"html_url": "https://evil.example.com/acme/spoofed"}),
		repositoryDiscoveryTestRepo("acme/public", 120, 12, time.Hour, nil),
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	result := runRepositoryPublishToolOnce(t, repositoryDiscoveryTestTool(server, "owner", repositoryDiscoveryDefaultSettings()), map[string]any{
		"operation": "repo_search", "query": "内部工具",
	})
	if !result.OK || len(result.Repositories) != 1 || result.Repositories[0].Repository != "acme/public" {
		t.Fatalf("repo_search leaked results=%#v", result.Repositories)
	}
}

func TestRepositoryDiscoverySearchRejectsQualifierInjectionWithoutRequest(t *testing.T) {
	github := &repositoryDiscoveryTestGitHub{}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	tool := repositoryDiscoveryTestTool(server, "owner", repositoryDiscoveryDefaultSettings())

	for _, input := range []map[string]any{
		{"operation": "repo_search", "query": "router repo:acme/demo"},
		{"operation": "repo_search", "query": "router", "language": "go rust"},
		{"operation": "repo_search", "query": "router", "sort": "popularity"},
		{"operation": "repo_search", "query": "router", "limit": 50},
		{"operation": "repo_search"},
	} {
		result := runRepositoryPublishToolOnce(t, tool, input)
		if result.OK || result.FailureCode != "invalid_input" {
			t.Fatalf("input=%#v result=%#v", input, result)
		}
	}
	if github.requestCount() != 0 {
		t.Fatalf("rejected input still reached GitHub: %d requests", github.requestCount())
	}
}

// pushed_at 才是「还有没有人维护」。updated_at 改个简介、加个 topic 就会往前跳，
// 拿它判断新鲜度会把停更两年的仓库说成刚更新过。
func TestRepositoryProfileJudgesFreshnessByPushedAt(t *testing.T) {
	pushed := time.Now().Add(-700 * 24 * time.Hour).UTC().Truncate(time.Second)
	github := &repositoryDiscoveryTestGitHub{profile: map[string]any{
		"full_name":         "acme/demo",
		"html_url":          "https://github.com/acme/demo",
		"description":       "演示仓库",
		"language":          "Go",
		"topics":            []string{"cli", "demo"},
		"license":           map[string]any{"spdx_id": "MIT"},
		"stargazers_count":  310,
		"forks_count":       26,
		"open_issues_count": 4,
		"pushed_at":         pushed.Format(time.RFC3339),
		"updated_at":        time.Now().UTC().Format(time.RFC3339),
		"private":           false,
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	result := runRepositoryPublishToolOnce(t, repositoryDiscoveryTestTool(server, "owner", repositoryDiscoveryDefaultSettings()), map[string]any{
		"operation": "repo", "repository": "acme/demo",
	})
	if !result.OK || result.RepositoryProfile == nil {
		t.Fatalf("repo result=%#v", result)
	}
	profile := *result.RepositoryProfile
	if !profile.PushedAt.Equal(pushed) || !profile.Stale {
		t.Fatalf("freshness came from updated_at instead of pushed_at: %#v", profile)
	}
	if profile.Stars != 310 || profile.Forks != 26 || profile.License != "MIT" || len(profile.Topics) != 2 {
		t.Fatalf("profile=%#v", profile)
	}
	if !strings.Contains(result.Message, "310 star") || !strings.Contains(result.Message, "26 fork") {
		t.Fatalf("message=%q", result.Message)
	}
}

// repo 是读操作，和 get、read_file 走同一套按可见性分流的 ACL：私有仓库不能靠
// 「只看 star 数」绕过授权名单。
func TestRepositoryProfilePrivateRepositoryStaysGated(t *testing.T) {
	github := &repositoryDiscoveryTestGitHub{profile: map[string]any{
		"full_name": "acme/demo", "html_url": "https://github.com/acme/demo",
		"stargazers_count": 3, "forks_count": 0, "private": true,
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	result := runRepositoryPublishToolOnce(t, repositoryDiscoveryTestTool(server, "outsider", repositoryDiscoveryDefaultSettings()), map[string]any{
		"operation": "repo", "repository": "acme/demo",
	})
	if result.OK || result.FailureCode != "permission_denied" || result.RepositoryProfile != nil {
		t.Fatalf("private profile leaked=%#v", result)
	}
}
