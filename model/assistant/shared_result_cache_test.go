package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestSharedResultCacheCoalescesAndSeparatesCancellation(t *testing.T) {
	var cache sharedResultCache[[]string]
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	load := func(ctx context.Context) ([]string, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return []string{"original"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := cache.load(ctx, "key", time.Minute, time.Second, nil, load); first <- err }()
	<-started
	second := make(chan []string, 1)
	go func() {
		value, err := cache.load(context.Background(), "key", time.Minute, time.Second, nil, load)
		if err != nil {
			t.Error(err)
		}
		second <- value
	}()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	value := <-second
	value[0] = "mutated"
	again, err := cache.load(context.Background(), "key", time.Minute, time.Second, nil, load)
	if err != nil || again[0] != "original" || calls.Load() != 1 {
		t.Fatalf("shared result corrupted: %v %v calls=%d", again, err, calls.Load())
	}
}

func TestSharedResultCacheRetriesErrorsAndExpires(t *testing.T) {
	var cache sharedResultCache[string]
	calls := 0
	load := func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("temporary failure")
		}
		return "ok", nil
	}
	if _, err := cache.load(context.Background(), "key", time.Minute, time.Second, nil, load); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := cache.load(context.Background(), "key", time.Minute, time.Second, nil, load); err != nil || calls != 2 {
		t.Fatal("failure was cached")
	}
	cache.mu.Lock()
	cache.entries["key"].expires = time.Now().Add(-time.Second)
	cache.mu.Unlock()
	cache.load(context.Background(), "key", time.Minute, time.Second, nil, load)
	if calls != 3 {
		t.Fatal("expired entry reused")
	}
	for i := 0; i < sharedCacheMaxEntries+3; i++ {
		cache.load(context.Background(), fmt.Sprint(i), time.Minute, time.Second, nil, load)
	}
	if len(cache.entries) > sharedCacheMaxEntries || cache.bytes > sharedCacheMaxBytes {
		t.Fatal("cache unbounded")
	}
}

