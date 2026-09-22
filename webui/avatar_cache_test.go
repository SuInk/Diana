// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 起因：QQ 的头像 CDN 回 max-age=2592000（30 天），地址又固定，换了头像控制台里
// 那张旧图能挂一个月。现在自己接管：每 6 小时带 If-Modified-Since 回源校验一次，
// 没换就是 304、0 字节。
func TestAvatarCacheRevalidatesWithConditionalRequests(t *testing.T) {
	const lastModified = "Sun, 05 Apr 2026 09:46:20 GMT"
	var requests, bodiesSent atomic.Int64
	var current atomic.Value
	current.Store("第一版头像")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("If-Modified-Since") == lastModified && current.Load().(string) == "第一版头像" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		bodiesSent.Add(1)
		w.Header().Set("Last-Modified", lastModified)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(onePixelPNG(current.Load().(string)))
	}))
	defer server.Close()

	cache := newAvatarCache()
	now := time.Now()
	cache.now = func() time.Time { return now }
	fetch := func(ctx context.Context, previous *avatarEntry) (*avatarEntry, error) {
		return cache.fetchRemoteAvatar(ctx, server.URL+"/avatar", previous)
	}

	first, ok := cache.load(t.Context(), "member:10001", fetch)
	if !ok || first.sha == "" {
		t.Fatalf("第一次没取到：%+v", first)
	}
	if requests.Load() != 1 {
		t.Fatalf("第一次应当只取一次，实际 %d", requests.Load())
	}

	// 6 小时之内不回源，界面刷再多次也不打扰对端。
	for range 5 {
		if _, ok := cache.load(t.Context(), "member:10001", fetch); !ok {
			t.Fatal("缓存内取不到")
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("校验间隔内多余回源了 %d 次", requests.Load()-1)
	}

	// 过了间隔：回源，但头像没换，对端回 304，一个字节都不传。
	now = now.Add(avatarRecheckInterval + time.Minute)
	again, _ := cache.load(t.Context(), "member:10001", fetch)
	if requests.Load() != 2 {
		t.Fatalf("到点了没有回源校验：requests=%d", requests.Load())
	}
	if bodiesSent.Load() != 1 {
		t.Fatalf("没换头像却重新下载了：bodies=%d", bodiesSent.Load())
	}
	if again.sha != first.sha {
		t.Fatal("内容没变，哈希却变了")
	}

	// 换了头像：下一次校验拿到新内容，哈希跟着变——地址也就变了。
	current.Store("第二版头像")
	now = now.Add(avatarRecheckInterval + time.Minute)
	updated, _ := cache.load(t.Context(), "member:10001", fetch)
	if updated.sha == first.sha {
		t.Fatal("换了头像但哈希没变，界面还会显示旧图")
	}
	if bodiesSent.Load() != 2 {
		t.Fatalf("换了头像却没重新下载：bodies=%d", bodiesSent.Load())
	}
}

// 回源失败不能把已有的那张丢掉：网络抖一下界面就空一片不可接受。
func TestAvatarCacheKeepsLastGoodImageWhenRefreshFails(t *testing.T) {
	var fail atomic.Bool
	cache := newAvatarCache()
	now := time.Now()
	cache.now = func() time.Time { return now }
	fetch := func(ctx context.Context, previous *avatarEntry) (*avatarEntry, error) {
		if fail.Load() {
			return nil, errUnexpectedAvatarStatus
		}
		return &avatarEntry{sha: "abc", contentType: "image/png", body: onePixelPNG("x")}, nil
	}

	first, ok := cache.load(t.Context(), "group:111", fetch)
	if !ok || first.sha != "abc" {
		t.Fatalf("第一次没取到：%+v", first)
	}

	fail.Store(true)
	now = now.Add(avatarRecheckInterval + time.Minute)
	after, ok := cache.load(t.Context(), "group:111", fetch)
	if !ok || after.sha != "abc" || len(after.body) == 0 {
		t.Fatalf("回源失败把旧图弄丢了：%+v", after)
	}
	// 失败之后也要推迟下一次校验，否则每个请求都去撞一次。
	calls := 0
	fetchCounting := func(ctx context.Context, previous *avatarEntry) (*avatarEntry, error) {
		calls++
		return nil, errUnexpectedAvatarStatus
	}
	for range 3 {
		_, _ = cache.load(t.Context(), "group:111", fetchCounting)
	}
	if calls != 0 {
		t.Fatalf("失败后仍在每次请求都回源：%d 次", calls)
	}
}

// 一次列几十个群，拼地址不能触发下载。
func TestCachedSHANeverFetches(t *testing.T) {
	cache := newAvatarCache()
	if got := cache.cachedSHA("group:unknown"); got != "" {
		t.Fatalf("未知头像不该有哈希：%q", got)
	}
}

func onePixelPNG(seed string) []byte {
	// 不必是真 PNG：这几条用例只关心字节内容和哈希，不解码。
	return []byte("PNG:" + strings.Repeat(seed, 3))
}

// 一波消息同时进来，同一张头像正好过期：只能有一次回源，不能几十个请求一起去撞
// qlogo。过期后的并发校验要收敛成一次，其余的等这一次的结果。
func TestAvatarCacheCollapsesConcurrentRefresh(t *testing.T) {
	var fetches atomic.Int64
	release := make(chan struct{})
	cache := newAvatarCache()
	now := time.Now()
	cache.now = func() time.Time { return now }
	fetch := func(ctx context.Context, previous *avatarEntry) (*avatarEntry, error) {
		fetches.Add(1)
		// 卡住第一次回源，让后来的请求必然撞上「正在取」。
		<-release
		return &avatarEntry{sha: "abc", contentType: "image/png", body: onePixelPNG("x")}, nil
	}

	const callers = 32
	results := make(chan *avatarEntry, callers)
	for range callers {
		go func() {
			entry, _ := cache.load(context.Background(), "member:10001", fetch)
			results <- entry
		}()
	}
	// 等到确实有人在等这一次回源，再放行。
	deadline := time.Now().Add(2 * time.Second)
	for fetches.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)

	for range callers {
		select {
		case entry := <-results:
			if entry == nil || entry.sha != "abc" {
				t.Fatalf("并发调用没拿到结果：%+v", entry)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("并发调用卡住了")
		}
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("同一张头像同时回源了 %d 次，应当只有 1 次", got)
	}
}
