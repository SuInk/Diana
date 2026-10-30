// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBilibiliSourceFromInput(t *testing.T) {
	cases := []struct {
		raw, source, handle string
	}{
		{"dynamic:12345", rssSourceBilibiliDynamic, "12345"},
		{"https://space.bilibili.com/12345/dynamic", rssSourceBilibiliDynamic, "12345"},
		{"live:12345", rssSourceBilibiliLive, "UID 12345"},
		{"live:https://space.bilibili.com/12345", rssSourceBilibiliLive, "UID 12345"},
		{"https://live.bilibili.com/54321?spm=x", rssSourceBilibiliLive, "直播间 54321"},
		{"bangumi:ss11111", rssSourceBilibiliBangumi, "ss11111"},
		{"https://www.bilibili.com/bangumi/play/ep22222", rssSourceBilibiliBangumi, "ep22222"},
	}
	for _, tc := range cases {
		source, ok, err := bilibiliSourceFromInput(tc.raw)
		if !ok || err != nil || source.Source != tc.source || source.Handle != tc.handle {
			t.Fatalf("%s: got %+v ok=%v err=%v", tc.raw, source, ok, err)
		}
		// 已保存订阅交回接口地址时要认回同一个来源，WebUI 编辑才能原样保存。
		again, ok, err := bilibiliSourceFromInput(source.FeedURL)
		if !ok || err != nil || again.FeedURL != source.FeedURL || again.Source != source.Source {
			t.Fatalf("%s: round trip %+v ok=%v err=%v", tc.raw, again, ok, err)
		}
	}
	if _, ok, _ := bilibiliSourceFromInput("https://example.com/feed.xml"); ok {
		t.Fatal("ordinary feed treated as bilibili")
	}
	if _, ok, err := bilibiliSourceFromInput("dynamic:abc"); !ok || err == nil {
		t.Fatal("bad uid should be rejected")
	}
}

func TestResolveRSSWatchSourcesAcceptsBilibili(t *testing.T) {
	sources, err := resolveRSSWatchSources([]string{"live:12345", "https://example.com/feed.xml"}, nil)
	if err != nil || len(sources) != 2 || sources[0].Source != rssSourceBilibiliLive || sources[1].Source != "rss" {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	if label := rssWatchSourceLabel(sources[0]); label != "B 站直播 UID 12345" {
		t.Fatalf("label=%q", label)
	}
}

type rewriteTransport struct{ target string }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(rt.target, "http://")
	clone.Header.Set("X-Original-Host", req.URL.Host)
	return http.DefaultTransport.RoundTrip(clone)
}

func newBilibiliTestPlugin(t *testing.T, handler http.HandlerFunc) *RSSWatchPlugin {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewRSSWatchPlugin(&http.Client{Transport: rewriteTransport{target: server.URL}})
}

