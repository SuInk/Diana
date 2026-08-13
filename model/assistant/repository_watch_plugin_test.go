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

type repositoryWatchTestGitHub struct {
	mu           sync.Mutex
	commits      []map[string]any
	releases     []map[string]any
	token        string
	commitCalls  int
	releaseCalls int
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
	if strings.HasSuffix(r.URL.Path, "/releases") {
		s.releaseCalls++
		if s.failReleases {
			http.Error(w, `{"message":"release endpoint unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(s.releases)
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
		releases: []map[string]any{
			repositoryWatchReleasePayload("v1.1.0", "Second"),
			repositoryWatchReleasePayload("v1.0.0", "First"),
		},
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newRepositoryWatchPlugin(server.Client(), server.URL)
	store := &stubReminderStore{items: []Reminder{{
		ID: "watch-2", Kind: ReminderKindRepositoryWatch, OwnerID: "owner", GroupID: "123", UserID: "owner",
		Repository: "acme/demo", WatchCommits: true, WatchReleases: true,
		LastCommitSHA: "base-sha", LastReleaseTag: "v1.0.0",
		TriggerAt: time.Now().Add(-time.Minute), IntervalSeconds: 1800, CreatedAt: time.Now().Add(-time.Hour),
	}}}
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{"修复了投递，并发布 v1.1.0。"}}
	runtime := NewRuntime(BotConfig{RequestTimeout: 5 * time.Second}, channel, NewPluginManager(plugin), nil, store, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.fireDueReminders(context.Background())
	if len(channel.sent) != 1 || channel.sent[0].GroupID != "123" || !strings.Contains(channel.sent[0].Text, "v1.1.0") {
		t.Fatalf("sent=%#v", channel.sent)
	}
	item := store.items[0]
	if item.LastCommitSHA != "new-sha" || item.LastReleaseTag != "v1.1.0" || item.PendingDelivery != "" || item.ConsecutiveFailures != 0 {
		t.Fatalf("item=%#v", item)
	}
	if len(provider.requests) != 1 || !requestMessagesContain(provider.requests[0].Messages, "fix delivery") || !requestMessagesContain(provider.requests[0].Messages, "v1.1.0") {
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
