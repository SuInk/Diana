// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type neteaseRefreshServer struct {
	*httptest.Server
	refreshHits atomic.Int32
	code        atomic.Int32
	mu          sync.Mutex
	cookies     []string
}

func newNeteaseRefreshServer(t *testing.T) (*neteaseRefreshServer, *neteaseSource) {
	t.Helper()
	srv := &neteaseRefreshServer{}
	srv.code.Store(200)
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		srv.mu.Lock()
		srv.cookies = append(srv.cookies, r.URL.Path+" "+r.Header.Get("Cookie"))
		srv.mu.Unlock()
		if r.URL.Path == "/refresh" {
			srv.refreshHits.Add(1)
			code := srv.code.Load()
			if code == 200 {
				http.SetCookie(w, &http.Cookie{Name: "MUSIC_U", Value: "music-u-new", Path: "/"})
			}
			fmt.Fprintf(w, `{"code":%d}`, code)
			return
		}
		fmt.Fprint(w, `{"result":{"songs":[{"id":1974443814,"name":"晴天","artists":[{"name":"周杰伦"}],"duration":269000}]}}`)
	}))
	t.Cleanup(srv.Close)
	source := &neteaseSource{searchAPI: srv.URL + "/search?s=%s", refreshAPI: srv.URL + "/refresh"}
	return srv, source
}

// MUSIC_U 一天续一次，Set-Cookie 里的新值写回设置，后面的请求带新值。
func TestNeteaseRefreshesMusicUOncePerInterval(t *testing.T) {
	srv, source := newNeteaseRefreshServer(t)
	var saved []string
	fetcher := &musicFetcher{client: srv.Client(), saveCookie: func(_ context.Context, key, cookie string) error {
		if key != "netease" {
			t.Errorf("saveCookie key = %q", key)
		}
		saved = append(saved, cookie)
		return nil
	}}
	cfg := musicConfigFromSettings(SettingValues{musicSourceCookieSetting("netease"): "MUSIC_U=music-u-old; __csrf=x"})
	for range 3 {
		if _, ok := source.Search(context.Background(), fetcher, cfg, "晴天"); !ok {
			t.Fatal("Search() failed")
		}
	}
	if hits := srv.refreshHits.Load(); hits != 1 {
		t.Fatalf("refresh called %d times, want 1", hits)
	}
	if len(saved) != 1 || saved[0] != "music-u-new" {
		t.Fatalf("saved = %q", saved)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !strings.Contains(srv.cookies[0], "/refresh") || !strings.Contains(srv.cookies[0], "MUSIC_U=music-u-old") || !strings.Contains(srv.cookies[0], "os=pc") {
		t.Fatalf("refresh request cookie = %q", srv.cookies[0])
	}
	for _, line := range srv.cookies[1:] {
		if !strings.Contains(line, "MUSIC_U=music-u-new") {
			t.Fatalf("request after refresh used stale cookie: %q", line)
		}
	}
}

// 没登录（301）不写回、不打断点歌，过一阵再试而不是每次都试。
func TestNeteaseRefreshFailureKeepsCookie(t *testing.T) {
	srv, source := newNeteaseRefreshServer(t)
	srv.code.Store(301)
	fetcher := &musicFetcher{client: srv.Client(), saveCookie: func(context.Context, string, string) error {
		t.Error("failed refresh must not save a cookie")
		return nil
	}}
	cfg := musicConfigFromSettings(SettingValues{musicSourceCookieSetting("netease"): "music-u-old"})
	for range 3 {
		if _, ok := source.Search(context.Background(), fetcher, cfg, "晴天"); !ok {
			t.Fatal("Search() failed")
		}
	}
	if hits := srv.refreshHits.Load(); hits != 1 {
		t.Fatalf("refresh called %d times, want 1", hits)
	}
}

// 没填 MUSIC_U 不去续。
func TestNeteaseSkipsRefreshWithoutCookie(t *testing.T) {
	srv, source := newNeteaseRefreshServer(t)
	fetcher := &musicFetcher{client: srv.Client()}
	if _, ok := source.Search(context.Background(), fetcher, musicConfigFromSettings(SettingValues{}), "晴天"); !ok {
		t.Fatal("Search() failed")
	}
	if hits := srv.refreshHits.Load(); hits != 0 {
		t.Fatalf("refresh called %d times, want 0", hits)
	}
}