func TestBilibiliDynamicFeedSkipsPinned(t *testing.T) {
	plugin := newBilibiliTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Cookie"), "buvid3=") {
			t.Errorf("missing buvid3 cookie")
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[
{"id_str":"900","modules":{"module_tag":{"text":"置顶"},"module_author":{"name":"测试UP","pub_ts":100},"module_dynamic":{"desc":{"text":"pinned"}}}},
{"id_str":"902","modules":{"module_author":{"name":"测试UP","pub_ts":300},"module_dynamic":{"major":{"archive":{"title":"新视频","desc":"简介","cover":"//i0.hdslb.com/a.jpg","jump_url":"//www.bilibili.com/video/BV1xx411c7mD"}}}}},
{"id_str":"901","modules":{"module_author":{"name":"测试UP","pub_ts":200},"module_dynamic":{"desc":{"text":"转一下"}}},"orig":{"modules":{"module_author":{"name":"别人"},"module_dynamic":{"desc":{"text":"原文"}}}}}
]}}`))
	})
	feed, err := plugin.fetchFeed(context.Background(), bilibiliDynamicAPI+"?host_mid=12345", nil)
	if err != nil {
		t.Fatal(err)
	}
	if feed.Title != "测试UP 的 B 站动态" || len(feed.Items) != 2 {
		t.Fatalf("feed=%+v", feed)
	}
	video := feed.Items[0]
	if video.ID != "902" || video.Title != "投稿视频：新视频" || video.Link != "https://www.bilibili.com/video/BV1xx411c7mD" || len(video.Images) != 1 || video.Images[0] != "https://i0.hdslb.com/a.jpg" {
		t.Fatalf("video=%+v", video)
	}
	repost := feed.Items[1]
	if repost.Link != "https://t.bilibili.com/901" || !strings.Contains(repost.Content, "原文") {
		t.Fatalf("repost=%+v", repost)
	}
}

func TestBilibiliLiveFeedOnlyWhileLive(t *testing.T) {
	live := 1
	plugin := newBilibiliTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getRoomInfoOld") {
			_, _ = w.Write([]byte(`{"code":0,"data":{"roomid":54321}}`))
			return
		}
		if r.URL.Query().Get("room_id") != "54321" {
			t.Errorf("room_id=%q", r.URL.Query().Get("room_id"))
		}
		status := "0"
		if live == 1 {
			status = "1"
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"room_id":54321,"title":"晚间杂谈","live_status":` + status + `,"live_time":"2026-10-30 20:00:00","user_cover":"https://i0.hdslb.com/c.jpg","area_name":"聊天"}}`))
	})
	feedURL := bilibiliLiveByUIDAPI + "?mid=12345"
	feed, err := plugin.fetchFeed(context.Background(), feedURL, nil)
	if err != nil || len(feed.Items) != 1 {
		t.Fatalf("feed=%+v err=%v", feed, err)
	}
	item := feed.Items[0]
	if item.ID != "live:54321:2026-10-30 20:00:00" || item.Link != "https://live.bilibili.com/54321" || !item.PublishedAt.Equal(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("item=%+v", item)
	}
	live = 0
	feed, err = plugin.fetchFeed(context.Background(), feedURL, nil)
	if err != nil || len(feed.Items) != 0 {
		t.Fatalf("offline feed=%+v err=%v", feed, err)
	}
}

func TestBilibiliBangumiFeedNewestFirst(t *testing.T) {
	plugin := newBilibiliTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"result":{"title":"测试番","episodes":[
{"id":101,"title":"1","long_title":"开端","pub_time":1000},
{"id":102,"title":"2","long_title":"继续","pub_time":2000}]}}`))
	})
	feed, err := plugin.fetchFeed(context.Background(), bilibiliSeasonAPI+"?season_id=11111", nil)
	if err != nil || feed.Title != "测试番" || len(feed.Items) != 2 {
		t.Fatalf("feed=%+v err=%v", feed, err)
	}
	if feed.Items[0].ID != "ep102" || feed.Items[0].Title != "测试番 第2话 继续" || feed.Items[0].Link != "https://www.bilibili.com/bangumi/play/ep102" {
		t.Fatalf("latest=%+v", feed.Items[0])
	}
}

func TestBilibiliErrorHintsCredentials(t *testing.T) {
	plugin := newBilibiliTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":-352,"message":"风控校验失败"}`))
	})
	_, err := plugin.fetchFeed(context.Background(), bilibiliDynamicAPI+"?host_mid=12345", nil)
	if err == nil || !strings.Contains(err.Error(), "SESSDATA") {
		t.Fatalf("err=%v", err)
	}
}

func TestBilibiliDynamicEmptyWithoutLoginIsError(t *testing.T) {
	t.Setenv("DIANA_BILI_SESSDATA", "")
	t.Setenv("BILI_SESSDATA", "")
	plugin := newBilibiliTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[]}}`))
	})
	if _, err := plugin.fetchFeed(context.Background(), bilibiliDynamicAPI+"?host_mid=12345", nil); err == nil || !strings.Contains(err.Error(), "SESSDATA") {
		t.Fatalf("err=%v", err)
	}
	ctx := withResolverCredentials(context.Background(), resolverCredentials{BiliSessdata: "fake"})
	if feed, err := plugin.fetchFeed(ctx, bilibiliDynamicAPI+"?host_mid=12345", nil); err != nil || len(feed.Items) != 0 {
		t.Fatalf("feed=%+v err=%v", feed, err)
	}
}
