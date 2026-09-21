// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import "testing"

func TestSettingsWithDefaultsClampsWindow(t *testing.T) {
	settings := Settings{WindowWidth: 10, WindowHeight: 99999}.WithDefaults()
	if settings.WindowWidth != MinWindowSide {
		t.Fatalf("过小的宽度应夹到 %d，实际 %d", MinWindowSide, settings.WindowWidth)
	}
	if settings.WindowHeight != MaxWindowSide {
		t.Fatalf("过大的高度应夹到 %d，实际 %d", MaxWindowSide, settings.WindowHeight)
	}
	zero := Settings{}.WithDefaults()
	if zero.WindowWidth != DefaultWindowWidth || zero.WindowHeight != DefaultWindowHeight {
		t.Fatalf("零值应补成默认尺寸，实际 %dx%d", zero.WindowWidth, zero.WindowHeight)
	}
	if zero.Enabled {
		t.Fatal("零值必须是关着的：装了 Diana 不等于多出一个浏览器")
	}
}

// 这一档没有白名单，但黑名单要真的拦住，而且只认 http/https。
func TestHostAllowed(t *testing.T) {
	settings := Settings{DeniedHosts: []string{"Bank.example.com.", "*.internal.example.com", "https://blocked.example.com/path"}}.WithDefaults()
	cases := []struct {
		url  string
		want bool
	}{
		{"https://example.com/a", true},
		{"http://example.com:8080/a", true},
		{"https://bank.example.com/", false},
		{"https://admin.internal.example.com/", false},
		{"https://internal.example.com/", true},
		{"https://blocked.example.com/anything", false},
		{"file:///etc/passwd", false},
		{"chrome://settings", false},
		{"", false},
	}
	for _, testCase := range cases {
		if got := settings.HostAllowed(testCase.url); got != testCase.want {
			t.Fatalf("%s 期望 %v，实际 %v", testCase.url, testCase.want, got)
		}
	}
}

// 黑名单要规范化：大小写、末尾点、整条地址粘进来都得当成同一个站点。
func TestNormalizeHostsDropsWildcardOnly(t *testing.T) {
	settings := Settings{DeniedHosts: []string{"*", " ", "EXAMPLE.com", "example.com"}}.WithDefaults()
	if len(settings.DeniedHosts) != 1 || settings.DeniedHosts[0] != "example.com" {
		t.Fatalf("规范化结果不对：%v", settings.DeniedHosts)
	}
}
