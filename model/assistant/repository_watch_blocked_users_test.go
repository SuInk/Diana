package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRepositoryWatchBlockedUsersConfiguration(t *testing.T) {
	tool, store := repositoryWatchChatToolFixture(t, MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "manager"}, false)
	update := func(users []any) dianaRepositoryWatchResult {
		return runRepositoryWatchChatTool(t, tool, map[string]any{"operation": "update", "id": "watch-demo", "blocked_users": users})
	}
	got := update([]any{" @Alice ", "alice", "dependabot[bot]"})
	if got.Watch == nil || !slices.Equal(got.Watch.BlockedUsers, []string{"alice", "dependabot[bot]"}) {
		t.Fatalf("normalized setting = %#v", got)
	}
	for _, item := range store.items {
		if item.ID == "watch-demo" && !slices.Equal(item.BlockedUsers, got.Watch.BlockedUsers) {
			t.Fatalf("setting not persisted: %#v", item.BlockedUsers)
		}
		if item.ID == "watch-secret" && len(item.BlockedUsers) != 0 {
			t.Fatal("changed a different subscription")
		}
	}
	unchanged := runRepositoryWatchChatTool(t, tool, map[string]any{"operation": "update", "id": "watch-demo", "interval": "2m"})
	if !slices.Equal(unchanged.Watch.BlockedUsers, got.Watch.BlockedUsers) {
		t.Fatal("omitted setting was erased")
	}
	for _, bad := range []any{[]any{"https://github.com/alice"}, []any{"*"}, 42} {
		if _, err := tool.Run(context.Background(), map[string]any{"operation": "update", "id": "watch-demo", "blocked_users": bad}); err == nil {
			t.Fatalf("accepted invalid setting: %v", bad)
		}
	}
	if cleared := update([]any{}); len(cleared.Watch.BlockedUsers) != 0 {
		t.Fatal("empty list did not clear setting")
	}
}

