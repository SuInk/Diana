package assistant

import (
	"context"
	"encoding/json"
	"errors"
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
	runtime := NewRuntime(BotConfig{RequestTimeout: 5 * time.Second, SystemPrompt: "全局普通人设"}, channel, NewPluginManager(plugin), nil, store, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{
		"123": {GroupID: "123", SystemPrompt: "本群限定的自然人设"},
	}})
	runtime.fireDueReminders(context.Background())
	if len(channel.sent) != 1 || channel.sent[0].GroupID != "123" || !strings.Contains(channel.sent[0].Text, "v1.1.0") || strings.Contains(channel.sent[0].Text, "watch-2") {
		t.Fatalf("sent=%#v", channel.sent)
	}
	item := store.items[0]
	if item.LastCommitSHA != "new-sha" || item.LastReleaseTag != "v1.1.0" || item.PendingDelivery != "" || item.ConsecutiveFailures != 0 {
		t.Fatalf("item=%#v", item)
	}
	if len(provider.requests) != 1 || !requestMessagesContain(provider.requests[0].Messages, "fix delivery") || !requestMessagesContain(provider.requests[0].Messages, "v1.1.0") || !requestMessagesContain(provider.requests[0].Messages, "本群限定的自然人设") || requestMessagesContain(provider.requests[0].Messages, "watch-2") {
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
	if strings.Contains(alertText, "commit endpoint unavailable") {
		t.Fatalf("alert leaked upstream detail: %q", alertText)
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
	for _, secret := range []string{"private.example", "signature=secret", "owner-token", "Authorization"} {
		if strings.Contains(groupText, secret) {
			t.Fatalf("group alert leaked %q: %q", secret, groupText)
		}
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
