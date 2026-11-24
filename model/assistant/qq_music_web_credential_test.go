// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// qqWebLoginServer 模拟 musicu：POST 是续期（QQLogin），GET 是搜索，搜索只认 validKey。
type qqWebLoginServer struct {
	*httptest.Server
	refreshHits atomic.Int32
	refreshFail atomic.Bool
	validKey    atomic.Value
	mu          sync.Mutex
	refreshBody map[string]any
}

func newQQWebLoginServer(t *testing.T, validKey string) *qqWebLoginServer {
	t.Helper()
	srv := &qqWebLoginServer{}
	srv.validKey.Store(validKey)
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			srv.refreshHits.Add(1)
			body, _ := io.ReadAll(r.Body)
			var request struct {
				Req0 struct {
					Module string         `json:"module"`
					Method string         `json:"method"`
					Param  map[string]any `json:"param"`
				} `json:"req_0"`
			}
			_ = json.Unmarshal(body, &request)
			srv.mu.Lock()
			srv.refreshBody = request.Req0.Param
			srv.mu.Unlock()
			if srv.refreshFail.Load() || request.Req0.Module != qqWebLoginModule || request.Req0.Method != qqWebLoginMethod {
				fmt.Fprint(w, `{"code":0,"req_0":{"code":1000}}`)
				return
			}
			srv.validKey.Store("Q_H_L_new")
			fmt.Fprintf(w, `{"code":0,"req_0":{"code":0,"data":{"musickey":"Q_H_L_new","musickeyCreateTime":%d,"keyExpiresIn":259200,"refresh_token":"rt-new","access_token":"at-new","expired_at":1900000000,"openid":"openid-1","unionid":"union-1","encryptUin":"euin-1"}}}`, time.Now().Unix())
			return
		}
		if !strings.Contains(r.Header.Get("Cookie"), "qqmusic_key="+srv.validKey.Load().(string)) {
			fmt.Fprint(w, `{"req_0":{"code":2001}}`)
			return
		}
		fmt.Fprint(w, `{"req_0":{"code":0,"data":{"body":{"item_song":[{"mid":"003OUlho2HcRHC","name":"晴天","singer":[{"name":"周杰伦"}],"interval":269}]}}}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func qqWebTestCookie(created time.Time) string {
	return "uin=10001; qqmusic_key=Q_H_L_old; qm_keyst=Q_H_L_old; psrf_qqopenid=openid-1; psrf_qqaccess_token=at-old; psrf_qqrefresh_token=rt-old; psrf_access_token_expiresAt=1800000000; psrf_musickey_createtime=" + strconv.FormatInt(created.Unix(), 10) + "; tmeLoginType=2; ts_last=y.qq.com/"
}

func qqWebTestConfig(cookie string) musicConfig {
	return musicConfigFromSettings(SettingValues{musicSourceCookieSetting("qq"): cookie})
}

// 网页复制的 Cookie 过了一天就续：并发几次点歌只续一次，新 key 和新 token 写回设置，
// 其余键原样留着，搜索用的是新 key。
func TestQQWebCookieRefreshesOnceWhenStale(t *testing.T) {
	srv := newQQWebLoginServer(t, "Q_H_L_old")
	source := &qqSource{musicuAPI: srv.URL + "/musicu?data=%s"}
	var saved []string
	var savedMu sync.Mutex
	fetcher := &musicFetcher{client: srv.Client(), saveCookie: func(_ context.Context, key, cookie string) error {
		savedMu.Lock()
		defer savedMu.Unlock()
		saved = append(saved, cookie)
		return nil
	}}
	cfg := qqWebTestConfig(qqWebTestCookie(time.Now().Add(-30 * time.Hour)))

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := source.Search(context.Background(), fetcher, cfg, "晴天"); !ok {
				t.Error("Search() failed")
			}
		}()
	}
	wg.Wait()
	if hits := srv.refreshHits.Load(); hits != 1 {
		t.Fatalf("refresh called %d times, want 1", hits)
	}
	if len(saved) != 1 {
		t.Fatalf("saved %d cookies, want 1", len(saved))
	}
	values := musicCookieValues(saved[0])
	if values["qqmusic_key"] != "Q_H_L_new" || values["qm_keyst"] != "Q_H_L_new" || values["psrf_qqrefresh_token"] != "rt-new" ||
		values["psrf_qqaccess_token"] != "at-new" || values["psrf_access_token_expiresat"] != "1900000000" || values["ts_last"] != "y.qq.com/" {
		t.Fatalf("saved cookie = %q", saved[0])
	}
	if qqWebCredentialOlder(saved[0], qqWebCredentialRenewAge, time.Now()) {
		t.Fatalf("saved cookie still looks stale: %q", saved[0])
	}
	srv.mu.Lock()
	param := srv.refreshBody
	srv.mu.Unlock()
	if param["refresh_token"] != "rt-old" || param["access_token"] != "at-old" || param["musickey"] != "Q_H_L_old" || param["musicid"] != float64(10001) {
		t.Fatalf("refresh param = %#v", param)
	}
}

func TestQQWebCookieSkipsRefreshWhileFresh(t *testing.T) {
	srv := newQQWebLoginServer(t, "Q_H_L_old")
	source := &qqSource{musicuAPI: srv.URL + "/musicu?data=%s"}
	fetcher := &musicFetcher{client: srv.Client()}
	cfg := qqWebTestConfig(qqWebTestCookie(time.Now().Add(-time.Hour)))
	for range 3 {
		if _, ok := source.Search(context.Background(), fetcher, cfg, "晴天"); !ok {
			t.Fatal("Search() failed")
		}
	}
	if hits := srv.refreshHits.Load(); hits != 0 {
		t.Fatalf("refresh called %d times, want 0", hits)
	}
}

// 没到三天有效期时续期失败不算凭据失效：照用旧 key，也不在每次点歌时重试。
func TestQQWebCookieRefreshFailureKeepsUnexpiredKey(t *testing.T) {
	srv := newQQWebLoginServer(t, "Q_H_L_old")
	srv.refreshFail.Store(true)
	source := &qqSource{musicuAPI: srv.URL + "/musicu?data=%s"}
	fetcher := &musicFetcher{client: srv.Client(), saveCookie: func(context.Context, string, string) error {
		t.Error("failed refresh must not save a cookie")
		return nil
	}}
	cfg := qqWebTestConfig(qqWebTestCookie(time.Now().Add(-30 * time.Hour)))
	for range 3 {
		ctx, diagnostics := withMusicDiagnostics(context.Background())
		if _, ok := source.Search(ctx, fetcher, cfg, "晴天"); !ok {
			t.Fatal("Search() failed with an unexpired key")
		}
		if problem := diagnostics.credentialProblem(); problem != "" {
			t.Fatalf("credentialProblem() = %q", problem)
		}
	}
	if hits := srv.refreshHits.Load(); hits != 1 {
		t.Fatalf("refresh called %d times, want 1", hits)
	}
}

// 过了有效期又续不上，才说凭据失效、要重新复制。
func TestQQWebCookieReportsExpiredKeyThatCannotRefresh(t *testing.T) {
	srv := newQQWebLoginServer(t, "Q_H_L_valid-elsewhere")
	srv.refreshFail.Store(true)
	source := &qqSource{musicuAPI: srv.URL + "/musicu?data=%s"}
	fetcher := &musicFetcher{client: srv.Client()}
	cfg := qqWebTestConfig(qqWebTestCookie(time.Now().Add(-4 * 24 * time.Hour)))
	check := source.CheckLogin(context.Background(), fetcher, cfg)
	if check.State != CredentialInvalid || !strings.Contains(check.Message, "续期失败") || !strings.Contains(check.Message, "重新登录") {
		t.Fatalf("CheckLogin() = %#v", check)
	}
}

func TestQQWebRefreshableCookieNeedsQQLoginTokens(t *testing.T) {
	if !qqWebRefreshableCookie(qqWebTestCookie(time.Now())) {
		t.Fatal("web QQ login cookie should be refreshable")
	}
	for _, raw := range []string{
		"uin=10001; qqmusic_key=Q_H_L_old",
		"uin=10001; qqmusic_key=Q_H_L_old; wxopenid=w; wxrefresh_token=r; tmeLoginType=1",
		"psrf_qqopenid=o; psrf_qqaccess_token=a; psrf_qqrefresh_token=r; qqmusic_key=k",
	} {
		if qqWebRefreshableCookie(raw) {
			t.Errorf("qqWebRefreshableCookie(%q) = true", raw)
		}
	}
}
