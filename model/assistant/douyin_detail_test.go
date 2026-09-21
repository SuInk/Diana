// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestPickDouyinFeedItemSkipsRecommendations(t *testing.T) {
	target := douyinMediaDetail{AwemeID: "7655654561134339561"}
	target.Video.PlayAddr.URI = "v2800fgi0000d8v5grfog65ufu9n2dog"
	other := douyinMediaDetail{AwemeID: "7687200037759250790"}
	other.Video.PlayAddr.URI = "v0200fg10000dan6mufog65kc7fg7hr0"
	blocked := douyinMediaDetail{AwemeID: "7655654561134339561"}

	item, ok := pickDouyinFeedItem([]douyinMediaDetail{other, target}, "7655654561134339561")
	if !ok || item.Video.PlayAddr.URI != target.Video.PlayAddr.URI {
		t.Fatalf("expected the requested aweme, got %#v (ok=%v)", item, ok)
	}
	if _, ok := pickDouyinFeedItem([]douyinMediaDetail{other}, "7655654561134339561"); ok {
		t.Fatal("a feed without the requested aweme must not match")
	}
	// 风控挡下时字段是空的，这种条目不能当成解析成功。
	if _, ok := pickDouyinFeedItem([]douyinMediaDetail{blocked}, "7655654561134339561"); ok {
		t.Fatal("an empty item must not count as usable")
	}
}

func TestDouyinDetailUsableAcceptsImagePosts(t *testing.T) {
	images := douyinMediaDetail{AwemeID: "1", AwemeType: 68}
	images.Images = append(images.Images, struct {
		URLList []string `json:"url_list"`
	}{URLList: []string{"https://example.invalid/1.jpg"}})
	if !douyinDetailUsable(images) {
		t.Fatal("image posts have no play_addr but are still usable")
	}
	if douyinDetailUsable(douyinMediaDetail{AwemeID: "1"}) {
		t.Fatal("an item with neither video nor images is not usable")
	}
}

func TestDouyinCookieValue(t *testing.T) {
	cookie := "ttwid=abc; UIFID=deadbeef; odin_tt=zzz"
	if got := douyinCookieValue(cookie, "UIFID"); got != "deadbeef" {
		t.Fatalf("UIFID=%q", got)
	}
	if got := douyinCookieValue(cookie, "msToken"); got != "" {
		t.Fatalf("missing field should be empty, got %q", got)
	}
}

func TestDouyinWebHeadersCarryCookieAndUifid(t *testing.T) {
	t.Setenv("DIANA_DOUYIN_CK", "ttwid=abc; UIFID=deadbeef")
	headers := douyinWebHeaders(context.Background(), "https://open.douyin.com/")
	if headers["Cookie"] != "ttwid=abc; UIFID=deadbeef" {
		t.Fatalf("Cookie=%q", headers["Cookie"])
	}
	// Argus 只看请求头里的 uifid，Cookie 里那份不算。
	if headers["uifid"] != "deadbeef" {
		t.Fatalf("uifid=%q", headers["uifid"])
	}
	if headers["Referer"] != "https://open.douyin.com/" || headers["User-Agent"] != douyinUserAgent {
		t.Fatalf("headers=%v", headers)
	}

	t.Setenv("DIANA_DOUYIN_CK", "")
	bare := douyinWebHeaders(context.Background(), "")
	if _, ok := bare["Cookie"]; ok {
		t.Fatal("没配 Cookie 时不应该发空 Cookie 头")
	}
	if _, ok := bare["uifid"]; ok {
		t.Fatal("没配 Cookie 时不应该发 uifid 头")
	}
}

// TestLiveDouyinDetail 打真实接口，确认 Argus 网关这条链路还通。
func TestLiveDouyinDetail(t *testing.T) {
	link := strings.TrimSpace(os.Getenv("DIANA_LIVE_DOUYIN_URL"))
	if link == "" {
		t.Skip("DIANA_LIVE_DOUYIN_URL is required")
	}
	detail, status := fetchDouyinDetail(context.Background(), link)
	if status != "" {
		t.Fatalf("status=%s", status)
	}
	if !douyinDetailUsable(detail) || strings.TrimSpace(detail.AwemeID) == "" {
		t.Fatalf("detail=%#v", detail)
	}
	t.Logf("aweme_id=%s type=%d desc=%.30s", detail.AwemeID, detail.AwemeType, detail.Desc)
}

// share/note 形式的图集 aweme_type 是 0，play_addr 指向背景音乐，只看 type 会把它当视频下。
func TestDouyinDetailIsImagePostByImages(t *testing.T) {
	note := douyinMediaDetail{AwemeID: "7684990612341317446", AwemeType: 0}
	note.Video.PlayAddr.URI = "https://sf11-cdn-tos.douyinstatic.com/obj/ies-music/7640489378485209865.mp3"
	note.Images = append(note.Images, struct {
		URLList []string `json:"url_list"`
	}{URLList: []string{"https://example.invalid/1.jpg"}}, struct {
		URLList []string `json:"url_list"`
	}{URLList: []string{"https://example.invalid/2.jpg"}})

	if !douyinDetailIsImagePost(note) {
		t.Fatal("带 images 的作品必须按图集处理")
	}
	if path := downloadDouyinMediaDetailFile(context.Background(), note); path != "" {
		t.Fatalf("图集不该走视频下载，实际拿到 %q", path)
	}
	if !douyinDetailIsImagePost(douyinMediaDetail{AwemeID: "1", AwemeType: 68}) {
		t.Fatal("老的图集 aweme_type 仍要认")
	}

	video := douyinMediaDetail{AwemeID: "2"}
	video.Video.PlayAddr.URI = "v0200fg10000dan6mufog65kc7fg7hr0"
	if douyinDetailIsImagePost(video) {
		t.Fatal("普通视频不该被当成图集")
	}
}

func TestDouyinPlayAddrURL(t *testing.T) {
	if got := douyinPlayAddrURL("v0200fg10000dan6mufog65kc7fg7hr0"); !strings.Contains(got, "video_id=v0200fg10000dan6mufog65kc7fg7hr0") {
		t.Fatalf("video_id 形式应拼到 play 接口，实际 %s", got)
	}
	// 已经是完整地址时再拼一次，play 接口只会回 0 字节。
	direct := "https://sf11-cdn-tos.douyinstatic.com/obj/ies-music/7640489378485209865.mp3"
	if got := douyinPlayAddrURL(direct); got != direct {
		t.Fatalf("完整地址应原样下载，实际 %s", got)
	}
}
