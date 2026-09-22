// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 头像地址按内容寻址，和 Discord、GitHub 同一个路数：地址里带内容哈希，浏览器
// 可以永久缓存，换了头像就是一条新地址。
//
// 起因是 QQ 的头像 CDN 回 Cache-Control: max-age=2592000（30 天），而
// p.qlogo.cn/gh/<群号>/... 和 q1.qlogo.cn/g?nk=<QQ号> 又是固定地址——换了头像，
// 控制台里那张旧图能挂一个月。第三方的响应头改不了，只能自己接管这条链路。
//
// 服务端每 avatarRecheckInterval 回源校验一次，带 If-Modified-Since；qlogo 支持
// 条件请求，没换头像回 304、0 字节。6 小时是照着同类实现取的中间值：Gitea 的
// STATIC_CACHE_TIME 是 6h、联邦头像服务端缓存 1 天，Mastodon 远端账号的
// possibly_stale 阈值是 1 天。
const (
	avatarRecheckInterval = 6 * time.Hour
	// 只存元数据，字节放系统的文件缓存里，所以这个上限可以给得宽。
	avatarCacheEntries = 2048
	avatarFetchTimeout = 8 * time.Second
	avatarMaxBytes     = 4 << 20
)

// avatarEntry 是一张头像的当前状态。
type avatarEntry struct {
	sha          string
	contentType  string
	body         []byte
	lastModified string
	checkedAt    time.Time
}

type avatarCache struct {
	mu      sync.Mutex
	entries map[string]*avatarEntry
	// inflight 保证同一张头像同时只有一次回源：群管理页一次要几十张，
	// 首次打开会把它们一起请求下来。
	inflight map[string]chan struct{}
	client   *http.Client
	now      func() time.Time
}

func newAvatarCache() *avatarCache {
	return &avatarCache{
		entries:  make(map[string]*avatarEntry),
		inflight: make(map[string]chan struct{}),
		client:   &http.Client{Timeout: avatarFetchTimeout},
		now:      time.Now,
	}
}

// cachedSHA 返回已知的内容哈希，不触发任何回源。列表接口用它拼地址：一次列几十
// 个群，不能为了拼地址把几十张头像都下一遍。还不知道就先不带哈希，等浏览器来取
// 那张图时再说。
func (c *avatarCache) cachedSHA(key string) string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok {
		return entry.sha
	}
	return ""
}

// load 取头像内容，必要时回源。fetch 只在缓存过期或没有时调用。
func (c *avatarCache) load(ctx context.Context, key string, fetch func(ctx context.Context, previous *avatarEntry) (*avatarEntry, error)) (*avatarEntry, bool) {
	for {
		c.mu.Lock()
		entry, ok := c.entries[key]
		if ok && c.now().Sub(entry.checkedAt) < avatarRecheckInterval {
			c.mu.Unlock()
			return entry, true
		}
		wait, busy := c.inflight[key]
		if busy {
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				// 等不到就把手上这份旧的先给出去，总比列表里空一格强。
				return entry, entry != nil
			}
		}
		done := make(chan struct{})
		c.inflight[key] = done
		c.mu.Unlock()

		fresh, err := fetch(ctx, entry)
		c.mu.Lock()
		delete(c.inflight, key)
		if err == nil && fresh != nil {
			fresh.checkedAt = c.now()
			c.entries[key] = fresh
			entry = fresh
		} else if entry != nil {
			// 回源失败不丢已有的那张，只是推迟下一次校验，免得每个请求都去撞。
			entry.checkedAt = c.now()
		}
		c.evictLocked()
		c.mu.Unlock()
		close(done)
		return entry, entry != nil
	}
}

// evictLocked 超出条目上限就整批清掉。这里存的是可以随时重取的东西，清空的代价
// 只是下一次多走一趟回源，不值得为它维护一套 LRU。
func (c *avatarCache) evictLocked() {
	if len(c.entries) <= avatarCacheEntries {
		return
	}
	c.entries = make(map[string]*avatarEntry, avatarCacheEntries)
}

// fetchRemoteAvatar 按 URL 取头像，带条件请求。previous 有 Last-Modified 时，
// 没变的话对端回 304，一个字节都不用传。
func (c *avatarCache) fetchRemoteAvatar(ctx context.Context, url string, previous *avatarEntry) (*avatarEntry, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if previous != nil && previous.lastModified != "" {
		request.Header.Set("If-Modified-Since", previous.lastModified)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotModified && previous != nil {
		return previous, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, errUnexpectedAvatarStatus
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, avatarMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || int64(len(body)) > avatarMaxBytes {
		return nil, errUnexpectedAvatarStatus
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if !strings.HasPrefix(contentType, "image/") {
		contentType = http.DetectContentType(body)
	}
	if !strings.HasPrefix(contentType, "image/") {
		return nil, errUnexpectedAvatarStatus
	}
	return &avatarEntry{
		sha:          avatarContentSHA(body),
		contentType:  contentType,
		body:         body,
		lastModified: strings.TrimSpace(response.Header.Get("Last-Modified")),
	}, nil
}

func avatarContentSHA(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])[:16]
}

var errUnexpectedAvatarStatus = avatarError("头像源没有返回可用的图片")

type avatarError string

func (e avatarError) Error() string { return string(e) }
