// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"time"
)

// 「自动」优先级按各家实测的登录态排：会员有效的先问，登录了但不是会员（或会员
// 过期）的其次，游客和失效 Cookie 最后；同档按登记顺序。QQ 会员过期、网易云
// 还是会员时，热门歌在 QQ 必然拿不到地址，先问它只是白白多等一轮。

const (
	musicLoginCacheTTL      = 30 * time.Minute
	musicLoginErrorCacheTTL = 5 * time.Minute
	musicLoginCheckTimeout  = 8 * time.Second
)

const (
	musicRankMember = iota
	musicRankLoggedIn
	musicRankGuest
)

type musicLoginEntry struct {
	cookieHash string
	check      CredentialCheck
	at         time.Time
}

// musicLoginCache 记各家最近一次账号实测结果。键里只放 Cookie 的摘要，
// 换了 Cookie 自然失效，内存里也不留原文。
type musicLoginCache struct {
	mu      sync.Mutex
	entries map[string]musicLoginEntry
	now     func() time.Time
}

func musicCookieHash(cookie string) string {
	sum := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(sum[:8])
}

func (c *musicLoginCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *musicLoginCache) get(source, cookie string) (CredentialCheck, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[source]
	if !ok || entry.cookieHash != musicCookieHash(cookie) {
		return CredentialCheck{}, false
	}
	ttl := musicLoginCacheTTL
	if entry.check.State == CredentialError {
		ttl = musicLoginErrorCacheTTL
	}
	if c.clock().Sub(entry.at) > ttl {
		return CredentialCheck{}, false
	}
	return entry.check, true
}

func (c *musicLoginCache) put(source, cookie string, check CredentialCheck) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]musicLoginEntry{}
	}
	c.entries[source] = musicLoginEntry{cookieHash: musicCookieHash(cookie), check: check, at: c.clock()}
}

// sourceLogin 返回这家的登录态，没填 Cookie 时不发请求、直接算游客。
func (p *MusicPlugin) sourceLogin(ctx context.Context, cfg musicConfig, source musicSource) (CredentialCheck, bool) {
	cookie := strings.TrimSpace(cfg.sourceOptions(source.Key()).Cookie)
	checker, ok := source.(musicLoginChecker)
	if cookie == "" || !ok {
		return CredentialCheck{}, false
	}
	if check, ok := p.logins.get(source.Key(), cookie); ok {
		return check, true
	}
	checkCtx, cancel := context.WithTimeout(ctx, musicLoginCheckTimeout)
	defer cancel()
	check := checker.CheckLogin(checkCtx, p.fetcher, cfg)
	p.logins.put(source.Key(), cookie, check)
	return check, true
}

// 账号档位，设置页的状态标签和排序都看它。
const (
	musicTierMember    = "member"
	musicTierExpired   = "expired"
	musicTierNotMember = "not_member"
	musicTierUnknown   = "unknown"
	musicTierInvalid   = "invalid"
	musicTierGuest     = "guest"
)

func musicLoginTier(check CredentialCheck, configured bool) string {
	if !configured {
		return musicTierGuest
	}
	switch check.State {
	case CredentialValid:
		switch {
		case check.Member && !check.MembershipExpired:
			return musicTierMember
		case check.MembershipExpired:
			return musicTierExpired
		}
		return musicTierNotMember
	case CredentialInvalid:
		return musicTierInvalid
	case CredentialUnconfigured:
		return musicTierGuest
	}
	return musicTierUnknown
}

func musicLoginRank(check CredentialCheck, configured bool) int {
	if !configured {
		return musicRankGuest
	}
	switch check.State {
	case CredentialValid:
		if check.Member && !check.MembershipExpired {
			return musicRankMember
		}
		return musicRankLoggedIn
	case CredentialInvalid, CredentialUnconfigured:
		return musicRankGuest
	}
	// 账号接口一时没问到，不知道好坏，排在中间，不让一次网络抖动把它踢到最后。
	return musicRankLoggedIn
}

// rankedSources 是点歌时的问询顺序：手动指定了优先曲库就照旧，否则按登录态自动排。
func (p *MusicPlugin) rankedSources(ctx context.Context, cfg musicConfig) []musicSource {
	sources := p.orderedSources(cfg)
	if strings.TrimSpace(cfg.PreferredSource) != "" || len(sources) < 2 {
		return sources
	}
	ranks := make(map[string]int, len(sources))
	for _, source := range sources {
		check, configured := p.sourceLogin(ctx, cfg, source)
		ranks[source.Key()] = musicLoginRank(check, configured)
	}
	slices.SortStableFunc(sources, func(a, b musicSource) int {
		return ranks[a.Key()] - ranks[b.Key()]
	})
	return sources
}

// membershipNotes 说清楚每家为什么放不出来，给「搜到了但没有播放地址」的报错用。
// 只用已缓存或现查的账号结果，不猜版权。
func (p *MusicPlugin) membershipNotes(ctx context.Context, cfg musicConfig) string {
	notes := make([]string, 0, len(p.sources))
	for _, source := range p.orderedSources(cfg) {
		check, configured := p.sourceLogin(ctx, cfg, source)
		switch {
		case !configured:
			notes = append(notes, source.Label()+"没填登录 Cookie，只能放公开试听的歌")
		case check.State == CredentialInvalid:
			notes = append(notes, source.Label()+"的 Cookie 已失效，需要重新复制")
		case check.State == CredentialValid && check.MembershipExpired && check.MembershipEnd != "":
			notes = append(notes, source.Label()+"会员已于 "+check.MembershipEnd+" 到期")
		case check.State == CredentialValid && check.MembershipExpired:
			notes = append(notes, source.Label()+"会员已到期")
		case check.State == CredentialValid && !check.Member:
			notes = append(notes, source.Label()+"已登录但不是会员")
		}
	}
	return strings.Join(notes, "；")
}
