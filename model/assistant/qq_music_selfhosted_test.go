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
	"time"
)

// qqL1124Server 模拟 L-1124/QQMusicApi：老 Node 版的路径一律 404，
// 和真实服务一样，靠这个认出方言。
type qqL1124Server struct {
	*httptest.Server
	legacyHits   atomic.Int32
	checkHits    atomic.Int32
	refreshHits  atomic.Int32
	expired      atomic.Bool
	refreshFails atomic.Bool
	mu           sync.Mutex
	urlCookies   []string
}

func newQQL1124Server(t *testing.T) *qqL1124Server {
	t.Helper()
	srv := &qqL1124Server{}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search" || r.URL.Path == "/song/urls":
			srv.legacyHits.Add(1)
			http.NotFound(w, r)
		case r.URL.Path == "/search/search_by_type":
			if r.URL.Query().Get("keyword") == "" || r.URL.Query().Get("search_type") != "0" {
				http.Error(w, "bad query", http.StatusUnprocessableEntity)
				return
			}
			fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"searchid":"1","song":[{"mid":"003OUlho2HcRHC","name":"晴天","singer":[{"name":"周杰伦"}],"album":{"name":"叶惠美"},"interval":269}]}}`)
		case r.URL.Path == "/song/003OUlho2HcRHC/url":
			srv.mu.Lock()
			srv.urlCookies = append(srv.urlCookies, r.Header.Get("Cookie"))
			srv.mu.Unlock()
			fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"expiration":80400,"data":[{"mid":"003OUlho2HcRHC","filename":"M500003OUlho2HcRHC003OUlho2HcRHC.mp3","purl":"M500003OUlho2HcRHC.mp3?vkey=abc","vkey":"abc","ekey":"","result":0}]}}`)
		case r.URL.Path == "/login/check_expired":
			srv.checkHits.Add(1)
			// 照搬 Web 层对 bool 的包装：true（已过期）是 code 0。
			if srv.expired.Load() {
				fmt.Fprint(w, `{"code":0,"msg":"ok","data":null}`)
				return
			}
			fmt.Fprint(w, `{"code":-1,"msg":"操作失败"}`)
		case r.URL.Path == "/login/refresh_credential":
			srv.refreshHits.Add(1)
			// 让并发请求有机会撞在一起。
			time.Sleep(20 * time.Millisecond)
			if srv.refreshFails.Load() || !strings.Contains(r.Header.Get("Cookie"), "refresh_key=rk-old") {
				fmt.Fprint(w, `{"code":-1,"msg":"登录凭证刷新失败"}`)
				return
			}
			srv.expired.Store(false)
			fmt.Fprintf(w, `{"code":0,"msg":"ok","data":{"openid":"","refresh_token":"rt-new","access_token":"","expired_at":0,"musicid":10001,"musickey":"Q_H_L_new","unionid":"","str_musicid":"10001","refresh_key":"rk-new","musickey_create_time":%d,"key_expires_in":259200,"login_type":2}}`, time.Now().Unix())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func qqSelfHostedTestConfig(base, cookie string) musicConfig {
	return musicConfigFromSettings(SettingValues{
		musicSourceAPIBaseSetting("qq"): base,
		musicSourceCookieSetting("qq"):  cookie,
	})
}

const qqRefreshableTestCookie = "musicid=10001; musickey=Q_H_L_old; refresh_key=rk-old; refresh_token=rt-old; theme=dark"

// 填了 L-1124 版地址时，原先请求的 /search?key=、/song/urls?id= 在那边是 404，
// 每次都悄悄退回官方接口，自建服务等于没配。
func TestQQSelfHostedRecognisesL1124Dialect(t *testing.T) {
	srv := newQQL1124Server(t)
	source := &qqSource{musicuAPI: srv.URL + "/official-unused?data=%s", streamCDN: "https://cdn.example.invalid/"}
	fetcher := &musicFetcher{client: srv.Client()}
	cfg := qqSelfHostedTestConfig(srv.URL, "")

	found, ok := source.Search(context.Background(), fetcher, cfg, "晴天")
	if !ok || found.ID != "003OUlho2HcRHC" || found.Name != "晴天" || found.Duration != 269*time.Second {
		t.Fatalf("Search() = %#v, %v", found, ok)
	}
	if got := source.PlayableURL(context.Background(), fetcher, cfg, found.ID); got != "https://cdn.example.invalid/M500003OUlho2HcRHC.mp3?vkey=abc" {
		t.Fatalf("PlayableURL() = %q", got)
	}
	if _, ok := source.Search(context.Background(), fetcher, cfg, "七里香"); !ok {
		t.Fatal("second Search() failed")
	}
	// 只在第一次搜索时试过一次老路径，之后记住了方言。
	if hits := srv.legacyHits.Load(); hits != 1 {
		t.Fatalf("legacy paths hit %d times, want 1", hits)
	}
}

