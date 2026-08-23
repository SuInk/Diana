// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type mutableFeed struct {
	mu   sync.Mutex
	body string
	hits int
}

func (f *mutableFeed) serve(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits++
	w.Header().Set("Content-Type", "application/rss+xml")
	_, _ = w.Write([]byte(f.body))
}

func (f *mutableFeed) set(body string) {
	f.mu.Lock()
	f.body = body
	f.mu.Unlock()
}

func rssTestBody(items ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>Tibo updates</title>` + strings.Join(items, "") + `</channel></rss>`
}

func rssTestItem(id, title, content, published string) string {
	return fmt.Sprintf(`<item><guid>%s</guid><title>%s</title><description>%s</description><link>https://x.com/tibo/status/%s</link><pubDate>%s</pubDate></item>`, id, title, content, id, published)
}

func TestParseRSSAndAtom(t *testing.T) {
	rss, err := parseRSSOrAtom([]byte(rssTestBody(
		rssTestItem("2", "Reset &amp; ready", "<b>quota restored</b>", "Fri, 14 Aug 2026 11:00:00 +0800"),
		rssTestItem("1", "Earlier", "old", "Fri, 14 Aug 2026 10:00:00 +0800"),
	)))
	if err != nil {
		t.Fatal(err)
	}
	if rss.Title != "Tibo updates" || len(rss.Items) != 2 || rss.Items[0].ID != "2" || rss.Items[0].Content != "quota restored" {
		t.Fatalf("rss=%#v", rss)
	}
	atom := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>News</title><entry><id>a1</id><title>Hello</title><content>Body</content><published>2026-08-14T03:00:00Z</published><link rel="alternate" href="https://example.com/a1"/></entry></feed>`
	parsed, err := parseRSSOrAtom([]byte(atom))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Title != "News" || len(parsed.Items) != 1 || parsed.Items[0].Link != "https://example.com/a1" {
		t.Fatalf("atom=%#v", parsed)
	}
}

func TestTwitterFeedURLUsesConfigurableTemplate(t *testing.T) {
	settings := SettingValues{rssWatchSettingTwitterTemplate: "https://rss.example.test/x/{handle}/feed"}
	got, err := twitterFeedURL("https://x.com/Tibo", settings)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://rss.example.test/x/Tibo/feed" {
		t.Fatalf("url=%q", got)
	}
	if _, err := twitterFeedURL("bad/name", settings); err == nil {
		t.Fatal("invalid handle should fail")
	}
}

func TestRSSWatchBaselineAndChangeOrdering(t *testing.T) {
	feed := &mutableFeed{body: rssTestBody(rssTestItem("1", "Initial", "old", "Fri, 14 Aug 2026 10:00:00 +0800"))}
	server := httptest.NewServer(http.HandlerFunc(feed.serve))
	defer server.Close()
	plugin := NewRSSWatchPlugin(server.Client())
	baseline, _, err := plugin.snapshot(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	feed.set(rssTestBody(
		rssTestItem("3", "Newest", "reset", "Fri, 14 Aug 2026 12:00:00 +0800"),
		rssTestItem("2", "Second", "pending", "Fri, 14 Aug 2026 11:00:00 +0800"),
		rssTestItem("1", "Initial", "old", "Fri, 14 Aug 2026 10:00:00 +0800"),
	))
	change, err := plugin.check(context.Background(), server.URL, baseline.ItemID, baseline.PublishedAt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if change.Snapshot.ItemID != "3" || len(change.Items) != 2 || change.Items[0].ID != "2" || change.Items[1].ID != "3" {
		t.Fatalf("change=%#v", change)
	}
}

func TestRuntimeCreatesRSSWatchWithCurrentFeedAsBaseline(t *testing.T) {
	feed := &mutableFeed{body: rssTestBody(rssTestItem("base", "Initial", "old", "Fri, 14 Aug 2026 10:00:00 +0800"))}
	server := httptest.NewServer(http.HandlerFunc(feed.serve))
	defer server.Close()
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(NewRSSWatchPlugin(server.Client())), nil, store, nil, nil)
	item, err := runtime.CreateRSSWatch(context.Background(), RSSWatchCreateInput{FeedURL: server.URL, JudgePrompt: "只在重置额度时通知", UserID: "owner", OwnerID: "owner", Interval: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != ReminderKindRSSWatch || item.LastFeedItemID != "base" || len(store.items) != 1 || !item.TriggerAt.After(time.Now()) {
		t.Fatalf("item=%#v stored=%#v", item, store.items)
	}
}

func TestRuntimeRSSWatchStaysSilentWhenJudgeRejects(t *testing.T) {
	feed := &mutableFeed{body: rssTestBody(
		rssTestItem("new", "General update", "nothing about quota", "Fri, 14 Aug 2026 11:00:00 +0800"),
		rssTestItem("base", "Initial", "old", "Fri, 14 Aug 2026 10:00:00 +0800"),
	)}
	server := httptest.NewServer(http.HandlerFunc(feed.serve))
	defer server.Close()
	store := &stubReminderStore{items: []Reminder{{ID: "rss-1", Kind: ReminderKindRSSWatch, OwnerID: "owner", UserID: "owner", FeedURL: server.URL, FeedSource: "twitter", FeedHandle: "tibo", FeedJudgePrompt: "只在重置额度时通知", LastFeedItemID: "base", LastFeedPublishedAt: time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC), TriggerAt: time.Now().Add(-time.Second), IntervalSeconds: 900}}}
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{`{"notify":false,"reply":""}`}}
	runtime := NewRuntime(BotConfig{RequestTimeout: 5 * time.Second}, channel, NewPluginManager(NewRSSWatchPlugin(server.Client())), nil, store, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.fireDueReminders(context.Background())
	if len(channel.sent) != 0 || len(provider.requestsSnapshot()) != 1 || store.items[0].LastFeedItemID != "new" || store.items[0].ConsecutiveFailures != 0 {
		t.Fatalf("sent=%#v requests=%d item=%#v", channel.sent, len(provider.requestsSnapshot()), store.items[0])
	}
}

func TestRuntimeRSSWatchNotifiesAndRetriesPendingReply(t *testing.T) {
	feed := &mutableFeed{body: rssTestBody(
		rssTestItem("new", "Quota reset", "Limits reset at 12:00 UTC", "Fri, 14 Aug 2026 11:00:00 +0800"),
		rssTestItem("base", "Initial", "old", "Fri, 14 Aug 2026 10:00:00 +0800"),
	)}
	server := httptest.NewServer(http.HandlerFunc(feed.serve))
	defer server.Close()
	store := &stubReminderStore{items: []Reminder{{ID: "rss-2", Kind: ReminderKindRSSWatch, OwnerID: "owner", UserID: "owner", GroupID: "group-1", FeedURL: server.URL, FeedSource: "twitter", FeedHandle: "tibo", FeedJudgePrompt: "重置额度时通知", LastFeedItemID: "base", LastFeedPublishedAt: time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC), TriggerAt: time.Now().Add(-time.Second), IntervalSeconds: 900}}}
	channel := &scriptedChannel{sendErrs: []error{errors.New("send failed"), nil, nil}}
	provider := &sequenceLLMProvider{replies: []string{`{"notify":true,"reply":"Tibo 表示额度会在 12:00 UTC 重置：https://x.com/tibo/status/new"}`}}
	runtime := NewRuntime(BotConfig{RequestTimeout: 5 * time.Second}, channel, NewPluginManager(NewRSSWatchPlugin(server.Client())), nil, store, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.fireDueReminders(context.Background())
	if len(provider.requestsSnapshot()) != 1 || store.items[0].PendingDelivery == "" || store.items[0].LastFeedItemID != "new" || store.items[0].ConsecutiveFailures != 1 {
		t.Fatalf("requests=%d item=%#v", len(provider.requestsSnapshot()), store.items[0])
	}
	store.items[0].TriggerAt = time.Now().Add(-time.Second)
	runtime.fireDueReminders(context.Background())
	if len(provider.requestsSnapshot()) != 1 || store.items[0].PendingDelivery != "" || store.items[0].ConsecutiveFailures != 0 {
		t.Fatalf("requests=%d item=%#v", len(provider.requestsSnapshot()), store.items[0])
	}
	feed.mu.Lock()
	hits := feed.hits
	feed.mu.Unlock()
	if hits != 1 {
		t.Fatalf("feed hits=%d, want 1", hits)
	}
}

func TestRSSJudgeDecisionRequiresNotify(t *testing.T) {
	if _, err := parseRSSJudgeDecision(`{"reply":"看起来有更新"}`); err == nil || !strings.Contains(err.Error(), "notify") {
		t.Fatalf("error=%v", err)
	}
	decision, err := parseRSSJudgeDecision("```json\n{\"notify\":false,\"reply\":\"不应发送\"}\n```")
	if err != nil || decision.Notify || decision.Reply != "" {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
}

// 光报一个「HTTP 404」没法行动：同样是 404，可能是用户名打错了，也可能是整条
// 路由已经下线。公共 RSSHub 的 X/Twitter 路由属于后者——它把所有请求 302 到
// google.com/404，换用户名和重试都不会生效，报错必须说出这件事。
func TestFeedFetchStatusErrorExplainsRetiredRoute(t *testing.T) {
	redirected := func(finalURL string) *http.Response {
		parsed, err := url.Parse(finalURL)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusNotFound, Request: &http.Request{URL: parsed}}
	}

	err := feedFetchStatusError("https://rsshub.app/twitter/user/someone", redirected("https://google.com/404"))
	if err == nil || !strings.Contains(err.Error(), "公共 RSSHub") || !strings.Contains(err.Error(), "Twitter RSS 模板") {
		t.Fatalf("public RSSHub 404 should name the retired route and the fix: %v", err)
	}

	err = feedFetchStatusError("https://rss.example.com/twitter/user/someone", redirected("https://elsewhere.example/404"))
	if err == nil || !strings.Contains(err.Error(), "已被上游下线") {
		t.Fatalf("off-host redirect should be reported as a retired route: %v", err)
	}

	sameHost, _ := url.Parse("https://rss.example.com/feed.xml")
	err = feedFetchStatusError("https://rss.example.com/feed.xml", &http.Response{StatusCode: http.StatusNotFound, Request: &http.Request{URL: sameHost}})
	if err == nil || !strings.Contains(err.Error(), "确认用户名或 Feed 地址") {
		t.Fatalf("plain 404 should suggest checking the address: %v", err)
	}

	err = feedFetchStatusError("https://rss.example.com/feed.xml", &http.Response{StatusCode: http.StatusInternalServerError, Request: &http.Request{URL: sameHost}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || strings.Contains(err.Error(), "确认用户名") {
		t.Fatalf("non-404 status should stay plain: %v", err)
	}
}

// Twitter 订阅默认直接读 X 公开时间线，不需要任何额外部署；填了模板才走模板。
func TestTwitterFeedURLDefaultsToPublicTimeline(t *testing.T) {
	for _, input := range []string{"someone", "@someone", "https://x.com/someone", "https://twitter.com/someone/"} {
		got, err := twitterFeedURL(input, SettingValues{})
		if err != nil {
			t.Fatalf("twitterFeedURL(%q) error = %v", input, err)
		}
		if got != "https://api.fxtwitter.com/2/profile/someone/statuses" {
			t.Fatalf("twitterFeedURL(%q) = %q", input, got)
		}
	}
	got, err := twitterFeedURL("someone", SettingValues{rssWatchSettingTwitterTemplate: "https://rss.example.com/twitter/user/{handle}"})
	if err != nil || got != "https://rss.example.com/twitter/user/someone" {
		t.Fatalf("configured template = %q, err = %v", got, err)
	}
}

// 时间线响应转 feed 条目：raw_text 在上游有字符串和对象两种形态，都要认。
func TestParseTwitterStatusesFeed(t *testing.T) {
	body := []byte(`{"code":200,"results":[
		{"type":"status","id":"2","url":"https://x.com/a/status/2","text":"第二条","created_timestamp":1787482674,"author":{"name":"Alice","screen_name":"alice"}},
		{"type":"status","id":"1","url":"https://x.com/b/status/1","raw_text":{"text":"转推正文","facets":[]},"created_timestamp":1787482000,"author":{"name":"","screen_name":"bob"}},
		{"type":"status","id":"","text":"没有 id 的要丢掉"}
	]}`)
	feed, err := parseTwitterStatusesFeed(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Items) != 2 {
		t.Fatalf("items = %#v", feed.Items)
	}
	if feed.Items[0].ID != "2" || feed.Items[0].Content != "第二条" || feed.Items[0].Author != "Alice" {
		t.Fatalf("first item = %#v", feed.Items[0])
	}
	if feed.Items[0].PublishedAt.Unix() != 1787482674 {
		t.Fatalf("published = %v", feed.Items[0].PublishedAt)
	}
	// raw_text 是对象时也要取出正文；作者名为空时回退到 @handle。
	if feed.Items[1].Content != "转推正文" || feed.Items[1].Author != "@bob" {
		t.Fatalf("second item = %#v", feed.Items[1])
	}
}

// 标题必须是订阅的那个账号。时间线里第一条常常是转推，取条目作者会让订阅
// @OpenAI 显示成被转推者的名字。
func TestTwitterStatusesFeedTitleTracksSubscribedHandle(t *testing.T) {
	if got := twitterHandleFromStatusesURL("https://api.fxtwitter.com/2/profile/OpenAI/statuses"); got != "OpenAI" {
		t.Fatalf("handle = %q", got)
	}
	if got := twitterHandleFromStatusesURL("https://rss.example.com/twitter/user/OpenAI"); got != "" {
		t.Fatalf("custom template should not be parsed as a profile path: %q", got)
	}
}

// JSON 与 XML 按内容分流，自建的 FxTwitter 兼容实例也能直接用。
func TestLooksLikeJSONDocument(t *testing.T) {
	if !looksLikeJSONDocument([]byte("\n  {\"code\":200}")) {
		t.Fatal("leading whitespace should still be detected as JSON")
	}
	if looksLikeJSONDocument([]byte(`<?xml version="1.0"?><rss></rss>`)) {
		t.Fatal("XML must not be treated as JSON")
	}
}
