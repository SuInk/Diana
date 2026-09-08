package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestRepositoryWatchCursorOnlyAdvances(t *testing.T) {
	const current = "2026-07-31T01:18:28Z#40"
	for _, tt := range []struct{ name, current, candidate, want string }{
		{"initial empty", "", "__none__", "__none__"},
		{"initial snapshot", "", current, current},
		{"first item", "__none__", current, current},
		{"empty response", current, "__none__", current},
		{"missing snapshot", current, "", current},
		{"older time", current, "2026-07-30T01:18:28Z#100", current},
		{"older number", current, "2026-07-31T01:18:28Z#39", current},
		{"same cursor", current, current, current},
		{"newer number", current, "2026-07-31T01:18:28Z#41", "2026-07-31T01:18:28Z#41"},
		{"newer time", current, "2026-08-01T01:18:28Z#1", "2026-08-01T01:18:28Z#1"},
		{"same instant", current, "2026-07-31T09:18:28+08:00#40", current},
		{"bad time", current, "invalid#99", current},
		{"bad number", current, "2026-08-01T01:18:28Z#41junk", current},
		{"invalid item", current, "2026-08-01T01:18:28Z#0", current},
		{"missing timestamp", current, "0#100", current},
		{"legacy untimed cursor", "0#40", "0#39", "0#40"},
		{"legacy cursor advances", "0#40", current, current},
		{"repair bad checkpoint", "invalid", current, current},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := advanceRepositoryWatchCursor(tt.current, tt.candidate); got != tt.want {
				t.Fatalf("advance(%q, %q)=%q, want %q", tt.current, tt.candidate, got, tt.want)
			}
		})
	}
}

func TestRepositoryWatchIssueAndPullCursorsSurviveEmptyAndStaleResponses(t *testing.T) {
	for _, kind := range []string{"issue", "pull_request"} {
		t.Run(kind, func(t *testing.T) {
			var mu sync.Mutex
			var response []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				mu.Lock()
				defer mu.Unlock()
				if strings.HasSuffix(r.URL.Path, "/commits") {
					_ = json.NewEncoder(w).Encode([]any{})
					return
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			plugin := newRepositoryWatchPlugin(server.Client(), server.URL)
			selection := repositoryWatchSelection{Issues: true, PullRequests: true}
			settings := SettingValues{repositoryWatchSettingLimit: 5}
			entry := func(number int, updatedAt string) map[string]any {
				return map[string]any{
					"number": number, "title": "historical record", "state": "closed",
					"created_at": "2025-01-01T00:00:00Z", "updated_at": updatedAt,
					"closed_at": "2025-09-17T02:04:37Z", "base": map[string]any{"ref": "main"},
				}
			}
			old := []map[string]any{
				entry(40, "2026-07-31T01:18:28Z"), entry(39, "2026-07-06T03:14:48Z"),
				entry(36, "2026-06-15T00:59:57Z"), entry(16, "2026-06-10T03:52:11Z"),
				entry(14, "2026-06-04T02:51:22Z"), entry(11, "2026-06-04T02:51:13Z"),
			}
			poll := func(items []map[string]any, cursor string) ([]int, string) {
				t.Helper()
				mu.Lock()
				response = items
				mu.Unlock()
				var numbers []int
				var next string
				var err error
				if kind == "issue" {
					var found []repositoryWatchIssue
					found, next, err = plugin.fetchIssues(context.Background(), "acme/demo", cursor, selection, settings)
					for _, item := range found {
						numbers = append(numbers, item.Number)
					}
				} else {
					var found []repositoryWatchPullRequest
					found, next, err = plugin.fetchPullRequests(context.Background(), "acme/demo", "main", cursor, selection, settings)
					for _, item := range found {
						numbers = append(numbers, item.Number)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				return numbers, next
			}
			const baseline = "2026-07-31T01:18:28Z#40"
			if got, cursor := poll(old, ""); len(got) != 0 || cursor != baseline {
				t.Fatalf("initial snapshot replayed history: %v %q", got, cursor)
			}
			filteredOut := entry(99, "2026-08-01T00:00:00Z")
			if kind == "issue" {
				filteredOut["pull_request"] = map[string]any{"url": "https://example.com/pull/99"}
			} else {
				filteredOut["base"] = map[string]any{"ref": "other"}
			}
			cursor := baseline
			for _, response := range [][]map[string]any{{}, nil, {filteredOut}, old[1:]} {
				got, next := poll(response, cursor)
				if len(got) != 0 || next != baseline {
					t.Fatalf("empty/stale response moved checkpoint: %v %q", got, next)
				}
				got, cursor = poll(old, next)
				if len(got) != 0 || cursor != baseline {
					t.Fatalf("recovery replayed historical records: %v %q", got, cursor)
				}
			}
			// A response need not be ordered; same-time IDs choose the greatest watermark.
			fresh := []map[string]any{old[1], entry(41, "2026-08-01T00:00:00Z"), old[0], entry(42, "2026-08-01T00:00:00Z")}
			got, cursor := poll(fresh, cursor)
			if !reflect.DeepEqual(got, []int{42, 41}) || cursor != "2026-08-01T00:00:00Z#42" {
				t.Fatalf("new records or maximum checkpoint lost: %v %q", got, cursor)
			}
			if got, next := poll(fresh, cursor); len(got) != 0 || next != cursor {
				t.Fatalf("unchanged records replayed: %v %q", got, next)
			}
			if got, next := poll(old, "broken#checkpoint"); len(got) != 0 || next != baseline {
				t.Fatalf("invalid checkpoint repair replayed history: %v %q", got, next)
			}
			if got, next := poll(nil, ""); len(got) != 0 || next != repositoryWatchNoIssueCursor {
				t.Fatalf("initial empty repository changed: %v %q", got, next)
			}
			if got, _ := poll(fresh[1:2], repositoryWatchNoIssueCursor); !reflect.DeepEqual(got, []int{41}) {
				t.Fatalf("first real record was swallowed: %v", got)
			}
		})
	}
}

func TestRepositoryWatchProgressCannotPersistOlderIssueOrPullCursors(t *testing.T) {
	const baseline = "2026-07-31T01:18:28Z#40"
	store := &stubReminderStore{items: []Reminder{{
		ID: "watch", Kind: ReminderKindRepositoryWatch, Repository: "acme/demo", IntervalSeconds: 60,
		WatchIssues: true, WatchPullRequests: true, LastIssueCursor: baseline, LastPullRequestCursor: baseline,
	}}}
	runtime := &Runtime{reminders: store}
	for _, candidate := range []string{"__none__", "", "broken", "2026-07-30T00:00:00Z#50", "2026-07-31T01:18:28Z#39"} {
		if err := runtime.storeRepositoryWatchProgress("watch", repositoryWatchSnapshot{IssueCursor: candidate, PullRequestCursor: candidate}, "", ""); err != nil {
			t.Fatal(err)
		}
		item := store.Reminders()[0]
		if item.LastIssueCursor != baseline || item.LastPullRequestCursor != baseline {
			t.Fatalf("persisted regressed checkpoint from %q: %#v", candidate, item)
		}
	}
	const newer = "2026-08-01T00:00:00Z#41"
	if err := runtime.storeRepositoryWatchProgress("watch", repositoryWatchSnapshot{IssueCursor: newer, PullRequestCursor: newer}, "new notification", "reference"); err != nil {
		t.Fatal(err)
	}
	item := store.Reminders()[0]
	if item.LastIssueCursor != newer || item.LastPullRequestCursor != newer || item.PendingDelivery != "new notification" {
		t.Fatalf("forward progress or pending delivery lost: %#v", item)
	}
}