func TestRSSFetchSharedButCursorsRemainIndependent(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `<rss version="2.0"><channel><title>Updates</title><item><guid>new</guid><title>New</title></item><item><guid>old</guid><title>Old</title></item></channel></rss>`)
	}))
	defer server.Close()
	p := NewRSSWatchPlugin(server.Client())
	a, err := p.check(context.Background(), server.URL, "old", time.Time{}, SettingValues{rssWatchSettingItemLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.check(context.Background(), server.URL, "", time.Time{}, SettingValues{rssWatchSettingItemLimit: 12})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(a.Items) != 1 || a.Items[0].ID != "new" || len(b.Items) != 2 || b.Items[0].ID != "old" {
		t.Fatalf("fetches=%d a=%+v b=%+v", calls.Load(), a, b)
	}
	a.Items[0].Title = "mutated"
	clean, _ := p.check(context.Background(), server.URL, "old", time.Time{}, nil)
	if clean.Items[0].Title != "New" {
		t.Fatal("cursor filtering mutated shared feed")
	}
	p.snapshot(context.Background(), server.URL, nil)
	if calls.Load() != 2 {
		t.Fatal("new subscription used stale baseline")
	}
}

func TestSocialResultsReuseAcrossPlatformsOnlyWithMatchingCredentials(t *testing.T) {
	p := NewResolverPlugin(nil)
	var calls atomic.Int32
	p.twitterPostFetcher = func(context.Context, string) (twitterPost, bool) {
		calls.Add(1)
		return twitterPost{ID: "1234567890", Text: "shared text", AuthorName: "Author", AuthorHandle: "author"}, true
	}
	requests := []PluginRequest{{Event: MessageEvent{ProfileID: "qq", Platform: PlatformOneBotV11}}, {Event: MessageEvent{ProfileID: "tg", Platform: PlatformTelegram}}}
	for _, req := range requests {
		ctx := withResolverCredentials(context.Background(), resolverCredentialsFromSettings(req.Settings))
		result := p.resolveSocialMedia(ctx, req, "https://x.com/author/status/1234567890", 9, time.Minute)
		if !result.Handled || !strings.Contains(result.Context, "shared text") {
			t.Fatalf("result=%+v", result)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("cross-platform fetches=%d", calls.Load())
	}
	requests[1].Settings = SettingValues{resolverSettingDouyinCookie: "different-private-context"}
	ctx := withResolverCredentials(context.Background(), resolverCredentialsFromSettings(requests[1].Settings))
	p.resolveSocialMedia(ctx, requests[1], "https://x.com/author/status/1234567890", 9, time.Minute)
	if calls.Load() != 2 {
		t.Fatal("different credentials shared")
	}
}

func TestSharedSocialMediaInvalidatesDeletedFilesAndChangedCookieFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video.mp4")
	os.WriteFile(path, []byte("media"), 0600)
	var cache sharedResultCache[resolverSocialResult]
	calls := 0
	load := func(context.Context) (resolverSocialResult, error) {
		calls++
		return resolverSocialResult{Handled: true, Context: "text", ResourceKeys: []string{"video"}, VideoURLs: []string{path}}, nil
	}
	cache.load(context.Background(), "key", time.Minute, time.Second, validSharedSocialResult, load)
	os.Remove(path)
	cache.load(context.Background(), "key", time.Minute, time.Second, validSharedSocialResult, load)
	if calls != 2 {
		t.Fatal("deleted media reused")
	}
	os.WriteFile(path, []byte("cookie-one"), 0600)
	first, _ := resolverCookieFileFingerprint(path)
	os.WriteFile(path, []byte("cookie-two"), 0600)
	second, _ := resolverCookieFileFingerprint(path)
	if first == second {
		t.Fatal("changed cookie file retained identity")
	}
}

type sharedJudgeProvider struct{ calls atomic.Int32 }

func (p *sharedJudgeProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls.Add(1)
	return &llm.GenerateResponse{Text: `{"notify":true,"reply":"new content"}`}, nil
}

func TestRSSJudgmentReuseHonorsRuleMaterialAndModel(t *testing.T) {
	p := &sharedJudgeProvider{}
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "model", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "one", APIKey: "key"}}}}}
	r := NewRuntime(BotConfig{}, &recordingChannel{}, NewDefaultPluginManager(), store, nil, nil, nil)
	r.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return p, nil })
	a := Reminder{ProfileID: "qq", Platform: PlatformOneBotV11, FeedJudgePrompt: "only new updates"}
	b := a
	b.ProfileID = "tg"
	b.Platform = PlatformTelegram
	change := rssWatchChange{FeedURL: "https://example.com/feed", Items: []rssWatchItem{{ID: "1", Content: "new"}}}
	var wg sync.WaitGroup
	for _, item := range []Reminder{a, b} {
		wg.Add(1)
		go func(item Reminder) {
			defer wg.Done()
			if _, err := r.judgeRSSWatch(context.Background(), item, change); err != nil {
				t.Error(err)
			}
		}(item)
	}
	wg.Wait()
	if p.calls.Load() != 1 {
		t.Fatalf("same judgment generated %d times", p.calls.Load())
	}
	b.FeedJudgePrompt = "a different rule"
	r.judgeRSSWatch(context.Background(), b, change)
	change.Items[0].Content = "updated"
	r.judgeRSSWatch(context.Background(), b, change)
	store.set.Profiles[0].Config.Model = "two"
	r.judgeRSSWatch(context.Background(), b, change)
	store.set.Profiles[0].Config.APIKey = "different-key"
	r.judgeRSSWatch(context.Background(), b, change)
	if p.calls.Load() != 5 {
		t.Fatalf("changed inputs reused judgment: %d", p.calls.Load())
	}
}

