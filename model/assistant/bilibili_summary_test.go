// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"net/url"
	"testing"
	"time"
)

func TestSignBilibiliWBIMatchesKnownWebExample(t *testing.T) {
	params := url.Values{"foo": {"114"}, "bar": {"514"}, "zab": {"1919810"}}
	signed, ok := signBilibiliWBI(
		params,
		"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png",
		"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png",
		time.Unix(1702204169, 0),
	)
	if !ok {
		t.Fatal("WBI signing rejected valid keys")
	}
	if got, want := signed.Get("w_rid"), "8f6f2b5b3d485fe1886cec6a0be8c5d4"; got != want {
		t.Fatalf("w_rid = %q, want %q", got, want)
	}
	if got := signed.Get("wts"); got != "1702204169" {
		t.Fatalf("wts = %q", got)
	}
}

func TestSelectedBilibiliCIDUsesRequestedPage(t *testing.T) {
	var view bilibiliViewResponse
	view.Data.CID = 10
	view.Data.Pages = append(view.Data.Pages,
		struct {
			CID      int64 `json:"cid"`
			Duration int   `json:"duration"`
		}{CID: 11},
		struct {
			CID      int64 `json:"cid"`
			Duration int   `json:"duration"`
		}{CID: 12},
	)
	if got := selectedBilibiliCID("https://www.bilibili.com/video/BV1xx?p=2", view); got != 12 {
		t.Fatalf("selected CID = %d, want 12", got)
	}
}

func TestBilibiliAIConclusionRequiresPlatformSummary(t *testing.T) {
	var response bilibiliAIConclusionResponse
	response.Code = 0
	response.Data.Code = 0
	response.Data.ModelResult.ResultType = 2
	response.Data.ModelResult.Summary = "  平台生成的视频总结  "
	if got, ok := bilibiliAIConclusionSummary(response); !ok || got != "平台生成的视频总结" {
		t.Fatalf("summary = %q, ok = %t", got, ok)
	}

	response.Data.Code = 1
	if got, ok := bilibiliAIConclusionSummary(response); ok || got != "" {
		t.Fatalf("unavailable platform summary leaked: %q, ok = %t", got, ok)
	}
}
