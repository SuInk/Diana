package assistant

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func rssTopicPoolGroupEvent(groupID string) MessageEvent {
	return MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, ProfileID: "qq", GroupID: groupID, UserID: "10001"}
}

func rssTopicPoolWatch(pool []rssTopicPoolItem) Reminder {
	return Reminder{
		ID: "pool-watch", Kind: ReminderKindRSSWatch, IntervalSeconds: 900, OwnerID: "owner",
		Platform: PlatformOneBotV11, ProfileID: "qq", GroupID: "100", UserID: "10001",
		FeedURL: "https://example.com/feed", FeedSource: "rss", FeedRuleMode: "all", FeedDelivery: rssDeliveryPool,
		NotificationTargetsJSON: encodeReminderDeliveryTargets([]ReminderDeliveryTarget{{Platform: PlatformOneBotV11, ProfileID: "qq", GroupID: "100"}}),
		FeedTopicPoolJSON:       encodeRSSTopicPool(pool),
	}
}

// 话题池订阅命中的条目不发群，存进池子，游标照常推进。
func TestRSSTopicPoolStoresHitsInsteadOfSending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<rss version="2.0"><channel><title>Tech</title>`+
			`<item><guid>new</guid><title>OpenAI 发布新模型</title><link>https://example.com/new</link><description>推理能力大幅提升</description></item>`+
			`<item><guid>old</guid><title>Old</title></item></channel></rss>`)
	}))
	defer server.Close()
	watch := rssTopicPoolWatch(nil)
	watch.FeedURL, watch.LastFeedItemID = server.URL, "old"
	tasks := &stubReminderStore{items: []Reminder{watch}}
	channel := &recordingChannel{}
	r := NewRuntime(BotConfig{ID: "qq", Platform: PlatformOneBotV11}, channel, NewPluginManager(NewRSSWatchPlugin(server.Client())), nil, tasks, nil, nil)
	if _, err := r.runClaimedRSSWatch(context.Background(), watch); err != nil {
		t.Fatal(err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("pool subscription pushed to group: %#v", sent)
	}
	saved := tasks.Reminders()[0]
	pool := decodeRSSTopicPool(saved.FeedTopicPoolJSON)
	if len(pool) != 1 || pool[0].Title != "OpenAI 发布新模型" || pool[0].URL != "https://example.com/new" || pool[0].Summary != "推理能力大幅提升" || pool[0].Source != "Tech" {
		t.Fatalf("pool = %#v", pool)
	}
	if saved.LastFeedItemID != "new" || strings.TrimSpace(saved.PendingDelivery) != "" {
		t.Fatalf("cursor=%q pending=%q", saved.LastFeedItemID, saved.PendingDelivery)
	}
	// 同一条再来一次不重复放。
	if err := r.addRSSTopicPoolEntries(saved, ReminderFeedSources(saved)[0], "Tech", []rssWatchItem{{Title: "OpenAI 发布新模型", Link: "https://example.com/new"}}); err != nil {
		t.Fatal(err)
	}
	if pool := decodeRSSTopicPool(tasks.Reminders()[0].FeedTopicPoolJSON); len(pool) != 1 {
		t.Fatalf("duplicate entry stored: %#v", pool)
	}
}

