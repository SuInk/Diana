package assistant

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTwitterProfileURLClassification(t *testing.T) {
	for _, raw := range []string{"https://x.com/SantosAdri64714", "https://twitter.com/SantosAdri64714/?s=21", "https://mobile.twitter.com/SantosAdri64714/media", "https://www.x.com/SantosAdri64714/with_replies"} {
		if got := twitterProfileHandle(raw); got != "SantosAdri64714" {
			t.Fatalf("%s: %s", raw, got)
		}
	}
	for _, raw := range []string{"https://x.com", "https://x.com/home", "https://x.com/i/status/123", "https://x.com/u/status/123/photo/1", "https://x.com/search?q=test", "https://x.com/intent/tweet", "https://x.com/u/unknown", "https://x.com.evil.test/u", "https://u@x.com/name", "ftp://x.com/name", "https://x.com/longer_than_fifteen"} {
		if twitterProfileHandle(raw) != "" {
			t.Fatalf("not a user profile: %s", raw)
		}
	}
	if key := resolverResourceKeyFromURL("x", "https://x.com/DianaVup?s=1"); key != "x:profile:dianavup" {
		t.Fatal(key)
	}
}

func TestFetchTwitterProfileResponses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
	}{
		{"success", 200, `{"code":200,"user":{"name":"Diana","screen_name":"example","description":"简介","followers":0,"following":2,"statuses":3}}`, ""},
		{"missing", 404, `{"code":404}`, "账号不存在"},
		{"rate limited", 429, `{}`, "限流"},
		{"envelope denied", 200, `{"code":403}`, "访问受限"},
		{"bad json", 200, `<html>unavailable</html>`, "无效数据"},
		{"wrong user", 200, `{"code":200,"user":{"name":"other","screen_name":"other"}}`, "未返回匹配"},
		{"empty", 200, `{"code":200,"user":null}`, "未返回匹配"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "https://api.fxtwitter.com/2/profile/example" {
					t.Fatal(req.URL)
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			profile, err := fetchTwitterProfileWithClient(context.Background(), "example", client)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err=%v", err)
				}
			} else if err != nil || profile.Handle != "example" || profile.Followers == nil || *profile.Followers != 0 {
				t.Fatalf("profile=%+v err=%v", profile, err)
			}
		})
	}
}

func TestTwitterProfilesNeverUseVideoDownloader(t *testing.T) {
	for _, platform := range []string{PlatformOneBotV11, PlatformTelegram} {
		for _, download := range []bool{false, true} {
			p := NewResolverPlugin(nil)
			p.twitterProfileFetcher = func(context.Context, string) (twitterProfile, error) {
				return twitterProfile{Name: "Diana", Handle: "example", Description: "公开简介"}, nil
			}
			p.mediaDownloader = func(context.Context, string) string { t.Fatal("profile sent to media downloader"); return "" }
			req := PluginRequest{Event: MessageEvent{Kind: EventKindPrivate, Platform: platform, UserID: "u"}, Text: "https://x.com/example", Settings: SettingValues{resolverSettingDownloadMedia: download}}
			response, err := p.Handle(context.Background(), req)
			if err != nil || response == nil || !strings.Contains(response.Context, "公开简介") || len(response.VideoURLs) != 0 {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		}
	}
	p := NewResolverPlugin(nil)
	p.videoDownloader = func(context.Context, string) string { t.Fatal("legacy downloader used for profile"); return "" }
	p.twitterProfileFetcher = func(context.Context, string) (twitterProfile, error) {
		return twitterProfile{Name: "Diana", Handle: "example"}, nil
	}
	if result := p.resolveTwitter(context.Background(), PluginRequest{}, "https://x.com/example"); !strings.Contains(result.Context, "用户主页") {
		t.Fatal(result)
	}
}

func TestTwitterProfileFailuresAreNotCachedAsMedia(t *testing.T) {
	p := NewResolverPlugin(nil)
	calls := 0
	p.twitterProfileFetcher = func(context.Context, string) (twitterProfile, error) {
		calls++
		return twitterProfile{}, errors.New("主页接口限流")
	}
	for i := 0; i < 2; i++ {
		result := p.resolveSocialMedia(context.Background(), PluginRequest{}, "https://x.com/example", 1, time.Minute)
		if !result.Handled || !result.Failed || !strings.Contains(result.Context, "限流") || strings.Contains(result.Context, "视频") || len(result.ResourceKeys) != 0 {
			t.Fatal(result)
		}
	}
	if calls != 2 {
		t.Fatalf("failure cached: calls=%d", calls)
	}
}

func TestLiveTwitterProfile(t *testing.T) {
	handle := os.Getenv("DIANA_LIVE_TWITTER_PROFILE_HANDLE")
	if handle == "" {
		t.Skip("set DIANA_LIVE_TWITTER_PROFILE_HANDLE to read a public profile")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	p := NewResolverPlugin(nil)
	result := p.resolveTwitterProfile(ctx, PluginRequest{}, "https://x.com/"+handle, handle)
	if result.Failed || !result.Handled {
		t.Fatal(result.Context)
	}
	t.Log(result.Context)
}
