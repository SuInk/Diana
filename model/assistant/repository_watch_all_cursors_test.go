package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type repositoryCursorFixture struct {
	mu        sync.Mutex
	responses map[string]any
	statuses  map[string]int
}

func newRepositoryCursorFixture(t *testing.T) (*repositoryCursorFixture, *RepositoryWatchPlugin) {
	t.Helper()
	f := &repositoryCursorFixture{responses: map[string]any{}, statuses: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		key := r.URL.RequestURI()
		value, found := f.responses[key]
		if !found {
			key = r.URL.Path
			value, found = f.responses[key]
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if code := f.statuses[key]; code != 0 {
			w.WriteHeader(code)
		}
		_ = json.NewEncoder(w).Encode(value)
	}))
	t.Cleanup(server.Close)
	return f, newRepositoryWatchPlugin(server.Client(), server.URL)
}

func (f *repositoryCursorFixture) set(path string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[path] = value
}

func TestRepositoryCommitCursorRejectsBackwardAndUnverifiedHistory(t *testing.T) {
	f, p := newRepositoryCursorFixture(t)
	const list = "/repos/acme/demo/commits"
	f.set(list, []any{repositoryWatchCommitPayload("base", "base"), repositoryWatchCommitPayload("old", "old")})
	_, initial, _, err := p.fetchCommits(context.Background(), "acme/demo", "", "", nil)
	if err != nil || initial != "base" {
		t.Fatalf("initial=%s err=%v", initial, err)
	}
	for _, empty := range []any{nil, []any{}} {
		f.set(list, empty)
		got, next, _, err := p.fetchCommits(context.Background(), "acme/demo", "", initial, nil)
		if err != nil || len(got) != 0 || next != initial {
			t.Fatalf("empty reset: %v %s %v", got, next, err)
		}
	}
	f.set(list, []any{repositoryWatchCommitPayload("old", "old")})
	for _, status := range []string{"behind", "diverged", "identical", ""} {
		f.set("/repos/acme/demo/compare/base...old", map[string]any{"status": status})
		got, next, _, err := p.fetchCommits(context.Background(), "acme/demo", "", initial, nil)
		if err != nil || len(got) != 0 || next != initial {
			t.Fatalf("%s reset: %v %s %v", status, got, next, err)
		}
	}
	f.set(list, []any{repositoryWatchCommitPayload("head", "new"), repositoryWatchCommitPayload("old-visible", "old ancestor")})
	f.set("/repos/acme/demo/compare/base...head", map[string]any{"status": "ahead", "total_commits": 2, "commits": []any{repositoryWatchCommitPayload("delta", "addition"), repositoryWatchCommitPayload("head", "new")}})
	got, next, truncated, err := p.fetchCommits(context.Background(), "acme/demo", "", initial, nil)
	if err != nil || next != "head" || truncated || len(got) != 2 || got[0].SHA != "head" || got[1].SHA != "delta" {
		t.Fatalf("verified range=%v next=%s err=%v", got, next, err)
	}
	f.set(list, []any{repositoryWatchCommitPayload("head", "new"), repositoryWatchCommitPayload("base", "base")})
	got, next, _, err = p.fetchCommits(context.Background(), "acme/demo", "", "head", nil)
	if err != nil || next != "head" || len(got) != 0 {
		t.Fatalf("replayed: %v %s %v", got, next, err)
	}
	f.set(list, []any{repositoryWatchCommitPayload("unknown", "unknown")})
	if _, next, _, err := p.fetchCommits(context.Background(), "acme/demo", "", "head", nil); err == nil || next != "head" {
		t.Fatalf("unverifiable cursor=%s err=%v", next, err)
	}
	f.set(list, nil)
	if _, _, _, err := p.fetchCommits(context.Background(), "acme/demo", "", "", nil); err == nil {
		t.Fatal("empty repository must not invent an initial commit")
	}
}