// 老 Node 版照旧可用，认出来以后不会再去碰 L-1124 的路径。
func TestQQSelfHostedKeepsLegacyDialect(t *testing.T) {
	var newHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search":
			fmt.Fprint(w, `{"data":{"list":[{"songmid":"003OUlho2HcRHC","songname":"晴天","singer":[{"name":"周杰伦"}],"albumname":"叶惠美","interval":269}]}}`)
		case "/song/urls":
			fmt.Fprintf(w, `{"data":{%q:"https://legacy.example.invalid/a.mp3"}}`, r.URL.Query().Get("id"))
		default:
			newHits.Add(1)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	source := &qqSource{musicuAPI: server.URL + "/official-unused?data=%s", streamCDN: "https://cdn.example.invalid/"}
	fetcher := &musicFetcher{client: server.Client()}
	cfg := qqSelfHostedTestConfig(server.URL, qqRefreshableTestCookie)

	found, ok := source.Search(context.Background(), fetcher, cfg, "晴天")
	if !ok || found.ID != "003OUlho2HcRHC" {
		t.Fatalf("Search() = %#v, %v", found, ok)
	}
	if got := source.PlayableURL(context.Background(), fetcher, cfg, found.ID); got != "https://legacy.example.invalid/a.mp3" {
		t.Fatalf("PlayableURL() = %q", got)
	}
	// 老版本没有续期接口，带着能续期的 Cookie 也不该去问 /login/*。
	if hits := newHits.Load(); hits != 0 {
		t.Fatalf("L-1124 paths hit %d times on a legacy server", hits)
	}
}

// 过期的凭据只续一次：refresh_token 用过即作废，并发的几次点歌同时去换，
// 后到的必然失败。续出来的新 Cookie 要写回设置，下一次请求直接用。
func TestQQSelfHostedRefreshesExpiredCredentialOnce(t *testing.T) {
	srv := newQQL1124Server(t)
	srv.expired.Store(true)
	source := &qqSource{musicuAPI: srv.URL + "/official-unused?data=%s", streamCDN: "https://cdn.example.invalid/"}
	var saved []string
	var savedMu sync.Mutex
	fetcher := &musicFetcher{client: srv.Client(), saveCookie: func(_ context.Context, key, cookie string) error {
		savedMu.Lock()
		defer savedMu.Unlock()
		if key != "qq" {
			t.Errorf("saveCookie key = %q", key)
		}
		saved = append(saved, cookie)
		return nil
	}}
	cfg := qqSelfHostedTestConfig(srv.URL, qqRefreshableTestCookie)

	var wg sync.WaitGroup
	urls := make([]string, 6)
	for i := range urls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			urls[i] = source.PlayableURL(context.Background(), fetcher, cfg, "003OUlho2HcRHC")
		}(i)
	}
	wg.Wait()
	for i, got := range urls {
		if got == "" {
			t.Fatalf("PlayableURL()[%d] empty", i)
		}
	}
	if hits := srv.refreshHits.Load(); hits != 1 {
		t.Fatalf("refresh_credential called %d times, want 1", hits)
	}
	if len(saved) != 1 {
		t.Fatalf("saved %d cookies, want 1: %q", len(saved), saved)
	}
	values := musicCookieValues(saved[0])
	if values["musickey"] != "Q_H_L_new" || values["refresh_key"] != "rk-new" || values["refresh_token"] != "rt-new" || values["theme"] != "dark" || values["key_expires_in"] != "259200" {
		t.Fatalf("saved cookie = %q", saved[0])
	}
	srv.mu.Lock()
	for _, cookie := range srv.urlCookies {
		if !strings.Contains(cookie, "musickey=Q_H_L_new") {
			t.Errorf("song url requested with stale cookie %q", cookie)
		}
	}
	srv.mu.Unlock()

	// 写回后的 Cookie 自带过期时间，本地就判断得了，不用再问 check_expired。
	checks := srv.checkHits.Load()
	if got := source.PlayableURL(context.Background(), fetcher, qqSelfHostedTestConfig(srv.URL, saved[0]), "003OUlho2HcRHC"); got == "" {
		t.Fatal("PlayableURL() with refreshed cookie failed")
	}
	if srv.checkHits.Load() != checks || srv.refreshHits.Load() != 1 {
		t.Fatalf("refreshed cookie triggered another check (%d→%d) or refresh (%d)", checks, srv.checkHits.Load(), srv.refreshHits.Load())
	}
}

func TestQQSelfHostedSkipsRefreshWhileCredentialValid(t *testing.T) {
	srv := newQQL1124Server(t)
	source := &qqSource{musicuAPI: srv.URL + "/official-unused?data=%s", streamCDN: "https://cdn.example.invalid/"}
	fetcher := &musicFetcher{client: srv.Client()}
	cfg := qqSelfHostedTestConfig(srv.URL, qqRefreshableTestCookie)

	for range 3 {
		if got := source.PlayableURL(context.Background(), fetcher, cfg, "003OUlho2HcRHC"); got == "" {
			t.Fatal("PlayableURL() failed")
		}
	}
	if srv.checkHits.Load() != 1 || srv.refreshHits.Load() != 0 {
		t.Fatalf("check_expired %d times, refresh %d times; want 1 and 0", srv.checkHits.Load(), srv.refreshHits.Load())
	}
}

