// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"
)

type fakeMusicRenewer struct {
	musicSource
	key     string
	renewed []string
}

func (s *fakeMusicRenewer) Key() string { return s.key }

func (s *fakeMusicRenewer) RenewCredentials(_ context.Context, _ *musicFetcher, cfg musicConfig) {
	s.renewed = append(s.renewed, cfg.sourceOptions(s.key).Cookie)
}

// 后台续期只碰启用了、而且填了 Cookie 的曲库，用的是最近一次推来的设置。
func TestMusicPluginRenewsEnabledSourcesWithCookie(t *testing.T) {
	plugin := NewMusicPlugin(nil)
	qq := &fakeMusicRenewer{key: "qq"}
	netease := &fakeMusicRenewer{key: "netease"}
	plugin.sources = []musicSource{netease, qq}
	plugin.PluginStateChanged(PluginStateChange{Enabled: true, Settings: SettingValues{
		musicSettingSources:                 []any{"qq"},
		musicSourceCookieSetting("qq"):      "old",
		musicSourceCookieSetting("netease"): "unused",
	}})
	t.Cleanup(func() { plugin.PluginStateChanged(PluginStateChange{}) })
	plugin.PluginStateChanged(PluginStateChange{Enabled: true, Settings: SettingValues{
		musicSettingSources:                 []any{"qq", "netease"},
		musicSourceCookieSetting("qq"):      "new",
		musicSourceCookieSetting("netease"): "",
	}})
	plugin.renewCredentials(context.Background())
	if len(qq.renewed) != 1 || qq.renewed[0] != "new" {
		t.Fatalf("qq renewed = %q", qq.renewed)
	}
	if len(netease.renewed) != 0 {
		t.Fatalf("netease without cookie renewed %q", netease.renewed)
	}
}

func TestMusicPluginStopsRenewTimerWhenDisabled(t *testing.T) {
	plugin := NewMusicPlugin(nil)
	plugin.PluginStateChanged(PluginStateChange{Enabled: true})
	plugin.mu.RLock()
	started := plugin.renewTimer != nil
	plugin.mu.RUnlock()
	if !started {
		t.Fatal("renew timer not started")
	}
	plugin.PluginStateChanged(PluginStateChange{})
	plugin.mu.RLock()
	defer plugin.mu.RUnlock()
	if plugin.renewTimer != nil {
		t.Fatal("renew timer still set after disable")
	}
}
