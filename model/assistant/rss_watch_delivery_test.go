package assistant

import (
	"context"
	"errors"
	"testing"
)

type rssTargetFailureChannel struct {
	recordingChannel
	fail bool
}

func (c *rssTargetFailureChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if c.fail && msg.ProfileID == "b" {
		return nil, errors.New("target b unavailable")
	}
	return c.recordingChannel.SendWithResult(ctx, msg)
}

func TestRSSWatchRetriesOnlyFailedTargets(t *testing.T) {
	channel := &rssTargetFailureChannel{fail: true}
	item := Reminder{ID: "rss", Kind: ReminderKindRSSWatch, FeedURL: "https://example.com/feed", OwnerID: "owner", ProfileID: "a", UserID: "owner", IntervalSeconds: 900,
		NotificationTargetsJSON: encodeReminderDeliveryTargets([]ReminderDeliveryTarget{
			{Platform: PlatformOneBotV11, ProfileID: "a", UserID: "100"},
			{Platform: PlatformOneBotV11, ProfileID: "b", UserID: "100"},
			{Platform: PlatformOneBotV11, ProfileID: "a", UserID: "100"},
		})}
	store := &stubReminderStore{items: []Reminder{item}}
	r := NewRuntime(BotConfig{ID: "a", SendRetryAttempts: 1}, channel, NewDefaultPluginManager(), nil, store, nil, nil)
	r.profileConfigs["b"] = BotConfig{ID: "b", Platform: PlatformOneBotV11, SendRetryAttempts: 1}
	err := r.sendRSSWatchTargets(context.Background(), item, "new item")
	if err == nil {
		t.Fatal("target failure was hidden")
	}
	if len(channel.sentSnapshot()) != 1 || len(store.items[0].PendingDeliveredTargets) != 1 {
		t.Fatalf("successful target missing: sent=%v completed=%v err=%v", channel.sentSnapshot(), store.items[0].PendingDeliveredTargets, err)
	}
	channel.fail = false
	if err := r.sendRSSWatchTargets(context.Background(), store.items[0], "new item"); err != nil {
		t.Fatal(err)
	}
	if got := channel.sentSnapshot(); len(got) != 2 || got[0].ProfileID != "a" || got[1].ProfileID != "b" {
		t.Fatalf("retry resent completed target: %#v", got)
	}
}
