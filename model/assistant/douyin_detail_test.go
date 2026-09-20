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
