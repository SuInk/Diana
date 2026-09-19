package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func rssOwnerToolFixture() (*Runtime, *stubReminderStore) {
	store := &stubReminderStore{items: []Reminder{
		{ID: "personal", Kind: ReminderKindRSSWatch, OwnerID: "owner", UserID: "owner", FeedURL: "https://example.com/owner.xml", IntervalSeconds: 900},
		{ID: "other", Kind: ReminderKindRSSWatch, OwnerID: "member", UserID: "member", FeedURL: "https://example.com/member.xml", IntervalSeconds: 900},
		{ID: "webui", Kind: ReminderKindRSSWatch, OwnerID: "webui:profile", GroupID: "100", FeedURL: "https://example.com/webui.xml", IntervalSeconds: 900},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	return runtime, store
}

func runRSSOwnerTool(t *testing.T, tool *dianaRSSWatchTool, input map[string]any) dianaRSSWatchResult {
	t.Helper()
	raw, err := tool.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var result dianaRSSWatchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return result
}

func TestRSSWatchOwnerListsAndManagesAllSubscriptions(t *testing.T) {
	runtime, store := rssOwnerToolFixture()
	tool := newDianaRSSWatchTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "owner"})

	listed := runRSSOwnerTool(t, tool, map[string]any{"operation": "list"})
	if len(listed.Items) != 3 {
		t.Fatalf("owner listed %d subscriptions, want 3", len(listed.Items))
	}
	owners := map[string]bool{}
	for _, item := range listed.Items {
		owners[item.OwnerID] = true
	}
	for _, owner := range []string{"owner", "member", "webui:profile"} {
		if !owners[owner] {
			t.Fatalf("owner list omitted %q: %#v", owner, listed.Items)
		}
	}

	cancelled := runRSSOwnerTool(t, tool, map[string]any{"operation": "cancel", "id": "webui"})
	if cancelled.Watch == nil || cancelled.Watch.OwnerID != "webui:profile" || cancelled.Watch.Status != "cancelled" {
		t.Fatalf("cancelled = %#v", cancelled)
	}
	if store.items[2].CancelledAt.IsZero() || time.Since(store.items[2].CancelledAt) > time.Minute {
		t.Fatalf("webui subscription was not cancelled: %#v", store.items[2])
	}

	deleted := runRSSOwnerTool(t, tool, map[string]any{"operation": "delete", "id": "other"})
	if !deleted.OK || len(store.items) != 2 {
		t.Fatalf("delete result=%#v items=%#v", deleted, store.items)
	}
}

func TestRSSWatchRegularUserRemainsOwnerScoped(t *testing.T) {
	runtime, _ := rssOwnerToolFixture()
	tool := newDianaRSSWatchTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "member"})

	listed := runRSSOwnerTool(t, tool, map[string]any{"operation": "list"})
	if len(listed.Items) != 1 || listed.Items[0].ID != "other" {
		t.Fatalf("member list = %#v", listed.Items)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "cancel", "id": "webui"}); err == nil || !strings.Contains(err.Error(), "没有找到属于目标用户") {
		t.Fatalf("member cancel error = %v", err)
	}
}