// 模型判断的订阅：只把判断结果点名的条目放进池子，没点名的不放。
func TestRSSTopicPoolKeepsOnlyJudgedEntries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<rss version="2.0"><channel><title>Tech</title>`+
			`<item><guid>new</guid><title>OpenAI 发布新模型</title><link>https://example.com/new</link></item>`+
			`<item><guid>other</guid><title>某公司换了新 logo</title><link>https://example.com/other</link></item>`+
			`<item><guid>old</guid><title>Old</title></item></channel></rss>`)
	}))
	defer server.Close()
	watch := rssTopicPoolWatch(nil)
	watch.FeedURL, watch.LastFeedItemID = server.URL, "old"
	watch.FeedRuleMode, watch.FeedJudgePrompt = "llm", "只要 AI 相关"
	tasks := &stubReminderStore{items: []Reminder{watch}}
	channel := &recordingChannel{}
	models := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "model", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "one", APIKey: "key"}}}}}
	r := NewRuntime(BotConfig{ID: "qq", Platform: PlatformOneBotV11}, channel, NewPluginManager(NewRSSWatchPlugin(server.Client())), models, tasks, nil, nil)
	r.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return &sharedJudgeProvider{}, nil })
	if _, err := r.runClaimedRSSWatch(context.Background(), watch); err != nil {
		t.Fatal(err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("pool subscription pushed to group: %#v", sent)
	}
	saved := tasks.Reminders()[0]
	if pool := decodeRSSTopicPool(saved.FeedTopicPoolJSON); len(pool) != 1 || pool[0].URL != "https://example.com/new" {
		t.Fatalf("pool = %#v", pool)
	}
	if saved.LastFeedItemID != "new" {
		t.Fatalf("cursor = %q", saved.LastFeedItemID)
	}
}

func rssTopicPoolSample(now time.Time) []rssTopicPoolItem {
	return []rssTopicPoolItem{
		{ID: "a", Source: "Tech", Title: "OpenAI 发布新模型", Summary: "推理能力大幅提升", CreatedAt: now.Unix() - 120},
		{ID: "b", Source: "Tech", Title: "Steam 秋季特卖开始", CreatedAt: now.Unix()},
		{ID: "c", Source: "Tech", Title: "某显卡降价", CreatedAt: now.Unix() - 60},
		{ID: "stale", Source: "Tech", Title: "OpenAI 旧闻", CreatedAt: now.Add(-rssTopicPoolTTL - time.Hour).Unix()},
	}
}

func rssTopicPoolRuntime(tasks *stubReminderStore) *Runtime {
	return NewRuntime(BotConfig{ID: "qq", Platform: PlatformOneBotV11}, &recordingChannel{}, NewDefaultPluginManager(), nil, tasks, nil, nil)
}

// 冷场找话题：只给这个群还没提过的最新两条，过期的不给，不是订阅目标的群不给；
// 标记已提只对这个群生效，标过的下次不再给。
func TestRSSTopicPoolColdStartOffersNewestUnshared(t *testing.T) {
	tasks := &stubReminderStore{items: []Reminder{rssTopicPoolWatch(rssTopicPoolSample(time.Now()))}}
	r := rssTopicPoolRuntime(tasks)
	group := rssTopicPoolGroupEvent("100")

	news := r.rssTopicPoolNews(group)
	if !news.fromTopicPool() || news.Results != "[Tech] Steam 秋季特卖开始\n[Tech] 某显卡降价" {
		t.Fatalf("news = %#v", news)
	}
	if other := r.rssTopicPoolNews(rssTopicPoolGroupEvent("200")); other.Results != "" {
		t.Fatalf("non-target group got pool news: %#v", other)
	}
	r.markRSSTopicPoolShared(group, news.topicPool)
	if next := r.rssTopicPoolNews(group); next.Results != "[Tech] OpenAI 发布新模型（推理能力大幅提升）" {
		t.Fatalf("after marking, news = %#v", next)
	}
	for _, item := range decodeRSSTopicPool(tasks.Reminders()[0].FeedTopicPoolJSON) {
		if want := item.ID == "b" || item.ID == "c"; item.sharedWith(messageEventDeliveryKey(group)) != want {
			t.Fatalf("shared marks = %#v", item)
		}
	}
}