func TestRepositoryCommitCursorUsesNewestComparePages(t *testing.T) {
	f, p := newRepositoryCursorFixture(t)
	f.set("/repos/acme/demo/commits", []any{repositoryWatchCommitPayload("c205", "latest")})
	page := func(first, last int) map[string]any {
		var commits []any
		for i := first; i <= last; i++ {
			commits = append(commits, repositoryWatchCommitPayload(fmt.Sprintf("c%d", i), "new"))
		}
		return map[string]any{"status": "ahead", "total_commits": 205, "commits": commits}
	}
	path := "/repos/acme/demo/compare/base...c205?per_page=100"
	f.set(path, page(1, 100))
	f.set(path+"&page=3", page(201, 205))
	f.set(path+"&page=2", page(101, 200))
	got, next, truncated, err := p.fetchCommits(context.Background(), "acme/demo", "", "base", SettingValues{repositoryWatchSettingLimit: 10})
	if err != nil || len(got) != 10 || next != "c205" || !truncated {
		t.Fatalf("got=%v next=%s truncated=%v err=%v", got, next, truncated, err)
	}
	for i, item := range got {
		if item.SHA != fmt.Sprintf("c%d", 205-i) {
			t.Fatalf("wrong page order: %v", got)
		}
	}
}

func TestRepositoryReleaseCursorSurvivesEmptyOlderAndDeletedAnchor(t *testing.T) {
	f, p := newRepositoryCursorFixture(t)
	path := "/repos/acme/demo/releases"
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	release := func(tag string, id int, day int) map[string]any {
		return map[string]any{"tag_name": tag, "id": id, "published_at": at.AddDate(0, 0, day), "name": tag}
	}
	old, current := release("v8", 8, 0), release("v9", 9, 1)
	f.set(path, []any{old, current})
	got, cursor, err := p.fetchReleases(context.Background(), "acme/demo", repositoryWatchSnapshot{}, nil)
	if err != nil || len(got) != 0 || cursor.ReleaseTag != "v9" || cursor.ReleaseID != 9 {
		t.Fatalf("initial=%v %#v %v", got, cursor, err)
	}
	for _, response := range []any{nil, []any{}, []any{map[string]any{"draft": true}}, []any{old}} {
		f.set(path, response)
		got, next, err := p.fetchReleases(context.Background(), "acme/demo", cursor, nil)
		if err != nil || len(got) != 0 || next.ReleaseTag != cursor.ReleaseTag || !next.ReleasePublishedAt.Equal(cursor.ReleasePublishedAt) {
			t.Fatalf("regressed=%v %#v %v", got, next, err)
		}
		f.set(path, []any{current, old})
		if got, _, err := p.fetchReleases(context.Background(), "acme/demo", next, nil); err != nil || len(got) != 0 {
			t.Fatalf("replayed=%v %v", got, err)
		}
	}
	// A backport or previously drafted release may have a lower tag and ID.
	fresh := release("v1-backport", 3, 2)
	f.set(path, []any{old, fresh})
	got, cursor, err = p.fetchReleases(context.Background(), "acme/demo", cursor, nil)
	if err != nil || len(got) != 1 || got[0].Tag != "v1-backport" || cursor.ReleaseTag != "v1-backport" {
		t.Fatalf("fresh=%v %#v %v", got, cursor, err)
	}
	f.set(path, []any{release("other", 10, 2), fresh})
	got, cursor, err = p.fetchReleases(context.Background(), "acme/demo", cursor, nil)
	if err != nil || len(got) != 1 || got[0].Tag != "other" || cursor.ReleaseID != 10 {
		t.Fatalf("same time=%v %#v %v", got, cursor, err)
	}
	f.set(path, []any{release("other", 11, 2)})
	got, cursor, err = p.fetchReleases(context.Background(), "acme/demo", cursor, nil)
	if err != nil || len(got) != 1 || cursor.ReleaseID != 11 {
		t.Fatalf("recreated release identity lost: %v %#v %v", got, cursor, err)
	}
	// Metadata survives persistence even after the anchor is removed upstream.
	item := Reminder{ID: "r", Kind: ReminderKindRepositoryWatch, Repository: "acme/demo", IntervalSeconds: 60, WatchReleases: true}
	applyRepositoryReleaseCursor(&item, cursor)
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var restored Reminder
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.LastReleaseID != cursor.ReleaseID || !restored.LastReleasePublishedAt.Equal(cursor.ReleasePublishedAt) {
		t.Fatal("release metadata lost after reload")
	}
	f.set(path, []any{old})
	_, next, err := p.fetchReleases(context.Background(), "acme/demo", repositoryWatchSnapshot{ReleaseTag: restored.LastReleaseTag, ReleasePublishedAt: restored.LastReleasePublishedAt, ReleaseID: restored.LastReleaseID}, nil)
	if err != nil || next.ReleaseTag != restored.LastReleaseTag {
		t.Fatalf("deleted anchor reset: %#v %v", next, err)
	}
}

