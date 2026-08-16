// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type repositoryWatchTestGitHub struct {
	mu           sync.Mutex
	commits      []map[string]any
	pullRequests []map[string]any
	pullFiles    map[int][]map[string]any
	releases     []map[string]any
	starCount    int
	token        string
	commitCalls  int
	pullCalls    int
	releaseCalls int
	starCalls    int
	diffCalls    int
	failCommits  bool
	failReleases bool
}

func (s *repositoryWatchTestGitHub) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && r.Header.Get("Authorization") != "Bearer "+s.token {
		http.Error(w, `{"message":"bad token"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.URL.Path, "/commits") {
		s.commitCalls++
		if s.failCommits {
			http.Error(w, `{"message":"commit endpoint unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(s.commits)
		return
	}
	if strings.Contains(r.URL.Path, "/compare/") {
		s.diffCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_commits": 1,
			"ahead_by":      1,
			"files": []map[string]any{{
				"filename": "model/assistant/runtime.go", "status": "modified",
				"additions": 4, "deletions": 1, "changes": 5,
				"patch": "@@ -1 +1 @@\n-old\n+new",
			}},
		})
		return
	}
	if strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/files") {
		var number int
		_, _ = fmt.Sscanf(r.URL.Path, "/repos/acme/demo/pulls/%d/files", &number)
		_ = json.NewEncoder(w).Encode(s.pullFiles[number])
		return
	}
	if strings.HasSuffix(r.URL.Path, "/pulls") {
		s.pullCalls++
		_ = json.NewEncoder(w).Encode(s.pullRequests)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/releases") {
		s.releaseCalls++
		if s.failReleases {
			http.Error(w, `{"message":"release endpoint unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(s.releases)
		return
	}
	if r.URL.Path == "/repos/acme/demo" {
		s.starCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"stargazers_count": s.starCount, "html_url": "https://github.com/acme/demo"})
		return
	}
	http.NotFound(w, r)
}

func repositoryWatchCommitPayload(sha, title string) map[string]any {
	return map[string]any{
		"sha": sha, "html_url": "https://github.com/acme/demo/commit/" + sha,
		"commit": map[string]any{
			"message": title + "\n\nbody",
			"author":  map[string]any{"name": "Diana", "date": "2026-08-13T00:00:00Z"},
		},
		"author": map[string]any{"login": "diana"},
	}
}

func repositoryWatchReleasePayload(tag, name string) map[string]any {
	return map[string]any{
		"tag_name": tag, "name": name, "body": "完整更新说明",
		"html_url":     "https://github.com/acme/demo/releases/tag/" + tag,
		"published_at": "2026-08-13T00:00:00Z", "draft": false,
	}
}

func repositoryWatchPullPayload(number int, title, state, mergeSHA, updatedAt string) map[string]any {
	var mergedAt any
	if state == "merged" {
		mergedAt = updatedAt
		state = "closed"
	}
	return map[string]any{
		"number": number, "title": title, "state": state,
		"html_url":   "https://github.com/acme/demo/pull/" + fmt.Sprint(number),
		"created_at": "2026-08-13T00:00:00Z", "updated_at": updatedAt, "merged_at": mergedAt,
		"merge_commit_sha": mergeSHA, "user": map[string]any{"login": "diana"},
		"base": map[string]any{"ref": "main"}, "head": map[string]any{"ref": "feature"},
	}
}

func TestNormalizeGitHubRepository(t *testing.T) {
	for _, raw := range []string{"SuInk/Diana", "https://github.com/SuInk/Diana", "https://github.com/SuInk/Diana.git"} {
		got, err := normalizeGitHubRepository(raw)
		if err != nil || got != "SuInk/Diana" {
			t.Fatalf("normalize %q = %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "github.com/a", "https://example.com/a/b", "a/b/c", "a/../b"} {
		if _, err := normalizeGitHubRepository(raw); err == nil {
			t.Fatalf("invalid repository %q was accepted", raw)
		}
	}
}

func TestDefaultPluginManagerIncludesRepositoryWatch(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := manager.Get(repositoryWatchPluginID)
	if !ok || !state.Installed || !state.Enabled || !state.Manifest.BuiltIn || !state.Manifest.Official {
		t.Fatalf("repository watch plugin state=%#v found=%v", state, ok)
	}
	plugin, settings, enabled := manager.PluginWithSettings(repositoryWatchPluginID, nil)
	if !enabled || plugin == nil || settings.Int(repositoryWatchSettingTimeout, 0) != 20 {
		t.Fatalf("plugin=%T settings=%#v enabled=%v", plugin, settings, enabled)
	}
}

func TestRepositoryWatchIntervalPolicyDependsOnToken(t *testing.T) {
	tests := []struct {
		name     string
		settings SettingValues
		want     time.Duration
	}{
		{name: "anonymous", want: time.Hour},
		{name: "authenticated", settings: SettingValues{repositoryWatchSettingToken: "secret"}, want: time.Minute},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			interval, err := parseRepositoryWatchInterval("", test.settings)
			if err != nil || interval != test.want {
				t.Fatalf("interval=%s err=%v", interval, err)
			}
			if interval, err := parseRepositoryWatchInterval("30s", test.settings); err != nil || interval != 30*time.Second {
				t.Fatalf("custom interval=%s err=%v", interval, err)
			}
		})
	}
	if _, err := parseRepositoryWatchInterval("8761h", nil); err == nil {
		t.Fatal("interval above 365 days was accepted")
	}
}

func TestRepositoryWatchPluginBuildsBaselineAndChanges(t *testing.T) {
	github := &repositoryWatchTestGitHub{
		token:    "secret",
		commits:  []map[string]any{repositoryWatchCommitPayload("old-sha", "initial")},
		releases: []map[string]any{repositoryWatchReleasePayload("v1.0.0", "First")},
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)
	settings := SettingValues{repositoryWatchSettingToken: "secret", repositoryWatchSettingTimeout: 5, repositoryWatchSettingLimit: 12}

	baseline, err := plugin.snapshot(context.Background(), "acme/demo", "main", true, true, settings)
	if err != nil || baseline.CommitSHA != "old-sha" || baseline.ReleaseTag != "v1.0.0" {
		t.Fatalf("baseline=%#v err=%v", baseline, err)
	}

	github.mu.Lock()
	github.commits = []map[string]any{
		repositoryWatchCommitPayload("new-sha", "fix delivery"),
		repositoryWatchCommitPayload("old-sha", "initial"),
	}
	github.releases = []map[string]any{
		repositoryWatchReleasePayload("v1.1.0", "Second"),
		repositoryWatchReleasePayload("v1.0.0", "First"),
	}
	github.mu.Unlock()
	change, err := plugin.check(context.Background(), "acme/demo", "main", baseline.CommitSHA, baseline.ReleaseTag, true, true, settings)
	if err != nil {
		t.Fatal(err)
	}
	if change.Snapshot.CommitSHA != "new-sha" || change.Snapshot.ReleaseTag != "v1.1.0" || len(change.Commits) != 1 || len(change.Releases) != 1 {
		t.Fatalf("change=%#v", change)
	}
}

func TestRepositoryWatchPluginClassifiesPullRequestsStarsAndReadsDiffs(t *testing.T) {
	github := &repositoryWatchTestGitHub{
		commits:      []map[string]any{repositoryWatchCommitPayload("base-sha", "initial")},
		pullRequests: []map[string]any{repositoryWatchPullPayload(1, "initial PR", "open", "", "2026-08-13T00:00:00Z")},
		releases:     []map[string]any{repositoryWatchReleasePayload("v1.0.0", "First")},
		starCount:    10,
		pullFiles:    map[int][]map[string]any{},
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)
	selection := repositoryWatchSelection{Commits: true, PullRequests: true, Releases: true, Stars: true}

	baseline, err := plugin.snapshotSelected(context.Background(), "acme/demo", "main", selection, nil)
	if err != nil || baseline.CommitSHA != "base-sha" || baseline.PullRequestCursor == "" || baseline.ReleaseTag != "v1.0.0" || baseline.StarCount != 10 || !baseline.HasStarCount {
		t.Fatalf("baseline=%#v err=%v", baseline, err)
	}

	github.mu.Lock()
	github.commits = []map[string]any{
		repositoryWatchCommitPayload("direct-sha", "improve watcher"),
		repositoryWatchCommitPayload("merge-sha", "merge pull request"),
		repositoryWatchCommitPayload("base-sha", "initial"),
	}
	github.pullRequests = []map[string]any{
		repositoryWatchPullPayload(2, "add PR classification", "merged", "merge-sha", "2026-08-14T00:00:00Z"),
		repositoryWatchPullPayload(1, "initial PR", "open", "", "2026-08-13T00:00:00Z"),
	}
	github.pullFiles[2] = []map[string]any{{
		"filename": "model/assistant/repository_watch_plugin.go", "status": "modified",
		"additions": 20, "deletions": 2, "changes": 22, "patch": "@@ -1 +1 @@\n-old\n+new PR support",
	}}
	github.releases = []map[string]any{
		repositoryWatchReleasePayload("v1.1.0", "Second"),
		repositoryWatchReleasePayload("v1.0.0", "First"),
	}
	github.starCount = 13
	github.mu.Unlock()

	change, err := plugin.checkSelected(context.Background(), "acme/demo", "main", baseline, selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(change.Commits) != 1 || change.Commits[0].SHA != "direct-sha" {
		t.Fatalf("commits were not classified: %#v", change.Commits)
	}
	if change.CommitDiff == nil || len(change.CommitDiff.Files) != 1 || !strings.Contains(change.CommitDiff.Files[0].Patch, "+new") {
		t.Fatalf("commit diff=%#v", change.CommitDiff)
	}
	if len(change.PullRequests) != 1 || change.PullRequests[0].Status != "merged" || len(change.PullRequests[0].Files) != 1 {
		t.Fatalf("pull requests=%#v", change.PullRequests)
	}
	if change.Stars == nil || change.Stars.Previous != 10 || change.Stars.Current != 13 || change.Stars.Delta != 3 {
		t.Fatalf("stars=%#v", change.Stars)
	}
	if change.Snapshot.StarCount != 13 || change.Snapshot.PullRequestCursor == baseline.PullRequestCursor {
		t.Fatalf("snapshot=%#v", change.Snapshot)
	}
	rendered := renderRepositoryWatchChanges(change)
	for _, want := range []string{"Commit · 1", "PR · 1", "#2 [已合并]", "Release · 1", "Star", "10 → 13（+3）"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered changes missing %q: %s", want, rendered)
		}
	}
}

func TestRepositoryWatchCommitLimitOnlyMarksActualOverflow(t *testing.T) {
	github := &repositoryWatchTestGitHub{}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)
	settings := SettingValues{repositoryWatchSettingLimit: 12}

	commits := make([]map[string]any, 0, 14)
	for index := 12; index >= 1; index-- {
		sha := fmt.Sprintf("new-%02d", index)
		commits = append(commits, repositoryWatchCommitPayload(sha, sha))
	}
	commits = append(commits, repositoryWatchCommitPayload("cursor", "previous checkpoint"))
	github.commits = commits

	change, err := plugin.check(context.Background(), "acme/demo", "main", "cursor", "", true, false, settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(change.Commits) != 12 || change.Truncated {
		t.Fatalf("exact-limit change=%#v", change)
	}

	github.mu.Lock()
	github.commits = append([]map[string]any{repositoryWatchCommitPayload("new-13", "new-13")}, commits...)
	github.mu.Unlock()
	change, err = plugin.check(context.Background(), "acme/demo", "main", "cursor", "", true, false, settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(change.Commits) != 12 || !change.Truncated || change.Snapshot.CommitSHA != "new-13" {
		t.Fatalf("overflow change=%#v", change)
	}
}

func TestRepositoryWatchMissingCursorDoesNotMarkShortResultTruncated(t *testing.T) {
	github := &repositoryWatchTestGitHub{commits: []map[string]any{
		repositoryWatchCommitPayload("new-03", "third"),
		repositoryWatchCommitPayload("new-02", "second"),
		repositoryWatchCommitPayload("new-01", "first"),
	}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)

	change, err := plugin.check(context.Background(), "acme/demo", "main", "rewritten-cursor", "", true, false, SettingValues{repositoryWatchSettingLimit: 12})
	if err != nil {
		t.Fatal(err)
	}
	if len(change.Commits) != 3 || change.Truncated || change.Snapshot.CommitSHA != "new-03" {
		t.Fatalf("missing-cursor short change=%#v", change)
	}
}

func TestRenderRepositoryWatchChangesAlwaysIncludesEveryFetchedCommit(t *testing.T) {
	change := repositoryWatchChange{
		Commits: []repositoryWatchCommit{
			{SHA: "1111111aaaa", Title: "first", URL: "https://example.test/1"},
			{SHA: "2222222bbbb", Title: "second", URL: "https://example.test/2"},
			{SHA: "3333333cccc", Title: "third", URL: "https://example.test/3"},
		},
		Releases:  []repositoryWatchRelease{{Tag: "v1.2.3", Name: "Stable", URL: "https://example.test/release"}},
		Truncated: true,
	}
	result := renderRepositoryWatchChanges(change)
	for _, want := range []string{"Commit · 3", "1111111 first", "2222222 second", "3333333 third", "Release · 1", "v1.2.3: Stable", "本次只展示了部分最新提交。"} {
		if !strings.Contains(result, want) {
			t.Fatalf("rendered changes missing %q: %s", want, result)
		}
	}
}

func TestRepositoryWatchPluginDetectsFirstReleaseAfterEmptyBaseline(t *testing.T) {
	github := &repositoryWatchTestGitHub{
		commits: []map[string]any{repositoryWatchCommitPayload("base-sha", "initial")},
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)

	baseline, err := plugin.snapshot(context.Background(), "acme/demo", "", true, true, nil)
	if err != nil || baseline.ReleaseTag != repositoryWatchNoReleaseCursor {
		t.Fatalf("baseline=%#v err=%v", baseline, err)
	}
	github.mu.Lock()
	github.releases = []map[string]any{repositoryWatchReleasePayload("v1.0.0", "First")}
	github.mu.Unlock()
	change, err := plugin.check(context.Background(), "acme/demo", "", baseline.CommitSHA, baseline.ReleaseTag, false, true, nil)
	if err != nil || len(change.Releases) != 1 || change.Releases[0].Tag != "v1.0.0" || change.Snapshot.ReleaseTag != "v1.0.0" {
		t.Fatalf("change=%#v err=%v", change, err)
	}
}

func TestRepositoryWatchPluginBuildsOnlyRequestedBaseline(t *testing.T) {
	github := &repositoryWatchTestGitHub{
		commits:      []map[string]any{repositoryWatchCommitPayload("base-sha", "initial")},
		failReleases: true,
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)

	baseline, err := plugin.snapshot(context.Background(), "acme/demo", "", true, false, nil)
	if err != nil || baseline.CommitSHA != "base-sha" || baseline.ReleaseTag != "" {
		t.Fatalf("baseline=%#v err=%v", baseline, err)
	}
	github.mu.Lock()
	defer github.mu.Unlock()
	if github.commitCalls != 1 || github.releaseCalls != 0 {
		t.Fatalf("commit calls=%d release calls=%d", github.commitCalls, github.releaseCalls)
	}
}

func TestRepositoryWatchPluginExplainsPrivateRepositoryAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer private-secret" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)

	_, err := plugin.snapshot(context.Background(), "acme/private", "", true, false, nil)
	if err == nil || !strings.Contains(err.Error(), "私有仓库请先") {
		t.Fatalf("missing-token error=%v", err)
	}
	_, err = plugin.snapshot(context.Background(), "acme/private", "", true, false, SettingValues{repositoryWatchSettingToken: "wrong-secret"})
	if err == nil || !strings.Contains(err.Error(), "无效或已过期") || strings.Contains(err.Error(), "wrong-secret") {
		t.Fatalf("invalid-token error=%v", err)
	}
	_, err = plugin.snapshot(context.Background(), "acme/private", "", true, false, SettingValues{repositoryWatchSettingToken: "private-secret"})
	if err == nil || !strings.Contains(err.Error(), "Contents: read") || strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("permission error=%v", err)
	}
}

func TestRepositoryWatchPluginGuidesRateLimitedRequestsToSettings(t *testing.T) {
	tests := []struct {
		name     string
		settings SettingValues
		want     []string
	}{
		{
			name: "anonymous public request",
			want: []string{"公开仓库同样受限", "插件 → 仓库更新订阅 → 设置", "GitHub Token"},
		},
		{
			name:     "authenticated request",
			settings: SettingValues{repositoryWatchSettingToken: "exhausted-token"},
			want:     []string{"Token 请求额度已耗尽", "插件 → 仓库更新订阅 → 设置", "更换 Token"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-RateLimit-Remaining", "0")
				http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
			}))
			defer server.Close()
			plugin := newRepositoryWatchPlugin(server.Client(), server.URL)

			_, err := plugin.snapshot(context.Background(), "acme/public", "", true, false, test.settings)
			if err == nil {
				t.Fatal("rate-limited request unexpectedly succeeded")
			}
			for _, text := range test.want {
				if !strings.Contains(err.Error(), text) {
					t.Fatalf("error %q does not contain %q", err, text)
				}
			}
			if strings.Contains(err.Error(), "exhausted-token") {
				t.Fatalf("error exposed token: %q", err)
			}
		})
	}
}

func TestRuntimeCreatesRepositoryWatchForWebUI(t *testing.T) {
	github := &repositoryWatchTestGitHub{
		commits:  []map[string]any{repositoryWatchCommitPayload("base-sha", "initial")},
		releases: []map[string]any{repositoryWatchReleasePayload("v1.0.0", "First")},
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(plugin), nil, store, nil, nil)
	item, err := runtime.CreateRepositoryWatch(context.Background(), RepositoryWatchCreateInput{
		Repository:   "https://github.com/acme/demo",
		WatchCommits: true, WatchReleases: true, Platform: PlatformOneBotV11,
		ProfileID: "qq-main", GroupID: "123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 {
		t.Fatalf("items=%#v", store.items)
	}
	if item.Kind != ReminderKindRepositoryWatch || item.OwnerID != "webui:qq-main" || item.GroupID != "123" || item.UserID != "" || item.ProfileID != "qq-main" || item.LastCommitSHA != "base-sha" || item.LastReleaseTag != "v1.0.0" {
		t.Fatalf("item=%#v", item)
	}
	if remaining := time.Until(item.TriggerAt); remaining < 59*time.Minute || remaining > 61*time.Minute {
		t.Fatalf("next run=%s", item.TriggerAt)
	}
}

func TestRuntimeUpdatesRepositoryWatchDelivery(t *testing.T) {
	store := &stubReminderStore{items: []Reminder{{
		ID: "watch-delivery", Kind: ReminderKindRepositoryWatch,
		Platform: PlatformOneBotV11, ProfileID: "old-bot", ContextNamespace: "old-bot",
		OwnerID: "webui:old-bot", UserID: "10001", Repository: "acme/demo",
		WatchCommits: true, IntervalSeconds: 60, TriggerAt: time.Now().Add(time.Minute),
	}}}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(NewRepositoryWatchPlugin(nil)), nil, store, nil, nil)
	item, err := runtime.UpdateRepositoryWatch(context.Background(), "webui:old-bot", "watch-delivery", RepositoryWatchUpdateInput{
		Delivery: true, Platform: PlatformOneBotV11, ProfileID: "new-bot", ContextNamespace: "new-bot",
		OwnerID: "webui:new-bot", GroupID: "123456",
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.ProfileID != "new-bot" || item.ContextNamespace != "new-bot" || item.OwnerID != "webui:new-bot" || item.GroupID != "123456" || item.UserID != "" {
		t.Fatalf("updated delivery=%#v", item)
	}
	if len(store.items) != 1 || store.items[0] != item {
		t.Fatalf("stored=%#v updated=%#v", store.items, item)
	}
}

func TestRuntimeRepositoryWatchStaysSilentWithoutChanges(t *testing.T) {
	github := &repositoryWatchTestGitHub{
		commits:  []map[string]any{repositoryWatchCommitPayload("base-sha", "initial")},
		releases: []map[string]any{repositoryWatchReleasePayload("v1.0.0", "First")},
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)
	store := &stubReminderStore{items: []Reminder{{
		ID: "watch-1", Kind: ReminderKindRepositoryWatch, OwnerID: "owner", UserID: "owner",
		Repository: "acme/demo", WatchCommits: true, WatchReleases: true,
		LastCommitSHA: "base-sha", LastReleaseTag: "v1.0.0",
		TriggerAt: time.Now().Add(-time.Minute), IntervalSeconds: 1800, CreatedAt: time.Now().Add(-time.Hour),
	}}}
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{"不应该被调用"}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(plugin), nil, store, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.fireDueReminders(context.Background())
	if len(channel.sent) != 0 || len(provider.requests) != 0 {
		t.Fatalf("sent=%#v requests=%d", channel.sent, len(provider.requests))
	}
	if store.items[0].LastRunAt.IsZero() || store.items[0].TriggerAt.Before(time.Now().Add(29*time.Minute)) {
		t.Fatalf("item=%#v", store.items[0])
	}
}

func TestRuntimeRepositoryWatchSummarizesAndAdvancesCursors(t *testing.T) {
	github := &repositoryWatchTestGitHub{
		commits: []map[string]any{
			repositoryWatchCommitPayload("new-sha", "fix delivery"),
			repositoryWatchCommitPayload("base-sha", "initial"),
		},
		pullRequests: []map[string]any{
			repositoryWatchPullPayload(2, "add classified notifications", "open", "", "2026-08-14T00:00:00Z"),
			repositoryWatchPullPayload(1, "baseline", "open", "", "2026-08-13T00:00:00Z"),
		},
		pullFiles: map[int][]map[string]any{2: {{
			"filename": "model/assistant/repository_watch_plugin.go", "status": "modified",
			"additions": 12, "deletions": 1, "changes": 13, "patch": "@@ -1 +1 @@\n-old\n+classified",
		}}},
		releases: []map[string]any{
			repositoryWatchReleasePayload("v1.1.0", "Second"),
			repositoryWatchReleasePayload("v1.0.0", "First"),
		},
		starCount: 8,
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)
	store := &stubReminderStore{items: []Reminder{{
		ID: "watch-2", Kind: ReminderKindRepositoryWatch, OwnerID: "owner", GroupID: "123", UserID: "owner",
		Repository: "acme/demo", WatchCommits: true, WatchPullRequests: true, WatchReleases: true, WatchStars: true,
		LastCommitSHA: "base-sha", LastPullRequestCursor: repositoryWatchPullCursor(time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC), 1),
		LastReleaseTag: "v1.0.0", LastStarCount: 7,
		TriggerAt: time.Now().Add(-time.Minute), IntervalSeconds: 1800, CreatedAt: time.Now().Add(-time.Hour),
	}}}
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{"修复了投递，并发布 v1.1.0。"}}
	runtime := NewRuntime(BotConfig{RequestTimeout: 5 * time.Second, SystemPrompt: "本群限定的自然人设", DirectReplyChunkSize: 5000, ForwardReplyThreshold: 5000}, channel, NewPluginManager(plugin), nil, store, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.fireDueReminders(context.Background())
	var sentParts []string
	for _, sent := range channel.sent {
		if sent.GroupID != "123" {
			t.Fatalf("sent to unexpected group: %#v", channel.sent)
		}
		sentParts = append(sentParts, sent.Text)
	}
	sentText := strings.Join(sentParts, "\n")
	if sentText == "" {
		sentText = fmt.Sprint(channel.calls)
	}
	delivered := len(channel.sent) > 0 || len(channel.calls) > 0 && channel.calls[0].action == "send_group_forward_msg"
	if !delivered || !strings.Contains(sentText, "Commit · 1") || !strings.Contains(sentText, "new-sha fix delivery") || !strings.Contains(sentText, "PR · 1") || !strings.Contains(sentText, "Release · 1") || !strings.Contains(sentText, "Star") || !strings.Contains(sentText, "v1.1.0") || strings.Contains(sentText, "watch-2") {
		t.Fatalf("sent=%#v calls=%#v item=%#v requests=%#v", channel.sent, channel.calls, store.items[0], provider.requests)
	}
	item := store.items[0]
	if item.LastCommitSHA != "new-sha" || item.LastPullRequestCursor == "" || item.LastReleaseTag != "v1.1.0" || item.LastStarCount != 8 || item.PendingDelivery != "" || item.ConsecutiveFailures != 0 {
		t.Fatalf("item=%#v", item)
	}
	if len(provider.requests) != 1 || !requestMessagesContain(provider.requests[0].Messages, "fix delivery") || !requestMessagesContain(provider.requests[0].Messages, "classified notifications") || !requestMessagesContain(provider.requests[0].Messages, "repository_watch_plugin.go") || !requestMessagesContain(provider.requests[0].Messages, "+classified") || !requestMessagesContain(provider.requests[0].Messages, "v1.1.0") || !requestMessagesContain(provider.requests[0].Messages, "本群限定的自然人设") || !requestMessagesContain(provider.requests[0].Messages, "自然反应或评价") || !requestMessagesContain(provider.requests[0].Messages, "不要无依据吹捧") || requestMessagesContain(provider.requests[0].Messages, "watch-2") {
		t.Fatalf("requests=%#v", provider.requests)
	}
}

func TestRuntimeRepositoryWatchRetriesStoredSummaryWithoutCallingLLMAgain(t *testing.T) {
	github := &repositoryWatchTestGitHub{
		commits: []map[string]any{
			repositoryWatchCommitPayload("new-sha", "fix delivery"),
			repositoryWatchCommitPayload("base-sha", "initial"),
		},
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)
	store := &stubReminderStore{items: []Reminder{{
		ID: "watch-retry", Kind: ReminderKindRepositoryWatch, OwnerID: "owner", GroupID: "123", UserID: "owner",
		Repository: "acme/demo", WatchCommits: true, LastCommitSHA: "base-sha",
		TriggerAt: time.Now().Add(-time.Minute), IntervalSeconds: 1800, CreatedAt: time.Now().Add(-time.Hour),
	}}}
	channel := &scriptedChannel{sendErrs: []error{context.DeadlineExceeded, nil, nil}}
	provider := &sequenceLLMProvider{replies: []string{"修复了消息投递。"}}
	runtime := NewRuntime(BotConfig{RequestTimeout: 5 * time.Second}, channel, NewPluginManager(plugin), nil, store, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.fireDueReminders(context.Background())
	if len(provider.requestsSnapshot()) != 1 || store.items[0].PendingDelivery == "" || store.items[0].LastCommitSHA != "new-sha" || store.items[0].ConsecutiveFailures != 1 {
		t.Fatalf("requests=%d item=%#v", len(provider.requestsSnapshot()), store.items[0])
	}

	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	runtime.fireDueReminders(context.Background())
	if len(provider.requestsSnapshot()) != 1 || store.items[0].PendingDelivery != "" || store.items[0].ConsecutiveFailures != 0 {
		t.Fatalf("requests=%d item=%#v", len(provider.requestsSnapshot()), store.items[0])
	}
	github.mu.Lock()
	defer github.mu.Unlock()
	if github.commitCalls != 1 {
		t.Fatalf("commit checks=%d, want 1", github.commitCalls)
	}
}

func TestRepositoryWatchFailureAlertThresholdPersistsAcrossRestartAndRecovers(t *testing.T) {
	github := &repositoryWatchTestGitHub{
		commits:     []map[string]any{repositoryWatchCommitPayload("base-sha", "initial")},
		failCommits: true,
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)
	store := &stubReminderStore{items: []Reminder{{
		ID: "watch-threshold", Kind: ReminderKindRepositoryWatch, OwnerID: "owner", GroupID: "123", UserID: "owner",
		Repository: "acme/demo", WatchCommits: true, LastCommitSHA: "base-sha",
		TriggerAt: time.Now().Add(-time.Minute), IntervalSeconds: 1800, CreatedAt: time.Now().Add(-time.Hour),
	}}}
	channel := &recordingChannel{}
	newRuntime := func() *Runtime {
		return NewRuntime(BotConfig{}, channel, NewPluginManager(plugin), nil, store, nil, nil)
	}
	runtime := newRuntime()

	for attempt := 1; attempt <= 3; attempt++ {
		store.items[0].TriggerAt = time.Now().Add(-time.Second)
		runtime.fireDueReminders(context.Background())
		if got := store.items[0].ConsecutiveFailures; got != attempt {
			t.Fatalf("attempt %d failures=%d item=%#v", attempt, got, store.items[0])
		}
		channel.mu.Lock()
		sent := append([]OutgoingMessage(nil), channel.sent...)
		channel.mu.Unlock()
		wantNotices := 0
		if attempt == repositoryWatchFailureAlertThreshold {
			wantNotices = 1
		}
		if len(sent) != wantNotices {
			t.Fatalf("attempt %d notices=%#v", attempt, sent)
		}
	}
	alerted := store.items[0]
	if alerted.FailureAlertedAt.IsZero() || alerted.LastFailureStage != repositoryWatchFailureStagePolling || alerted.LastErrorFingerprint == "" {
		t.Fatalf("alert state=%#v", alerted)
	}
	channel.mu.Lock()
	alertText := channel.sent[0].Text
	channel.mu.Unlock()
	for _, want := range []string{"acme/demo", "连续 3 次", "仓库更新检查", "自动重试"} {
		if !strings.Contains(alertText, want) {
			t.Fatalf("alert %q missing %q", alertText, want)
		}
	}
	for _, diagnostic := range []string{"commits", "503", "commit endpoint unavailable"} {
		if !strings.Contains(alertText, diagnostic) {
			t.Fatalf("alert %q lost diagnostic %q", alertText, diagnostic)
		}
	}

	// Simulate a process restart with the same persisted reminder state.
	runtime = newRuntime()
	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	runtime.fireDueReminders(context.Background())
	channel.mu.Lock()
	noticesAfterRestart := len(channel.sent)
	channel.mu.Unlock()
	if noticesAfterRestart != 1 || store.items[0].ConsecutiveFailures != 4 {
		t.Fatalf("restart duplicated alert: notices=%d item=%#v", noticesAfterRestart, store.items[0])
	}

	github.mu.Lock()
	github.failCommits = false
	github.mu.Unlock()
	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	runtime.fireDueReminders(context.Background())
	recovered := store.items[0]
	channel.mu.Lock()
	recoveryMessages := append([]OutgoingMessage(nil), channel.sent...)
	channel.mu.Unlock()
	if len(recoveryMessages) != 2 || !strings.Contains(recoveryMessages[1].Text, "已恢复") {
		t.Fatalf("recovery messages=%#v", recoveryMessages)
	}
	if recovered.ConsecutiveFailures != 0 || recovered.LastError != "" || recovered.LastFailureStage != "" || recovered.LastErrorFingerprint != "" || !recovered.FailureAlertedAt.IsZero() || recovered.RecoveryNoticePending {
		t.Fatalf("recovered state=%#v", recovered)
	}

	github.mu.Lock()
	github.failCommits = true
	github.mu.Unlock()
	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	runtime.fireDueReminders(context.Background())
	if store.items[0].ConsecutiveFailures != 1 {
		t.Fatalf("new failure sequence=%#v", store.items[0])
	}
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.sent) != 2 {
		t.Fatalf("first failure after recovery sent alert: %#v", channel.sent)
	}
}

func TestRepositoryWatchFailureStateIsIsolatedAndFingerprintAware(t *testing.T) {
	now := time.Now()
	store := &stubReminderStore{items: []Reminder{
		{ID: "watch-a", Kind: ReminderKindRepositoryWatch, Repository: "acme/a", GroupID: "1", IntervalSeconds: 60},
		{ID: "watch-b", Kind: ReminderKindRepositoryWatch, Repository: "acme/b", GroupID: "2", IntervalSeconds: 60},
	}}
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(), nil, store, nil, nil)
	pollFailure := repositoryWatchStageFailure(repositoryWatchFailureStagePolling, errors.New("GitHub API 503"))
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := runtime.finishRecurringReminder("watch-a", now, pollFailure); err != nil {
			t.Fatal(err)
		}
		if attempt < 2 {
			if _, err := runtime.finishRecurringReminder("watch-b", now, pollFailure); err != nil {
				t.Fatal(err)
			}
		}
	}
	if store.items[0].ConsecutiveFailures != 3 || store.items[1].ConsecutiveFailures != 2 {
		t.Fatalf("subscription counters leaked: %#v", store.items)
	}
	if !repositoryWatchFailureShouldAlert(store.items[0]) || repositoryWatchFailureShouldAlert(store.items[1]) {
		t.Fatalf("threshold state=%#v", store.items)
	}
	acknowledged, err := runtime.acknowledgeRepositoryWatchFailureAlert("watch-a", store.items[0].LastErrorFingerprint, now)
	if err != nil || acknowledged.FailureAlertedAt.IsZero() {
		t.Fatalf("acknowledge=%#v err=%v", acknowledged, err)
	}

	summaryFailure := repositoryWatchStageFailure(repositoryWatchFailureStageSummary, errors.New("GitHub API 503"))
	changed, err := runtime.finishRecurringReminder("watch-a", now, summaryFailure)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ConsecutiveFailures != 1 || changed.LastFailureStage != repositoryWatchFailureStageSummary || !changed.FailureAlertedAt.IsZero() || repositoryWatchFailureShouldAlert(changed) {
		t.Fatalf("changed fingerprint did not start a new sequence: %#v", changed)
	}
}

func TestRepositoryWatchFailureAlertRequiresAcknowledgementAndRedactsGroupMessage(t *testing.T) {
	now := time.Now()
	store := &stubReminderStore{items: []Reminder{{
		ID: "watch-redaction", Kind: ReminderKindRepositoryWatch, OwnerID: "owner", GroupID: "123", UserID: "owner",
		Repository: "acme/private", IntervalSeconds: 60,
	}}}
	channel := &scriptedChannel{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, store, nil, nil)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	raw := `request https://private.example/repo?signature=secret Authorization: Bearer owner-token`
	failure := repositoryWatchStageFailure(repositoryWatchFailureStagePolling, errors.New(raw))
	for attempt := 0; attempt < repositoryWatchFailureAlertThreshold; attempt++ {
		if _, err := runtime.finishRecurringReminder("watch-redaction", now, failure); err != nil {
			t.Fatal(err)
		}
	}
	item := store.items[0]
	if err := runtime.notifyRepositoryWatchFailure(context.Background(), item, failure); err == nil {
		t.Fatal("unacknowledged channel unexpectedly acknowledged alert")
	}
	if !store.items[0].FailureAlertedAt.IsZero() {
		t.Fatalf("unacknowledged alert persisted: %#v", store.items[0])
	}
	channel.mu.Lock()
	if len(channel.sent) != 1 {
		channel.mu.Unlock()
		t.Fatalf("alert attempts=%#v", channel.sent)
	}
	groupText := channel.sent[0].Text
	channel.mu.Unlock()
	for _, secret := range []string{"private.example", "signature=secret", "owner-token"} {
		if strings.Contains(groupText, secret) {
			t.Fatalf("group alert leaked %q: %q", secret, groupText)
		}
	}
	if !strings.Contains(groupText, "Authorization=[REDACTED]") {
		t.Fatalf("group alert lost redacted credential context: %q", groupText)
	}
	runtime.recordReminderRetryAttempt(item, failure, errors.New("alert unacknowledged"), true)
	entries := logs.entriesSnapshot()
	if len(entries) != 1 || !strings.Contains(entries[0].Detail, "owner-token") {
		t.Fatalf("restricted log did not retain diagnostic: %#v", entries)
	}
}

func TestRepositoryWatchRecoveryNoticeStaysPendingUntilAcknowledged(t *testing.T) {
	now := time.Now()
	store := &stubReminderStore{items: []Reminder{{
		ID: "watch-recovery-pending", Kind: ReminderKindRepositoryWatch, Repository: "acme/demo",
		IntervalSeconds: 60, FailureAlertedAt: now.Add(-time.Minute),
	}}}
	runtime := NewRuntime(BotConfig{}, &scriptedChannel{}, NewPluginManager(), nil, store, nil, nil)
	first, err := runtime.finishRecurringReminder("watch-recovery-pending", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.RecoveryNoticePending || !first.FailureAlertedAt.IsZero() {
		t.Fatalf("first recovery state=%#v", first)
	}
	second, err := runtime.finishRecurringReminder("watch-recovery-pending", now.Add(time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !second.RecoveryNoticePending {
		t.Fatalf("pending recovery notice was lost before acknowledgement: %#v", second)
	}
}
