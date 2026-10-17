package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRSSWatchVideoCacheAndPrompt(t *testing.T) {
	const body = `{"code":200,"results":[{"id":"123","url":"https://x.com/a/status/123","text":"Trailer","media":{"videos":[{"url":"https://video.twimg.com/a.mp4","thumbnail_url":"https://pbs.twimg.com/a.jpg"}]},"quote":{"text":"Quoted trailer","media":{"videos":[{"url":"https://video.twimg.com/b.mp4","thumbnail_url":"https://pbs.twimg.com/b.jpg"}]}}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	defer server.Close()
	p := NewRSSWatchPlugin(server.Client())
	for _, phase := range []string{"fresh", "cached"} {
		t.Run(phase, func(t *testing.T) {
			change, err := p.check(context.Background(), server.URL, "", time.Time{}, SettingValues{})
			if err != nil {
				t.Fatal(err)
			}
			if len(change.Items) != 1 || len(change.Items[0].Videos) != 2 || change.Items[0].Videos[0].URL != "https://video.twimg.com/a.mp4" {
				t.Fatalf("videos lost: %+v", change.Items)
			}
			b, err := json.Marshal(change)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "twimg.com") {
				t.Fatalf("media leaked into prompt: %s", b)
			}
			change.Items[0].Videos[0].URL = "mutated"
		})
	}
}

func TestRSSWatchVideoSizeAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int
		declared  int64
		status    int
		mediaType string
		wantVideo bool
	}{
		{"within", 1024, 1024, 200, "video/mp4", true},
		{"at limit", 1 << 20, 1 << 20, 200, "video/mp4", true},
		{"declared oversized", 1, (1 << 20) + 1, 200, "video/mp4", false},
		{"unknown oversized", (1 << 20) + 1, -1, 200, "video/mp4", false},
		{"misreported oversized", (1 << 20) + 1, 1, 200, "video/mp4", false},
		{"unknown within", 1024, -1, 200, "video/mp4", true},
		{"empty", 0, 0, 200, "video/mp4", false},
		{"failed", 0, 0, 503, "video/mp4", false},
		{"html", 1024, 1024, 200, "text/html", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": []string{tc.mediaType}}, ContentLength: tc.declared, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", tc.size)))}, nil
			})}
			p := NewRSSWatchPlugin(client)
			item := rssWatchItem{ID: "123", Link: "https://x.com/a/status/123", Videos: []rssWatchVideo{{URL: "https://video.twimg.com/a.mp4", ThumbnailURL: "https://pbs.twimg.com/a.jpg"}}}
			tags := p.replyMedia(context.Background(), item.Link, []rssWatchItem{item}, SettingValues{rssWatchSettingVideoMaxMB: 1})
			msg := rssWatchOutgoingImages(OutgoingMessage{Text: "notice" + tags})
			if tc.wantVideo {
				if len(msg.VideoURLs) != 1 || len(msg.ImageURLs) != 0 {
					t.Fatalf("expected video only: %+v", msg)
				}
			} else if len(msg.VideoURLs) != 0 || len(msg.ImageURLs) != 1 {
				t.Fatalf("expected cover only: %+v", msg)
			}
			if msg.Text != "notice" {
				t.Fatalf("raw media tags left: %q", msg.Text)
			}
		})
	}
}

func TestRSSWatchVideoRejectsRedirectsAndUnmatchedItems(t *testing.T) {
	calls := 0
	p := NewRSSWatchPlugin(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.example/a.mp4"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})})
	items := []rssWatchItem{
		{ID: "123", Link: "https://x.com/a/status/123", Videos: []rssWatchVideo{{URL: "https://video.twimg.com/a.mp4", ThumbnailURL: "https://pbs.twimg.com/a.jpg"}}},
		{ID: "456", Link: "https://x.com/a/status/456", Videos: []rssWatchVideo{{URL: "https://video.twimg.com/b.mp4", ThumbnailURL: "https://pbs.twimg.com/b.jpg"}}},
	}
	tags := p.replyMedia(context.Background(), items[0].Link, items, SettingValues{})
	if calls != 1 || !strings.Contains(tags, "a.jpg") || strings.Contains(tags, "b.jpg") || strings.Contains(tags, "CQ:video") {
		t.Fatalf("redirect/matching: calls=%d tags=%s", calls, tags)
	}
	for _, raw := range []string{"http://video.twimg.com/a.mp4", "https://video.twimg.com.evil/a.mp4", "https://user@video.twimg.com/a.mp4", "https://video.twimg.com:443/a.mp4", "https://video.twimg.com/a.m3u8"} {
		if rssWatchVideoAllowed(raw) {
			t.Fatalf("unsafe URL allowed: %s", raw)
		}
	}
}

func TestRSSWatchVideoCancellationFallsBack(t *testing.T) {
	p := NewRSSWatchPlugin(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tags := p.replyMedia(ctx, "one", []rssWatchItem{{Videos: []rssWatchVideo{{URL: "https://video.twimg.com/a.mp4", ThumbnailURL: "https://pbs.twimg.com/a.jpg"}}}}, SettingValues{})
	if !strings.Contains(tags, "CQ:image") || strings.Contains(tags, "CQ:video") {
		t.Fatal(tags)
	}
}

func TestLiveRSSWatchTwitterVideoDelivery(t *testing.T) {
	if os.Getenv("DIANA_LIVE_RSS") != "1" {
		t.Skip("set DIANA_LIVE_RSS=1 to verify live Twitter videos")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := NewRSSWatchPlugin(nil)
	feedURL, err := twitterFeedURL("animecorner_ac")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.fetch(ctx, feedURL, SettingValues{}); err != nil {
		t.Fatal(err)
	}
	feed, err := p.fetch(ctx, feedURL, SettingValues{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range feed.Items {
		if len(item.Videos) == 0 {
			continue
		}
		reply := "RSS 视频测试：正常大小发送视频。\n" + item.Link
		normal := reply + p.replyMedia(ctx, item.Link, []rssWatchItem{item}, SettingValues{rssWatchSettingVideoMaxMB: 200})
		delivered := rssWatchOutgoingImages(OutgoingMessage{Text: normal})
		if len(delivered.VideoURLs) != 1 {
			t.Fatalf("live video could not be verified: %s", item.Link)
		}
		fallback := "RSS 视频测试：将上限设为 1 MiB，超限只发送封面。\n" + item.Link + p.replyMedia(ctx, item.Link, []rssWatchItem{item}, SettingValues{rssWatchSettingVideoMaxMB: 1})
		covered := rssWatchOutgoingImages(OutgoingMessage{Text: fallback})
		if len(covered.VideoURLs) != 0 || len(covered.ImageURLs) != 1 {
			t.Fatalf("live oversized fallback missing: %+v", covered)
		}
		t.Logf("tweet=%s video=%s; normal videos=%d; oversized videos=%d covers=%d", item.Link, delivered.VideoURLs[0], len(delivered.VideoURLs), len(covered.VideoURLs), len(covered.ImageURLs))
		if path := os.Getenv("DIANA_LIVE_RSS_OUTPUT"); path != "" {
			b, err := json.Marshal(map[string]string{"normal": normal, "fallback": fallback})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	t.Fatal("no video tweet in the live timeline")
}

func TestRSSWatchVideoNoticeDelivery(t *testing.T) {
	for _, platform := range []string{PlatformOneBotV11, PlatformTelegram} {
		t.Run(platform, func(t *testing.T) {
			channel := &recordingChannel{}
			r := NewRuntime(BotConfig{ID: "a", Platform: platform, SendRetryAttempts: 1}, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Platform: platform, ProfileID: "a", UserID: "100"}
			notice := "Trailer\n[CQ:video,file=https://video.twimg.com/a.mp4]"
			if err := r.sendSubscriberNotice(withSubscriptionPush(context.Background(), subscriptionPushRSSWatch), event, notice); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, msg := range channel.sentSnapshot() {
				for _, raw := range msg.VideoURLs {
					found = found || raw == "https://video.twimg.com/a.mp4"
				}
			}
			if !found {
				t.Fatalf("video missing from runtime delivery: %+v", channel.sentSnapshot())
			}
		})
	}
}
