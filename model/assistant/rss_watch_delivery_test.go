package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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
	if len(channel.sentSnapshot()) != 1 || len(store.items[0].PendingDeliveredTargets)-len(onlyStepKeys(store.items[0].PendingDeliveredTargets)) != 1 {
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

type rssStepFailureChannel struct {
	recordingChannel
	failImages bool
}

func (c *rssStepFailureChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if c.failImages && len(msg.ImageURLs) > 0 {
		return nil, errors.New("photo unavailable")
	}
	return c.recordingChannel.SendWithResult(ctx, msg)
}

// 文字已经发出去、图片被拒：补投只补图片，不再把文字发一遍。以前每 15 分钟重发
// 一遍同一段推文，群里刷屏。
func TestRSSWatchRetrySkipsDeliveredTelegramText(t *testing.T) {
	channel := &rssStepFailureChannel{failImages: true}
	item := Reminder{ID: "rss", Kind: ReminderKindRSSWatch, FeedURL: "https://example.com/feed", OwnerID: "owner",
		Platform: PlatformTelegram, ProfileID: "a", GroupID: "-10012345", IntervalSeconds: 900}
	store := &stubReminderStore{items: []Reminder{item}}
	r := NewRuntime(BotConfig{ID: "a", Platform: PlatformTelegram, SendRetryAttempts: 1}, channel, NewDefaultPluginManager(), nil, store, nil, nil)
	message := "New visual" + rssWatchReplyImages("x", []rssWatchItem{{Images: []string{"https://pbs.twimg.com/media/A.jpg"}}}) + "\n订阅 rss"
	if err := r.sendRSSWatchTargets(context.Background(), item, message); err == nil {
		t.Fatal("image failure was hidden")
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || !strings.Contains(sent[0].Text, "New visual") {
		t.Fatalf("first attempt = %#v", sent)
	}
	channel.failImages = false
	if err := r.sendRSSWatchTargets(context.Background(), store.items[0], message); err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 2 || sent[1].Text != "" || len(sent[1].ImageURLs) != 1 {
		t.Fatalf("retry resent delivered text: %#v", sent)
	}
	// 第三轮（例如目标级记录还没写上）也不会再发任何一段。
	if err := r.sendRSSWatchTargets(context.Background(), Reminder{ID: item.ID, Kind: item.Kind, OwnerID: item.OwnerID, Platform: item.Platform, ProfileID: item.ProfileID, GroupID: item.GroupID,
		PendingDeliveredTargets: onlyStepKeys(store.items[0].PendingDeliveredTargets)}, message); err != nil {
		t.Fatal(err)
	}
	if got := len(channel.sentSnapshot()); got != 2 {
		t.Fatalf("delivered steps were resent: %d sends", got)
	}
}

func onlyStepKeys(keys []string) []string {
	var out []string
	for _, key := range keys {
		if strings.Contains(key, "#") {
			out = append(out, key)
		}
	}
	return out
}

// 同一份推送投三次都失败就丢弃，下一轮照常抓新条目，不再无限补投。
func TestRSSWatchPendingDroppedAfterThreeFailedAttempts(t *testing.T) {
	item := Reminder{ID: "rss", Kind: ReminderKindRSSWatch, FeedURL: "https://example.com/feed", OwnerID: "owner", UserID: "100", IntervalSeconds: 900,
		PendingDelivery: "stuck", PendingDeliveredTargets: []string{"a#chunk:0:x|text"}}
	store := &stubReminderStore{items: []Reminder{item}}
	r := NewRuntime(BotConfig{ID: "a"}, &recordingChannel{}, NewDefaultPluginManager(), nil, store, nil, nil)
	for attempt := 1; attempt <= 2; attempt++ {
		updated, err := r.finishRecurringReminder(item.ID, time.Now(), errors.New("send failed"))
		if err != nil {
			t.Fatal(err)
		}
		if updated.PendingDelivery != "stuck" || updated.PendingDeliveryAttempts != attempt {
			t.Fatalf("attempt %d dropped early: %#v", attempt, updated)
		}
	}
	updated, err := r.finishRecurringReminder(item.ID, time.Now(), errors.New("send failed"))
	if err != nil {
		t.Fatal(err)
	}
	if updated.PendingDelivery != "" || updated.PendingDeliveredTargets != nil || updated.PendingDeliveryAttempts != 0 {
		t.Fatalf("pending kept after 3 attempts: %#v", updated)
	}
	if updated.ConsecutiveFailures != 3 || !strings.Contains(updated.LastError, "已放弃") {
		t.Fatalf("failure state = %d %q", updated.ConsecutiveFailures, updated.LastError)
	}
}