func TestRepositoryReleaseCursorMigratesLegacyTagAndKeepsFirstRelease(t *testing.T) {
	f, p := newRepositoryCursorFixture(t)
	path := "/repos/acme/demo/releases"
	old := repositoryWatchReleasePayload("v1.0.0", "old")
	newer := repositoryWatchReleasePayload("v1.1.0", "new")
	f.set(path, []any{newer, old})
	got, cursor, err := p.fetchReleases(context.Background(), "acme/demo", repositoryWatchSnapshot{ReleaseTag: "v1.1.0"}, nil)
	if err != nil || len(got) != 0 || cursor.ReleasePublishedAt.IsZero() {
		t.Fatalf("legacy=%v %#v %v", got, cursor, err)
	}
	partial := cursor
	partial.ReleaseID = 0
	got, repaired, err := p.fetchReleases(context.Background(), "acme/demo", partial, nil)
	if err != nil || len(got) != 0 || repaired.ReleaseID != cursor.ReleaseID {
		t.Fatalf("learning a missing release ID replayed history: %v %#v %v", got, repaired, err)
	}
	f.set(path, []any{old})
	f.set(path+"/tags/v1.1.0", newer)
	got, cursor, err = p.fetchReleases(context.Background(), "acme/demo", repositoryWatchSnapshot{ReleaseTag: "v1.1.0"}, nil)
	if err != nil || len(got) != 0 || cursor.ReleaseTag != "v1.1.0" {
		t.Fatalf("lookup=%v %#v %v", got, cursor, err)
	}
	if _, kept, err := p.fetchReleases(context.Background(), "acme/demo", repositoryWatchSnapshot{ReleaseTag: "deleted"}, nil); err == nil || kept.ReleaseTag != "deleted" {
		t.Fatal("unverifiable legacy tag was reset")
	}
	f.set(path, nil)
	_, empty, err := p.fetchReleases(context.Background(), "acme/demo", repositoryWatchSnapshot{}, nil)
	if err != nil || empty.ReleaseTag != repositoryWatchNoReleaseCursor {
		t.Fatal("initial empty changed")
	}
	f.set(path, []any{old})
	if got, _, err := p.fetchReleases(context.Background(), "acme/demo", empty, nil); err != nil || len(got) != 1 {
		t.Fatalf("first release lost: %v %v", got, err)
	}
}

func TestRepositoryStarCursorSurvivesEmptyOlderUnorderedAndSameTimeEvents(t *testing.T) {
	f, p := newRepositoryCursorFixture(t)
	path := "/repos/acme/demo/events"
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	event := func(id string, hour int) map[string]any {
		return repositoryWatchStarEventPayload(id, "u"+id, base.Add(time.Duration(hour)*time.Hour))
	}
	f.set("/repos/acme/demo", map[string]any{"stargazers_count": 10})
	f.set(path, []any{event("100", 1), event("99", 0)})
	change, state, err := p.fetchStars(context.Background(), "acme/demo", repositoryWatchSnapshot{}, nil)
	if err != nil || change != nil || state.EventID != "100" {
		t.Fatalf("initial=%v %#v %v", change, state, err)
	}
	cursor := repositoryWatchSnapshot{StarCount: 10, HasStarCount: true, StarEventID: state.EventID, StarEventAt: state.EventAt}
	for _, response := range []any{nil, []any{}, []any{map[string]any{"type": "PushEvent"}}, []any{event("99", 0)}} {
		f.set(path, response)
		change, state, err = p.fetchStars(context.Background(), "acme/demo", cursor, nil)
		if err != nil || change != nil || state.EventID != cursor.StarEventID || !state.EventAt.Equal(cursor.StarEventAt) {
			t.Fatalf("reset=%v %#v %v", change, state, err)
		}
	}
	f.set(path, []any{event("99", 0), event("102", 2), event("100", 1), event("101", 2), event("102", 2)})
	change, state, err = p.fetchStars(context.Background(), "acme/demo", cursor, nil)
	if err != nil || change == nil || change.Delta != 2 || state.EventID != "102" || !reflect.DeepEqual(stargazerLogins(change.AddedUsers), []string{"u101", "u102"}) {
		t.Fatalf("new=%v %#v %v", change, state, err)
	}
	cursor.StarEventID, cursor.StarEventAt = "102", state.EventAt
	f.set(path, []any{event("103", 2), event("102", 2)})
	change, state, err = p.fetchStars(context.Background(), "acme/demo", cursor, nil)
	if err != nil || change == nil || change.Delta != 1 || state.EventID != "103" {
		t.Fatalf("same-time=%v %#v %v", change, state, err)
	}
	cursor.StarEventID, cursor.StarEventAt = "103", state.EventAt
	f.set("/repos/acme/demo", map[string]any{"stargazers_count": 8})
	f.set(path, nil)
	change, state, err = p.fetchStars(context.Background(), "acme/demo", cursor, nil)
	if err != nil || change != nil || state.Count != 8 || state.EventID != "103" {
		t.Fatalf("unstar changed event watermark: %v %#v %v", change, state, err)
	}
}

