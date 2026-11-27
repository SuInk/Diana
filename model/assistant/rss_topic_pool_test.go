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

// 聊到相关话题才带进上下文；只带给订阅目标群；提过之后对这个群不再带。
func TestRSSTopicPoolContextMatchesTargetGroupAndMarksShared(t *testing.T) {
	now := time.Now()
	pool := []rssTopicPoolItem{
		{ID: "a", Source: "Tech", Title: "OpenAI 发布新模型", Summary: "推理能力大幅提升", CreatedAt: now.Unix()},
		{ID: "b", Source: "Tech", Title: "Steam 秋季特卖开始", CreatedAt: now.Unix()},
		{ID: "stale", Source: "Tech", Title: "OpenAI 旧闻", CreatedAt: now.Add(-rssTopicPoolTTL - time.Hour).Unix()},
	}
	tasks := &stubReminderStore{items: []Reminder{rssTopicPoolWatch(pool)}}
	r := NewRuntime(BotConfig{ID: "qq", Platform: PlatformOneBotV11}, &recordingChannel{}, NewDefaultPluginManager(), nil, tasks, nil, nil)
	group := rssTopicPoolGroupEvent("100")

	got := r.rssTopicPoolContext(group, "你们看到 openai 新模型了吗")
	if !strings.HasPrefix(got, rssTopicPoolContextHeader) || !strings.Contains(got, "OpenAI 发布新模型") || strings.Contains(got, "Steam") || strings.Contains(got, "旧闻") {
		t.Fatalf("context = %q", got)
	}
	if got := r.rssTopicPoolContext(group, "今天天气不错"); got != "" {
		t.Fatalf("unrelated chat got pool context: %q", got)
	}
	if got := r.rssTopicPoolContext(rssTopicPoolGroupEvent("200"), "你们看到 openai 新模型了吗"); got != "" {
		t.Fatalf("non-target group got pool context: %q", got)
	}

	r.markRSSTopicPoolShared(group, "刚看到 OpenAI 发布新模型，说是推理强了不少")
	if got := r.rssTopicPoolContext(group, "你们看到 openai 新模型了吗"); got != "" {
		t.Fatalf("shared item still offered: %q", got)
	}
	saved := decodeRSSTopicPool(tasks.Reminders()[0].FeedTopicPoolJSON)
	if !saved[0].sharedWith(messageEventDeliveryKey(group)) || saved[1].sharedWith(messageEventDeliveryKey(group)) {
		t.Fatalf("shared marks = %#v", saved)
	}

	// 冷场找话题时没有沾边的也按新旧挑剩下的。
	news := r.rssTopicPoolNews(group, []string{"甲：在吗"})
	if !news.FromTopicPool || !strings.Contains(news.Results, "Steam 秋季特卖开始") || strings.Contains(news.Results, "OpenAI") {
		t.Fatalf("cold start news = %#v", news)
	}
	if news := r.rssTopicPoolNews(rssTopicPoolGroupEvent("200"), []string{"甲：在吗"}); news.Results != "" {
		t.Fatalf("non-target group got cold start news: %#v", news)
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
	r := NewRuntime(BotConfig{ID: "qq", Platform: PlatformOneBotV11}, &recordingChannel{}, NewDefaultPluginManager(), nil, tasks, nil, nil)
	if got := r.rssTopicPoolContext(rssTopicPoolGroupEvent("100"), "openai 新模型"); got != "" {
		t.Fatalf("context = %q", got)
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