// 续期失败不能当作没事：凭据检查要说清楚已过期、为什么续不上，点歌失败时也要带上这句。
func TestQQSelfHostedReportsFailedRefresh(t *testing.T) {
	srv := newQQL1124Server(t)
	srv.expired.Store(true)
	srv.refreshFails.Store(true)
	source := &qqSource{musicuAPI: srv.URL + "/official-unused?data=%s", streamCDN: "https://cdn.example.invalid/"}
	fetcher := &musicFetcher{client: srv.Client(), saveCookie: func(context.Context, string, string) error {
		t.Error("failed refresh must not save a cookie")
		return nil
	}}
	cfg := qqSelfHostedTestConfig(srv.URL, qqRefreshableTestCookie)

	check := source.CheckLogin(context.Background(), fetcher, cfg)
	if check.State != CredentialInvalid || !strings.Contains(check.Message, "续期失败") || !strings.Contains(check.Message, "重新登录") {
		t.Fatalf("CheckLogin() = %#v", check)
	}

	ctx, diagnostics := withMusicDiagnostics(context.Background())
	source.PlayableURL(ctx, fetcher, cfg, "003OUlho2HcRHC")
	if problem := diagnostics.credentialProblem(); !strings.Contains(problem, "已过期") {
		t.Fatalf("credentialProblem() = %q", problem)
	}
}

// musicid/musickey 就是网页登录态的 uin/qqmusic_key。官方接口只认后者，不换一下，
// QQMusicApi 导出的有效 Cookie 会被凭据检查判成「没有登录」。
func TestQQOfficialRequestsAcceptQQMusicApiCookie(t *testing.T) {
	plugin := musicLoginTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		cookie := r.Header.Get("Cookie")
		if !strings.Contains(cookie, "qqmusic_key=Q_H_L_old") || !strings.Contains(cookie, "uin=10001") {
			fmt.Fprint(w, `{"req_0":{"code":1000}}`)
			return
		}
		fmt.Fprint(w, `{"req_0":{"code":0,"data":{"info":{"nick":"测试账号"}}}}`)
	})
	check := musicLoginCheck(t, plugin, "qq", "musicid=10001; musickey=Q_H_L_old")
	if check.State != CredentialValid || check.Account != "测试账号" {
		t.Fatalf("CheckLogin() = %#v", check)
	}
	// 已经带着官方键的 Cookie 原样发，不重复追加。
	if got := qqOfficialCookie("uin=10001; qqmusic_key=k"); got != "uin=10001; qqmusic_key=k" {
		t.Fatalf("qqOfficialCookie() = %q", got)
	}
}

func TestQQMergeRefreshedCookieKeepsOrderAndOfficialKeys(t *testing.T) {
	raw := "uin=10001; MusicKey=old; qqmusic_key=old; refresh_key=rk-old; theme=dark"
	got := qqMergeRefreshedCookie(raw, qqSelfHostedCredential{MusicID: 10001, MusicKey: "new", RefreshKey: "rk-new"})
	want := "uin=10001; MusicKey=new; qqmusic_key=new; refresh_key=rk-new; theme=dark; musicid=10001"
	if got != want {
		t.Fatalf("qqMergeRefreshedCookie() = %q, want %q", got, want)
	}
}

// 续期后的 Cookie 要写回全局设置并落盘，别的设置原样保留。
func TestMusicPluginSavesRefreshedCookie(t *testing.T) {
	plugin := NewMusicPlugin(nil)
	manager := NewPluginManager(plugin)
	if _, err := manager.UpdateSettings(musicPluginID, map[string]any{
		musicSourceCookieSetting("qq"):  "musicid=10001; musickey=old",
		musicSourceAPIBaseSetting("qq"): "http://127.0.0.1:8000",
	}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	runtime := &Runtime{plugins: manager}
	persisted := 0
	runtime.SetPluginStatePersister(func(context.Context) error {
		persisted++
		return nil
	})
	manager.bindRuntime(runtime)

	if err := plugin.fetcher.saveCookie(context.Background(), "qq", "musicid=10001; musickey=new"); err != nil {
		t.Fatalf("saveCookie() error = %v", err)
	}
	state, _ := manager.Get(musicPluginID)
	if state.Settings[musicSourceCookieSetting("qq")] != "musicid=10001; musickey=new" || state.Settings[musicSourceAPIBaseSetting("qq")] != "http://127.0.0.1:8000" {
		t.Fatalf("settings after save = %#v", state.Settings)
	}
	if persisted != 1 {
		t.Fatalf("persisted %d times, want 1", persisted)
	}
}