func TestRSSSharedWorkStillDeliversToBothRobots(t *testing.T) {
	var fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fetches.Add(1)
		fmt.Fprint(w, `<rss version="2.0"><channel><title>Updates</title><item><guid>new</guid><title>New</title></item><item><guid>old</guid><title>Old</title></item></channel></rss>`)
	}))
	defer server.Close()
	provider := &sharedJudgeProvider{}
	models := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "model", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "one", APIKey: "key"}}}}}
	a := Reminder{ID: "qq-watch", Kind: ReminderKindRSSWatch, IntervalSeconds: 300, ProfileID: "qq", Platform: PlatformOneBotV11, GroupID: "100", UserID: "10001", FeedURL: server.URL, FeedSource: "rss", LastFeedItemID: "old", FeedJudgePrompt: "Notify about new entries"}
	b := a
	b.ID = "tg-watch"
	b.ProfileID = "tg"
	b.Platform = PlatformTelegram
	tasks := &stubReminderStore{items: []Reminder{a, b}}
	qq, tg := &recordingChannel{}, &recordingChannel{}
	channels := NewMultiChannel([]ChannelBinding{{ProfileID: "qq", Platform: PlatformOneBotV11, Channel: qq}, {ProfileID: "tg", Platform: PlatformTelegram, Channel: tg}})
	r := NewRuntime(BotConfig{ID: "qq", BotAccount: "42"}, channels, NewPluginManager(NewRSSWatchPlugin(server.Client())), models, tasks, nil, nil)
	r.SetProfiles(ProfileSet{ActiveID: "qq", Profiles: []BotConfig{{ID: "qq", Platform: PlatformOneBotV11, BotAccount: "42"}, {ID: "tg", Platform: PlatformTelegram, BotAccount: "43"}}})
	r.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return provider, nil })
	for _, item := range []Reminder{a, b} {
		if _, err := r.runClaimedRSSWatch(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	if fetches.Load() != 1 || provider.calls.Load() != 1 {
		t.Fatalf("fetches=%d judgments=%d", fetches.Load(), provider.calls.Load())
	}
	if len(qq.sentSnapshot()) != 1 || len(tg.sentSnapshot()) != 1 {
		t.Fatal("one subscriber lost its delivery")
	}
	if !strings.Contains(qq.sentSnapshot()[0].Text, a.ID) || !strings.Contains(tg.sentSnapshot()[0].Text, b.ID) {
		t.Fatal("subscriber delivery identities crossed")
	}
	for _, item := range tasks.items {
		if item.LastFeedItemID != "new" {
			t.Fatalf("cursor for %s did not advance independently", item.ID)
		}
	}
}

func TestPublicPageReuseSeparatesParametersAndRetriesFailure(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprintf(w, "<html><head><title>%s</title></head></html>", req.UserAgent())
	}))
	defer server.Close()
	p := NewResolverPlugin(server.Client())
	opts := resolveOptions{cacheTTL: time.Minute, httpTimeout: time.Second, userAgent: "first-agent"}
	if _, ok := p.fetchPageMeta(context.Background(), server.URL, opts); ok {
		t.Fatal("failure reported as success")
	}
	fail.Store(false)
	first, ok := p.fetchPageMeta(context.Background(), server.URL, opts)
	if !ok || first.Title != "first-agent" {
		t.Fatal("failure cached")
	}
	p.fetchPageMeta(context.Background(), server.URL, opts)
	if calls.Load() != 2 {
		t.Fatal("page cache missed")
	}
	opts.userAgent = "second-agent"
	second, _ := p.fetchPageMeta(context.Background(), server.URL, opts)
	if calls.Load() != 3 || second.Title != "second-agent" {
		t.Fatal("different parser parameters reused")
	}
}

func TestFreshBaselineInvalidatesOlderInFlightFeed(t *testing.T) {
	var cache sharedResultCache[string]
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := cache.load(context.Background(), "feed", time.Minute, time.Second, nil, func(context.Context) (string, error) { close(started); <-release; return "old", nil })
		done <- err
	}()
	<-started
	cache.invalidate()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	value, err := cache.load(context.Background(), "feed", time.Minute, time.Second, nil, func(context.Context) (string, error) { return "new", nil })
	if err != nil || value != "new" {
		t.Fatalf("old flight repopulated baseline cache: %q %v", value, err)
	}
}

func TestSharedSocialCacheDoesNotBypassGroupPermission(t *testing.T) {
	p := NewResolverPlugin(nil)
	var calls atomic.Int32
	p.twitterPostFetcher = func(context.Context, string) (twitterPost, bool) {
		calls.Add(1)
		return twitterPost{ID: "1234567890", Text: "protected by group policy", AuthorHandle: "author"}, true
	}
	req := PluginRequest{Text: "https://x.com/author/status/1234567890", Event: MessageEvent{ProfileID: "tg", Platform: PlatformTelegram, Kind: EventKindGroup, GroupID: "100", UserID: "member"}}
	if result, err := p.Handle(context.Background(), req); err != nil || result == nil {
		t.Fatalf("warm cache failed: %v", err)
	}
	req.Event.ProfileID = "qq"
	req.Event.Platform = PlatformOneBotV11
	req.Event.SenderLevel = 1
	if result, err := p.Handle(context.Background(), req); err != nil || result != nil {
		t.Fatalf("cache bypassed group gate: %+v %v", result, err)
	}
	if calls.Load() != 1 {
		t.Fatal("blocked requester performed a fetch")
	}
}

func TestRSSDynamicOAuthJudgmentsAreNotReused(t *testing.T) {
	p := &sharedJudgeProvider{}
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "model", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "one", APIKey: "key", OAuthProvider: "dynamic-login"}}}}}
	r := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(NewRSSWatchPlugin(nil)), store, nil, nil, nil)
	r.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return p, nil })
	item := Reminder{FeedJudgePrompt: "notify"}
	change := rssWatchChange{FeedURL: "https://example.com/feed", Items: []rssWatchItem{{ID: "1", Content: "new"}}}
	for i := 0; i < 2; i++ {
		if _, err := r.judgeRSSWatch(context.Background(), item, change); err != nil {
			t.Fatal(err)
		}
	}
	if p.calls.Load() != 2 {
		t.Fatal("dynamic credentials shared model result")
	}
}