func TestRepositoryAllCursorStalePollCannotOverwritePendingDelivery(t *testing.T) {
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	base := Reminder{ID: "watch", Kind: ReminderKindRepositoryWatch, Repository: "acme/demo", IntervalSeconds: 60, WatchCommits: true, WatchPullRequests: true, WatchIssues: true, WatchReleases: true, WatchStars: true, LastCommitSHA: "base", LastPullRequestCursor: "2026-08-01T00:00:00Z#1", LastIssueCursor: "2026-08-01T00:00:00Z#1", LastReleaseTag: "v1", LastReleasePublishedAt: at, LastReleaseID: 1, LastStarEventID: "100", LastStarEventAt: at, PendingDelivery: "keep newer notification"}
	previous := repositoryWatchSnapshot{CommitSHA: base.LastCommitSHA, PullRequestCursor: base.LastPullRequestCursor, IssueCursor: base.LastIssueCursor, ReleaseTag: base.LastReleaseTag, ReleasePublishedAt: at, ReleaseID: 1, StarEventID: "100", StarEventAt: at}
	snapshot := repositoryWatchSnapshot{previous: &previous, repository: base.Repository, selection: repositoryWatchSelection{Commits: true, PullRequests: true, Issues: true, Releases: true, Stars: true}, CommitSHA: "candidate", PullRequestCursor: "2026-08-02T00:00:00Z#2", IssueCursor: "2026-08-02T00:00:00Z#2", ReleaseTag: "v2", ReleasePublishedAt: at.Add(time.Hour), ReleaseID: 2, HasStarCount: true, StarEventID: "101", StarEventAt: at.Add(time.Hour)}
	for name, mutate := range map[string]func(*Reminder){
		"commit":     func(r *Reminder) { r.LastCommitSHA = "newest" },
		"pull":       func(r *Reminder) { r.LastPullRequestCursor = "2026-08-03T00:00:00Z#3" },
		"issue":      func(r *Reminder) { r.LastIssueCursor = "2026-08-03T00:00:00Z#3" },
		"release":    func(r *Reminder) { r.LastReleaseTag = "v3"; r.LastReleasePublishedAt = at.Add(2 * time.Hour) },
		"star":       func(r *Reminder) { r.LastStarEventID = "103"; r.LastStarEventAt = at.Add(2 * time.Hour) },
		"repository": func(r *Reminder) { r.Repository = "acme/other" },
		"selection":  func(r *Reminder) { r.WatchIssues = false },
		"cancelled":  func(r *Reminder) { r.CancelledAt = at },
	} {
		t.Run(name, func(t *testing.T) {
			item := base
			mutate(&item)
			store := &stubReminderStore{items: []Reminder{item}}
			runtime := &Runtime{reminders: store}
			if err := runtime.storeRepositoryWatchProgress(item.ID, snapshot, "stale notification", ""); err == nil {
				t.Fatal("stale result accepted")
			}
			if !reflect.DeepEqual(store.Reminders()[0], item) {
				t.Fatal("stale result changed state or pending delivery")
			}
		})
	}
	store := &stubReminderStore{items: []Reminder{base}}
	runtime := &Runtime{reminders: store}
	if err := runtime.storeRepositoryWatchProgress(base.ID, snapshot, "fresh notification", ""); err != nil {
		t.Fatal(err)
	}
	item := store.Reminders()[0]
	if item.LastCommitSHA != "candidate" || item.LastReleaseID != 2 || item.LastStarEventID != "101" || item.PendingDelivery != "fresh notification" {
		t.Fatal("valid forward snapshot was not saved")
	}
	if err := runtime.storeRepositoryWatchProgress(base.ID, snapshot, "duplicate", ""); err == nil || !strings.Contains(err.Error(), "旧轮询") {
		t.Fatalf("old snapshot accepted twice: %v", err)
	}
}
