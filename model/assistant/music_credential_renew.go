// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"time"
)

// 后台续期：点歌时各家曲库会顺手检查凭据，但没人点歌的几天里 QQ 的 musickey
// 照样三天就过期，refresh_token 放久了也会作废。插件开着时每小时问一遍，
// 到点的才真去续，没到点只是本地看一眼时间。

const (
	musicCredentialRenewInterval = time.Hour
	// musicCredentialRenewDelay 是启动后第一次续期前等多久，别和启动恢复挤在一起。
	musicCredentialRenewDelay = time.Minute
)

// musicCredentialRenewer 是能自己续期登录态的曲库。
type musicCredentialRenewer interface {
	RenewCredentials(ctx context.Context, f *musicFetcher, cfg musicConfig)
}

func (s *qqSource) RenewCredentials(ctx context.Context, f *musicFetcher, cfg musicConfig) {
	_, _ = s.freshCredentials(ctx, f, cfg)
}

func (s *neteaseSource) RenewCredentials(ctx context.Context, f *musicFetcher, cfg musicConfig) {
	s.freshCredentials(ctx, f, cfg)
}

// PluginStateChanged 跟着插件开关启停后台续期，并记下最新设置：续期写回 Cookie
// 后这里也会被叫到，下一轮用的就是新 Cookie。用定时器而不是常驻协程，等待期间
// 不占协程，只有到点续期那一会儿才跑。
func (p *MusicPlugin) PluginStateChanged(change PluginStateChange) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.renewSettings = change.Settings
	if !change.Enabled {
		if p.renewTimer != nil {
			p.renewTimer.Stop()
			p.renewTimer = nil
		}
		return
	}
	if p.renewTimer == nil {
		p.renewTimer = time.AfterFunc(musicCredentialRenewDelay, p.renewTick)
	}
}

func (p *MusicPlugin) renewTick() {
	defer recoverGoroutinePanic("music_plugin.renew_credentials")
	p.renewCredentials(context.Background())
	p.mu.Lock()
	defer p.mu.Unlock()
	// 这一轮跑着的时候插件被关掉了，就不再排下一轮。
	if p.renewTimer != nil {
		p.renewTimer.Reset(musicCredentialRenewInterval)
	}
}

func (p *MusicPlugin) renewCredentials(ctx context.Context) {
	p.mu.RLock()
	settings := p.renewSettings
	p.mu.RUnlock()
	cfg := musicConfigFromSettings(settings)
	for _, source := range p.sources {
		renewer, ok := source.(musicCredentialRenewer)
		if !ok || !cfg.sourceEnabled(source.Key()) || cfg.sourceOptions(source.Key()).Cookie == "" {
			continue
		}
		renewCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		renewer.RenewCredentials(renewCtx, p.fetcher, cfg)
		cancel()
	}
}
