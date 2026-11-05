// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func rankTestConfig(settings SettingValues) musicConfig {
	return musicConfigFromSettings(settings)
}

// 自动优先级按缓存的登录态排：会员有效的先问，会员过期的排到后面，同档保持登记顺序。
func TestMusicRankedSourcesPutsMembersFirst(t *testing.T) {
	ctx := context.Background()
	plugin := NewMusicPlugin(nil)
	cfg := rankTestConfig(SettingValues{"netease_cookie": "ne", "qq_cookie": "uin=10001; qqmusic_key=k"})
	plugin.logins.put("netease", "ne", CredentialCheck{State: CredentialValid})
	plugin.logins.put("qq", "uin=10001; qqmusic_key=k", CredentialCheck{State: CredentialValid, Member: true})
	if got := musicSourceKeysOf(plugin.rankedSources(ctx, cfg)); !slices.Equal(got, []string{"qq", "netease"}) {
		t.Fatalf("member first: got %v", got)
	}

	plugin.logins.put("qq", "uin=10001; qqmusic_key=k", CredentialCheck{State: CredentialValid, MembershipExpired: true, MembershipEnd: "2026-05-02"})
	plugin.logins.put("netease", "ne", CredentialCheck{State: CredentialValid, Member: true})
	if got := musicSourceKeysOf(plugin.rankedSources(ctx, cfg)); !slices.Equal(got, []string{"netease", "qq"}) {
		t.Fatalf("expired membership should go last: got %v", got)
	}

	// 都只是登录、都不是会员时不换位置。
	plugin.logins.put("netease", "ne", CredentialCheck{State: CredentialValid})
	if got := musicSourceKeysOf(plugin.rankedSources(ctx, cfg)); !slices.Equal(got, musicSourceKeys()) {
		t.Fatalf("tie keeps registration order: got %v", got)
	}
}

// 没填 Cookie 的一家算游客，排在已登录的后面，而且不发账号请求。
func TestMusicRankedSourcesGuestGoesLast(t *testing.T) {
	plugin := NewMusicPlugin(nil)
	cfg := rankTestConfig(SettingValues{"qq_cookie": "uin=10001; qqmusic_key=k"})
	plugin.logins.put("qq", "uin=10001; qqmusic_key=k", CredentialCheck{State: CredentialValid})
	if got := musicSourceKeysOf(plugin.rankedSources(context.Background(), cfg)); !slices.Equal(got, []string{"qq", "netease"}) {
		t.Fatalf("guest last: got %v", got)
	}
}

// 手动选了优先曲库就照选的来，不看登录态。
func TestMusicRankedSourcesManualPreferenceWins(t *testing.T) {
	plugin := NewMusicPlugin(nil)
	cfg := rankTestConfig(SettingValues{musicSettingPreferred: "netease", "qq_cookie": "uin=10001; qqmusic_key=k"})
	plugin.logins.put("qq", "uin=10001; qqmusic_key=k", CredentialCheck{State: CredentialValid, Member: true})
	if got := musicSourceKeysOf(plugin.rankedSources(context.Background(), cfg)); !slices.Equal(got, []string{"netease", "qq"}) {
		t.Fatalf("manual preference: got %v", got)
	}
}

// 换了 Cookie 或过了有效期，缓存就不算数。
func TestMusicLoginCacheKeysOnCookieAndExpires(t *testing.T) {
	now := time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC)
	cache := musicLoginCache{now: func() time.Time { return now }}
	cache.put("qq", "old", CredentialCheck{State: CredentialValid})
	if _, ok := cache.get("qq", "new"); ok {
		t.Fatal("a different cookie must miss")
	}
	if _, ok := cache.get("qq", "old"); !ok {
		t.Fatal("same cookie within TTL must hit")
	}
	now = now.Add(musicLoginCacheTTL + time.Second)
	if _, ok := cache.get("qq", "old"); ok {
		t.Fatal("expired entry must miss")
	}
	cache.put("qq", "old", CredentialCheck{State: CredentialError})
	now = now.Add(musicLoginErrorCacheTTL + time.Second)
	if _, ok := cache.get("qq", "old"); ok {
		t.Fatal("errors are retried sooner")
	}
}

// 放不出来时把各家的真实原因说出来，而不是笼统的「可能需要会员」。
func TestMusicMembershipNotes(t *testing.T) {
	plugin := NewMusicPlugin(nil)
	cfg := rankTestConfig(SettingValues{"qq_cookie": "uin=10001; qqmusic_key=k"})
	plugin.logins.put("qq", "uin=10001; qqmusic_key=k", CredentialCheck{State: CredentialValid, MembershipExpired: true, MembershipEnd: "2026-05-02"})
	notes := plugin.membershipNotes(context.Background(), cfg)
	for _, want := range []string{"网易云音乐没填登录 Cookie", "QQ 音乐会员已于 2026-05-02 到期"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes = %q, missing %q", notes, want)
		}
	}
	plugin.logins.put("qq", "uin=10001; qqmusic_key=k", CredentialCheck{State: CredentialValid, Member: true})
	if notes := plugin.membershipNotes(context.Background(), cfg); strings.Contains(notes, "QQ") {
		t.Fatalf("a valid member should not be blamed: %q", notes)
	}
}

func TestMusicLoginTierMatchesRank(t *testing.T) {
	cases := []struct {
		check      CredentialCheck
		configured bool
		tier       string
	}{
		{CredentialCheck{State: CredentialValid, Member: true}, true, musicTierMember},
		{CredentialCheck{State: CredentialValid, MembershipExpired: true}, true, musicTierExpired},
		{CredentialCheck{State: CredentialValid}, true, musicTierNotMember},
		{CredentialCheck{State: CredentialError}, true, musicTierUnknown},
		{CredentialCheck{State: CredentialInvalid}, true, musicTierInvalid},
		{CredentialCheck{}, false, musicTierGuest},
	}
	for _, c := range cases {
		if got := musicLoginTier(c.check, c.configured); got != c.tier {
			t.Fatalf("musicLoginTier(%+v, %v) = %q, want %q", c.check, c.configured, got, c.tier)
		}
	}
}