func TestRepositoryWatchBlockedUsersAdvanceCursors(t *testing.T) {
	old, fresh := "2026-08-13T00:00:00Z", "2026-08-14T00:00:00Z"
	issue := repositoryWatchIssuePayload(2, "blocked issue", "open", "", fresh, fresh, "")
	pull := repositoryWatchPullPayload(2, "blocked pull", "open", "", fresh)
	release := repositoryWatchReleasePayload("v1.1.0", "blocked release")
	release["author"] = map[string]any{"login": "DIANA"}
	github := &repositoryWatchTestGitHub{
		commits: []map[string]any{repositoryWatchCommitPayload("new", "blocked commit"), repositoryWatchCommitPayload("old", "old")},
		issues:  []map[string]any{issue}, pullRequests: []map[string]any{pull}, releases: []map[string]any{release},
		starCount: 11, events: repositoryWatchStarEvents("old", "diana"),
	}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newTestRepositoryWatchPlugin(server.Client(), server.URL)
	at, _ := time.Parse(time.RFC3339, old)
	cursor := repositoryWatchSnapshot{CommitSHA: "old", IssueCursor: old + "#1", PullRequestCursor: old + "#1", ReleaseTag: "v1.0.0", ReleaseID: 1, ReleasePublishedAt: at, StarCount: 10, HasStarCount: true, StarEventID: "eold"}
	selection := repositoryWatchSelection{Commits: true, PullRequests: true, Issues: true, Releases: true, Stars: true, Diff: true, BlockedUsers: []string{"diana"}}
	change, err := plugin.checkSelected(context.Background(), "acme/demo", "", cursor, selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	change = applyRepositoryStarNotifyThreshold(Reminder{WatchStars: true, LastStarCount: 10, LastNotifiedStarCount: 10, StarNotifyThreshold: 1}, change)
	if len(change.Commits)+len(change.Issues)+len(change.PullRequests)+len(change.Releases) != 0 || change.Stars != nil {
		t.Fatalf("blocked event escaped: %#v", change)
	}
	if change.Snapshot.CommitSHA != "new" || change.Snapshot.IssueCursor != fresh+"#2" || change.Snapshot.PullRequestCursor != fresh+"#2" || change.Snapshot.ReleaseID != 2 || change.Snapshot.StarEventID != "ediana" || change.Snapshot.StarNotifiedCount != 11 {
		t.Fatalf("cursors not advanced: %#v", change.Snapshot)
	}
	if github.pullCommitCalls != 0 || github.diffCalls != 0 || github.pullFileCalls != 0 {
		t.Fatal("fetched blocked content for follow-up")
	}
	selection.BlockedUsers = nil
	replay, err := plugin.checkSelected(context.Background(), "acme/demo", "", change.Snapshot, selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Commits)+len(replay.Issues)+len(replay.PullRequests)+len(replay.Releases) != 0 || replay.Stars != nil {
		t.Fatalf("unblocking replayed events: %#v", replay)
	}
}

func TestRepositoryWatchBlockedStarsDoNotCountTowardThreshold(t *testing.T) {
	github := &repositoryWatchTestGitHub{starCount: 12, events: repositoryWatchStarEvents("old", "blocked", "allowed")}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newTestRepositoryWatchPlugin(server.Client(), server.URL)
	change, err := plugin.checkSelected(context.Background(), "acme/demo", "", repositoryWatchSnapshot{StarCount: 10, StarEventID: "eold", HasStarCount: true}, repositoryWatchSelection{Stars: true, BlockedUsers: []string{"BLOCKED"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	item := Reminder{WatchStars: true, LastStarCount: 10, LastNotifiedStarCount: 10, StarNotifyThreshold: 2}
	if got := applyRepositoryStarNotifyThreshold(item, change); got.Stars != nil || got.Snapshot.StarNotifiedCount != 11 {
		t.Fatalf("blocked star contributed to threshold: %#v", got)
	}
	item.StarNotifyThreshold = 1
	got := applyRepositoryStarNotifyThreshold(item, change)
	if got.Stars == nil || got.Stars.Delta != 1 || got.Stars.Current != 12 || !slices.Equal(stargazerLogins(got.Stars.AddedUsers), []string{"allowed"}) {
		t.Fatalf("mixed stars = %#v", got.Stars)
	}
	if strings.Contains(renderRepositoryWatchChanges(got), "blocked") {
		t.Fatal("blocked user leaked into rendered notification")
	}
}

func TestRepositoryWatchBlockedIssueDoesNotConsumeSummaryLimit(t *testing.T) {
	old, fresh := "2026-08-13T00:00:00Z", "2026-08-14T00:00:00Z"
	blocked := repositoryWatchIssuePayload(3, "blocked", "open", "", fresh, fresh, "")
	allowed := repositoryWatchIssuePayload(2, "allowed", "open", "", fresh, fresh, "")
	allowed["user"] = map[string]any{"login": "other"}
	github := &repositoryWatchTestGitHub{issues: []map[string]any{blocked, allowed}}
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()
	plugin := newTestRepositoryWatchPlugin(server.Client(), server.URL)
	change, err := plugin.checkSelected(context.Background(), "acme/demo", "", repositoryWatchSnapshot{IssueCursor: old + "#1"}, repositoryWatchSelection{Issues: true, BlockedUsers: []string{"diana"}}, SettingValues{repositoryWatchSettingLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(change.Issues) != 1 || change.Issues[0].Number != 2 || change.Snapshot.IssueCursor != fresh+"#3" {
		t.Fatalf("mixed issues = %#v", change)
	}
}

func TestRepositoryWatchBlockedUsersRejectStalePoll(t *testing.T) {
	previous := repositoryWatchSnapshot{}
	snapshot := repositoryWatchSnapshot{previous: &previous, repository: "acme/demo"}
	if err := validateRepositoryWatchProgress(Reminder{Repository: "acme/demo", BlockedUsers: []string{"alice"}}, snapshot); err == nil {
		t.Fatal("old poll bypassed changed blocklist")
	}
}

func TestRepositoryWatchLookupUserHint(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/users/alice" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"Alice","name":"Alice Example"}`))
	}))
	defer server.Close()
	plugin := newTestRepositoryWatchPlugin(server.Client(), server.URL)
	got := plugin.LookupUser(context.Background(), "@ALICE", nil)
	if got == nil || got.Login != "Alice" || got.Name != "Alice Example" {
		t.Fatalf("profile hint = %#v", got)
	}
	if got := plugin.LookupUser(context.Background(), "../repos/acme/demo", nil); got != nil || calls != 1 {
		t.Fatal("invalid username reached network")
	}
}
