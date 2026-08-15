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

// Cookie 过期是最高频的故障，提示必须能指向具体平台的凭据。
func TestDownloadFailureHintNamesMissingCredential(t *testing.T) {
	t.Setenv("DIANA_DOUYIN_CK", "")
	t.Setenv("DOUYIN_CK", "")
	t.Setenv("douyin_ck", "")
	t.Setenv("DIANA_XHS_CK", "")
	t.Setenv("XHS_CK", "")
	t.Setenv("xhs_ck", "")

	ctx := context.Background()
	if got := resolverCredentialFailureHint(ctx, "https://www.douyin.com/video/123"); !strings.Contains(got, "抖音 Cookie") {
		t.Fatalf("抖音缺 Cookie 时应明确指出，实际：%s", got)
	}
	if got := resolverCredentialFailureHint(ctx, "https://www.xiaohongshu.com/explore/abc"); !strings.Contains(got, "小红书 Cookie") {
		t.Fatalf("小红书缺 Cookie 时应明确指出，实际：%s", got)
	}

	// 配了 Cookie 之后提示应转向「可能已失效」而不是「未配置」。
	withCookie := withResolverCredentials(ctx, resolverCredentials{DouyinCookie: "x"})
	got := resolverCredentialFailureHint(withCookie, "https://www.douyin.com/video/123")
	if strings.Contains(got, "未配置") {
		t.Fatalf("已配置 Cookie 时不该提示未配置，实际：%s", got)
	}
	if !strings.Contains(got, "失效") {
		t.Fatalf("已配置 Cookie 时应提示可能失效，实际：%s", got)
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