// 冷场开口真发出去才标记已提；模型选择不开口时池子不动，下次还能用。
func TestRSSTopicPoolMarkedOnlyAfterInitiativeSent(t *testing.T) {
	for _, tc := range []struct {
		reply      string
		wantShared bool
	}{{"[SKIP]", false}, {"Steam 秋季特卖开始了，有人要剁手吗", true}} {
		runtime, channel, stub := newProactiveRuntime(t, tc.reply)
		now := time.Now()
		rememberWarmGroup(runtime, now)
		event := MessageEvent{Kind: EventKindGroup, MessageType: "group", GroupID: "10001", Time: now.Unix()}
		watch := rssTopicPoolWatch(rssTopicPoolSample(now))
		watch.NotificationTargetsJSON = encodeReminderDeliveryTargets([]ReminderDeliveryTarget{{Platform: event.Platform, ProfileID: event.ProfileID, GroupID: event.GroupID}})
		tasks := &stubReminderStore{items: []Reminder{watch}}
		runtime.reminders = tasks
		target := proactiveTarget{event: event, rules: groupRules()}
		plan, ok := runtime.proactivePlanFor(target, now)
		if !ok {
			t.Fatal("should be due")
		}
		runtime.runProactiveInitiative(context.Background(), target, plan)
		if dump := welcomeRequestDump(stub.lastRequest()); !strings.Contains(dump, "Steam 秋季特卖开始") || !strings.Contains(dump, "你订阅的信息源") {
			t.Fatalf("pool material missing from request:\n%s", dump)
		}
		if tc.wantShared != (len(channel.sent) == 1) {
			t.Fatalf("reply %q sent = %#v", tc.reply, channel.sent)
		}
		key := messageEventDeliveryKey(event)
		for _, item := range decodeRSSTopicPool(tasks.Reminders()[0].FeedTopicPoolJSON) {
			want := tc.wantShared && (item.ID == "b" || item.ID == "c")
			if item.sharedWith(key) != want {
				t.Fatalf("reply %q: shared marks = %#v", tc.reply, item)
			}
		}
	}
}

// 直接推送的订阅不参与话题池；取消的订阅也不再提供素材。
func TestRSSTopicPoolIgnoresPushAndCancelledWatches(t *testing.T) {
	pool := []rssTopicPoolItem{{ID: "a", Title: "OpenAI 发布新模型", CreatedAt: time.Now().Unix()}}
	push := rssTopicPoolWatch(pool)
	push.FeedDelivery = rssDeliveryPush
	cancelled := rssTopicPoolWatch(pool)
	cancelled.ID, cancelled.CancelledAt = "cancelled", time.Now()
	tasks := &stubReminderStore{items: []Reminder{push, cancelled}}
	r := rssTopicPoolRuntime(tasks)
	if got := r.rssTopicPoolCandidates(rssTopicPoolGroupEvent("100")); len(got) != 0 {
		t.Fatalf("candidates = %#v", got)
	}
}

func TestRSSTopicPoolPruneKeepsNewestWithinLimit(t *testing.T) {
	now := time.Now()
	var items []rssTopicPoolItem
	for i := range rssTopicPoolMaxItems + 5 {
		items = append(items, rssTopicPoolItem{ID: fmt.Sprint(i), Title: fmt.Sprint("t", i), CreatedAt: now.Unix() + int64(i)})
	}
	items = append([]rssTopicPoolItem{{ID: "stale", Title: "stale", CreatedAt: now.Add(-rssTopicPoolTTL - time.Minute).Unix()}}, items...)
	kept := pruneRSSTopicPool(items, now)
	if len(kept) != rssTopicPoolMaxItems || kept[0].ID != "5" || kept[len(kept)-1].ID != fmt.Sprint(rssTopicPoolMaxItems+4) {
		t.Fatalf("kept %d items, first=%s last=%s", len(kept), kept[0].ID, kept[len(kept)-1].ID)
	}
}

func TestNormalizeRSSDelivery(t *testing.T) {
	for raw, want := range map[string]string{"": rssDeliveryPush, " PUSH ": rssDeliveryPush, "pool": rssDeliveryPool} {
		if got, ok := normalizeRSSDelivery(raw); !ok || got != want {
			t.Fatalf("normalizeRSSDelivery(%q) = %q, %v", raw, got, ok)
		}
	}
	if _, ok := normalizeRSSDelivery("broadcast"); ok {
		t.Fatal("unknown delivery accepted")
	}
}
