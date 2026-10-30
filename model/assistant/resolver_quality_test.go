// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestYTDLPFormatSelectorRespectsMaxHeight(t *testing.T) {
	if got := ytDLPFormatSelector(1080); !strings.Contains(got, "height<=1080") {
		t.Fatalf("清晰度上限未生效：%s", got)
	}
	if got := ytDLPFormatSelector(0); strings.Contains(got, "height<=") {
		t.Fatalf("不限制时不该带高度过滤：%s", got)
	}
}

func TestResolverVideoMaxHeightFromContext(t *testing.T) {
	if got := resolverVideoMaxHeight(context.Background()); got != defaultVideoMaxHeight {
		t.Fatalf("默认清晰度应为 %d，实际 %d", defaultVideoMaxHeight, got)
	}
	ctx := withResolverMediaLimits(context.Background(), 100, 480, 1080)
	if got := resolverVideoMaxHeight(ctx); got != 1080 {
		t.Fatalf("期望 1080，实际 %d", got)
	}
	unlimited := withResolverMediaLimits(context.Background(), 100, 480, 0)
	if got := resolverVideoMaxHeight(unlimited); got != 0 {
		t.Fatalf("0 表示不限，实际 %d", got)
	}
}

func TestResolverMaxHeightFromSetting(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"1080", 1080},
		{"0", 0},
		{"", defaultVideoMaxHeight},
		{"高清", defaultVideoMaxHeight},
	}
	for _, tc := range cases {
		got := resolverMaxHeightFromSetting(SettingValues{resolverSettingMaxVideoHeight: tc.in})
		if got != tc.want {
			t.Errorf("输入 %q 期望 %d，实际 %d", tc.in, tc.want, got)
		}
	}
}

func TestSelectBilibiliDashMediaHonoursMaxHeight(t *testing.T) {
	videos := []bilibiliDashMedia{
		{BaseURL: "u480", Height: 480, Bandwidth: 100},
		{BaseURL: "u1080", Height: 1080, Bandwidth: 900},
	}
	audios := []bilibiliDashMedia{{BaseURL: "a", Bandwidth: 10}}

	video, _ := selectBilibiliDashMedia(720, videos, audios)
	if video.Base() != "u480" {
		t.Fatalf("720p 上限下应选 480p，实际 %s", video.Base())
	}
	video, _ = selectBilibiliDashMedia(0, videos, audios)
	if video.Base() != "u1080" {
		t.Fatalf("不限清晰度时应选 1080p，实际 %s", video.Base())
	}
}

func TestDownloadFailureHintDoesNotGuessCause(t *testing.T) {
	for _, raw := range []string{"https://x.com/example/status/1", "https://youtu.be/example", "https://www.xiaohongshu.com/explore/abc", "https://www.douyin.com/video/123"} {
		got := resolverDownloadFailureHint(context.Background(), raw)
		if got != "下载器未返回媒体文件，未提供具体错误" {
			t.Fatalf("unexpected failure hint: %s", got)
		}
	}
}

func TestResolverPlatformDomainsRejectLookalikes(t *testing.T) {
	valid := []string{
		"https://www.bilibili.com/video/BV1xx",
		"https://v.douyin.com/abc/",
		"https://www.xiaohongshu.com/explore/abc",
		"https://mobile.twitter.com/example/status/1",
		"https://x.com/example/status/1",
	}
	for _, raw := range valid {
		if !isKnownResolverPlatformURL(raw) {
			t.Errorf("valid resolver URL rejected: %s", raw)
		}
	}
	invalid := []string{
		"https://bilibili.com.attacker.example/video/1",
		"https://fake-douyin.com/video/1",
		"https://xiaohongshu.com.attacker.example/explore/1",
		"https://notx.com/status/1",
		"https://twitter.com.attacker.example/status/1",
		"file:///tmp/video.mp4",
	}
	for _, raw := range invalid {
		if isKnownResolverPlatformURL(raw) || isBilibiliURL(raw) || isDouyinURL(raw) || isXiaohongshuURL(raw) || isTwitterURL(raw) {
			t.Errorf("lookalike resolver URL accepted: %s", raw)
		}
	}
}
