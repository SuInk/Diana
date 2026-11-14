// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDouyinLiveTargetRecognizesLiveLinks(t *testing.T) {
	cases := []struct {
		raw          string
		room, webRID string
	}{
		// 分享短链跳转后的落地页。
		{"https://webcast.amemv.com/douyin/webcast/reflow/73001?u_code=x&roomId=73001&from=share", "73001", ""},
		{"https://webcast.amemv.com/douyin/webcast/somepage?roomId=73002", "73002", ""},
		{"https://live.douyin.com/58187?enter_from=share", "", "58187"},
		{"https://www.douyin.com/follow/live/58188", "", "58188"},
		{"https://www.douyin.com/video/12345", "", ""},
		{"https://www.douyin.com/note/12345", "", ""},
		{"https://v.douyin.com/abcDEF/", "", ""},
		{"https://live.douyin.com/", "", ""},
	}
	for _, tc := range cases {
		room, webRID := douyinLiveTarget(tc.raw)
		if room != tc.room || webRID != tc.webRID {
			t.Errorf("douyinLiveTarget(%q) = %q, %q; want %q, %q", tc.raw, room, webRID, tc.room, tc.webRID)
		}
	}
}

func TestBilibiliLiveRoomIDRecognizesRoomPages(t *testing.T) {
	cases := map[string]string{
		"https://live.bilibili.com/6":                         "6",
		"https://live.bilibili.com/21452505?broadcast_type=0": "21452505",
		"https://live.bilibili.com/h5/21452505":               "21452505",
		"https://live.bilibili.com/blanc/21452505/":           "21452505",
		"https://live.bilibili.com/p/eden/area-tags":          "",
		"https://www.bilibili.com/video/BV1xx411c7mD":         "",
		"https://b23.tv/abcdef":                               "",
	}
	for raw, want := range cases {
		if got := bilibiliLiveRoomID(raw); got != want {
			t.Errorf("bilibiliLiveRoomID(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestBilibiliLiveRoomFromResponse(t *testing.T) {
	var room bilibiliLiveRoomResponse
	var anchor bilibiliLiveAnchorResponse
	mustUnmarshal(t, `{"code":0,"data":{"room_id":77342,"short_id":6,"title":"赛事直播","live_status":1,"user_cover":"https://i0.hdslb.com/cover.jpg","online":750792,"area_name":"游戏赛事","parent_area_name":"赛事"}}`, &room)
	mustUnmarshal(t, `{"code":0,"data":{"info":{"uid":1,"uname":"官方赛事"}}}`, &anchor)
	info := bilibiliLiveRoomFromResponse(room, anchor)
	if info.RoomID != "77342" || info.Status != "live" || info.Anchor != "官方赛事" || info.Area != "赛事 · 游戏赛事" || info.Cover != "https://i0.hdslb.com/cover.jpg" {
		t.Fatalf("info = %#v", info)
	}
	text := liveRoomContext("Bilibili", info)
	for _, want := range []string{"[Bilibili直播] 赛事直播", "主播：官方赛事", "状态：直播中", "人气：750792"} {
		if !strings.Contains(text, want) {
			t.Fatalf("context %q missing %q", text, want)
		}
	}

	room.Data.LiveStatus = 2
	if got := bilibiliLiveRoomFromResponse(room, anchor); got.Status != "replay" {
		t.Fatalf("status = %q, want replay", got.Status)
	}
	room.Data.LiveStatus = 0
	offline := bilibiliLiveRoomFromResponse(room, anchor)
	if offline.Status != "offline" || strings.Contains(liveRoomContext("Bilibili", offline), "人气") {
		t.Fatalf("offline = %#v", offline)
	}
}

func TestDouyinLiveRoomResponses(t *testing.T) {
	var reflow douyinLiveReflowResponse
	mustUnmarshal(t, `{"status_code":0,"data":{"room":{"id_str":"77061","status":4,"title":"正在直播","user_count":0,"cover":{"url_list":["https://p11-webcast.douyinpic.com/a.jpg"]},"owner":{"nickname":"主播甲"}}}}`, &reflow)
	info := douyinLiveRoomInfo(reflow.Data.Room, "77061", "")
	if info.RoomID != "77061" || info.Status != "offline" || info.Anchor != "主播甲" || info.Cover == "" {
		t.Fatalf("reflow info = %#v", info)
	}

	var web douyinLiveWebEnterResponse
	mustUnmarshal(t, `{"status_code":0,"data":{"data":[{"id_str":"77068","status":2,"title":"打舞萌","user_count":321}],"user":{"nickname":"主播乙"}}}`, &web)
	info = douyinLiveRoomInfo(web.Data.Data[0], "58187", web.Data.User.Nickname)
	if info.RoomID != "77068" || info.Status != "live" || info.Anchor != "主播乙" || info.Online != 321 {
		t.Fatalf("web info = %#v", info)
	}
}

// 直播间链接不能落进作品解析（只会报「平台接口解析失败」），也不能拿去下载视频。
func TestResolverSocialMediaRoutesLiveLinksToLiveRoom(t *testing.T) {
	t.Setenv("DIANA_DOUYIN_CK", "")
	t.Setenv("DOUYIN_CK", "")
	cases := []struct {
		raw, platform, room, webRID, want, key string
	}{
		{"https://live.douyin.com/58187", "douyin", "", "58187", "[抖音直播] 打舞萌", "douyin:live:77068"},
		{"https://live.bilibili.com/6", "bilibili", "6", "", "[Bilibili直播] 打舞萌", "bilibili:live:77068"},
	}
	for _, tc := range cases {
		plugin := NewResolverPlugin(nil)
		plugin.douyinDetailFetcher = func(context.Context, string) (douyinMediaDetail, bool, string) {
			t.Fatalf("%s: live link went to douyin detail", tc.raw)
			return douyinMediaDetail{}, false, ""
		}
		plugin.mediaDownloader = func(context.Context, string) string {
			t.Fatalf("%s: live link went to video download", tc.raw)
			return ""
		}
		var gotPlatform, gotRoom, gotWebRID string
		plugin.liveRoomFetcher = func(_ context.Context, platform, roomID, webRID string) (liveRoomInfo, string) {
			gotPlatform, gotRoom, gotWebRID = platform, roomID, webRID
			return liveRoomInfo{RoomID: "77068", Title: "打舞萌", Anchor: "主播乙", Status: "live", Cover: "https://example.com/cover.jpg"}, ""
		}
		result := plugin.resolveSocialMediaFresh(context.Background(), PluginRequest{}, tc.raw, 9)
		if gotPlatform != tc.platform || gotRoom != tc.room || gotWebRID != tc.webRID {
			t.Fatalf("%s: fetcher got %q %q %q", tc.raw, gotPlatform, gotRoom, gotWebRID)
		}
		if !result.Handled || result.Failed || !strings.HasPrefix(result.Context, tc.want) || len(result.VideoURLs) != 0 {
			t.Fatalf("%s: result = %#v", tc.raw, result)
		}
		if len(result.ResourceKeys) != 1 || result.ResourceKeys[0] != tc.key || len(result.ImageURLs) != 1 {
			t.Fatalf("%s: keys/images = %#v / %#v", tc.raw, result.ResourceKeys, result.ImageURLs)
		}
	}
}

func TestResolverLiveRoomFailureSaysItIsALiveRoom(t *testing.T) {
	plugin := NewResolverPlugin(nil)
	plugin.liveRoomFetcher = func(context.Context, string, string, string) (liveRoomInfo, string) {
		return liveRoomInfo{}, "request_failed"
	}
	result := plugin.resolveSocialMediaFresh(context.Background(), PluginRequest{}, "https://live.douyin.com/58187", 9)
	if !result.Failed || len(result.ResourceKeys) != 0 || !strings.Contains(result.Context, "直播间") || strings.Contains(result.Context, "平台接口解析失败") {
		t.Fatalf("result = %#v", result)
	}
}

func mustUnmarshal(t *testing.T, raw string, out any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}
