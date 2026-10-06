// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import "testing"

func TestPolicyDefaultsDisabledAllowAllWhenEnabled(t *testing.T) {
	policy := Policy{}.WithDefaults()
	if policy.Enabled {
		t.Fatal("默认必须是关闭的")
	}
	if policy.WriteEnabled {
		t.Fatal("默认必须只读")
	}
	// Enabled 打开且白名单为空 = 允许全部应用（主人默认）。
	policy.Enabled = true
	if !policy.AppAllowed("com.apple.Safari", "Safari") {
		t.Fatal("Enabled 且空白名单应允许任意应用")
	}
	if !policy.AppAllowed("", "TextEdit") {
		t.Fatal("没有 bundle id 时仍应按显示名放行（空白名单）")
	}
}

func TestPolicyAllowlistAndDeny(t *testing.T) {
	policy := Policy{
		Enabled:     true,
		AllowedApps: []string{"com.apple.Safari", "TextEdit"},
		DeniedApps:  []string{"com.apple.Safari"},
	}.WithDefaults()
	if policy.AppAllowed("com.apple.Safari", "Safari") {
		t.Fatal("黑名单应优先于白名单")
	}
	if !policy.AppAllowed("com.apple.TextEdit", "TextEdit") {
		t.Fatal("白名单内的显示名应放行")
	}
	if policy.AppAllowed("com.apple.Mail", "Mail") {
		t.Fatal("白名单外应拒绝")
	}
}

func TestPolicyRejectsStarOnly(t *testing.T) {
	policy := Policy{AllowedApps: []string{"*"}}.WithDefaults()
	if len(policy.AllowedApps) != 0 {
		t.Fatalf("单独 * 不该进白名单，得到 %v", policy.AllowedApps)
	}
}
