// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/updater"

	"github.com/gin-gonic/gin"
)

// TestGithubRepoFromRemote 验证对应功能场景。
func TestGithubRepoFromRemote(t *testing.T) {
	cases := map[string][2]string{
		"git@github.com:SuInk/diana.git":     {"SuInk", "diana"},
		"https://github.com/SuInk/diana.git": {"SuInk", "diana"},
		"https://github.com/SuInk/diana":     {"SuInk", "diana"},
	}
	for remote, want := range cases {
		owner, repo, ok := githubRepoFromRemote(remote)
		if !ok || owner != want[0] || repo != want[1] {
			t.Fatalf("githubRepoFromRemote(%q) = %q/%q ok=%v", remote, owner, repo, ok)
		}
	}
	if _, _, ok := githubRepoFromRemote("https://gitee.com/x/y.git"); ok {
		t.Fatal("non-github remote should not match")
	}
}

// TestChangelogPrefersReleases 验证对应功能场景。
func TestChangelogPrefersReleases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/SuInk/diana/releases"):
			_, _ = w.Write([]byte(`[{"tag_name":"v1.1.0","name":"体验优化","body":"- 插件设置\n- 鉴权","prerelease":false,"published_at":"2026-07-26T12:00:00Z","html_url":"https://github.com/SuInk/diana/releases/tag/v1.1.0"},{"tag_name":"v1.2.0-draft","draft":true}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer github.Close()

	handler := NewSystemUpdateHandler(fakeSystemUpdater{status: updater.Status{
		RemoteURL: "git@github.com:SuInk/diana.git",
		Branch:    "main",
	}})
	handler.githubAPIBase = github.URL
	router := gin.New()
	handler.Register(router)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/update/changelog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("changelog = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"kind":"releases"`) || !strings.Contains(body, `"tag":"v1.1.0"`) {
		t.Fatalf("body = %s", body)
	}
	if strings.Contains(body, "v1.2.0-draft") {
		t.Fatalf("draft release leaked: %s", body)
	}
}

// TestRollbackEndpointRequiresConfirmation 验证版本回退只能由用户明确确认触发。
func TestRollbackEndpointRequiresConfirmation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewSystemUpdateHandler(fakeSystemUpdater{result: updater.Result{Status: updater.Status{HeadCommit: "de9c9be"}}})
	router := gin.New()
	handler.Register(router)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/system/update/rollback", strings.NewReader(`{"ref":"de9c9be"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed rollback = %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/system/update/rollback", strings.NewReader(`{"ref":"de9c9be","confirmation":"rollback-version"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "auto_update") {
		t.Fatalf("rollback response should not expose removed auto-update state: %s", rec.Body.String())
	}
	// 非法 ref 直接 400。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/system/update/rollback", strings.NewReader(`{"ref":"","confirmation":"rollback-version"}`))
	req.Header.Set("Content-Type", "application/json")
	badHandler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRemoteNotConfigured})
	badRouter := gin.New()
	badHandler.Register(badRouter)
	badRouter.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad ref rollback = %d", rec.Code)
	}
}

// TestChangelogEndpointFetchesGitHub 验证对应功能场景。
func TestChangelogEndpointFetchesGitHub(t *testing.T) {
	gin.SetMode(gin.TestMode)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/SuInk/diana/releases") {
			// 无 Release：返回空列表，触发提交记录回退。
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/repos/SuInk/diana/commits") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"sha":"abcdef1234567","commit":{"message":"feat: 第一行\n\n正文","author":{"name":"diana","date":"2026-07-26T10:00:00Z"}},"html_url":"https://github.com/SuInk/diana/commit/abcdef1"}]`))
	}))
	defer github.Close()

	handler := NewSystemUpdateHandler(fakeSystemUpdater{status: updater.Status{
		RemoteURL: "git@github.com:SuInk/diana.git",
		Branch:    "feature/ux-next",
	}})
	handler.githubAPIBase = github.URL
	router := gin.New()
	handler.Register(router)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/update/changelog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("changelog = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"kind":"commits"`) || !strings.Contains(rec.Body.String(), `"short":"abcdef1"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "正文") {
		t.Fatalf("message should keep first line only: %s", rec.Body.String())
	}

	// 第二次命中缓存，即使 GitHub 假服务关闭也能返回。
	github.Close()
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/update/changelog", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cached":true`) {
		t.Fatalf("cached changelog = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestChangelogRateLimitCooldownBlocksCommitFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetAt := time.Now().Add(20 * time.Minute)
	var calls atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasPrefix(r.URL.Path, "/repos/SuInk/diana/releases") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/repos/SuInk/diana/commits") {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
	}))
	defer github.Close()

	handler := NewSystemUpdateHandler(fakeSystemUpdater{status: updater.Status{
		RemoteURL: "git@github.com:SuInk/diana.git",
		Branch:    "main",
	}})
	handler.githubAPIBase = github.URL
	router := gin.New()
	handler.Register(router)

	for range 2 {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/update/changelog", nil))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("changelog = %d: %s", rec.Code, rec.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("GitHub calls during cooldown = %d, want releases+commits once", calls.Load())
	}
}

// TestVersionEndpointFallsBackToBuildVersion 验证对应功能场景。
func TestVersionEndpointFallsBackToBuildVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRemoteNotConfigured})
	handler.SetBuildVersion("v1.2.3")
	router := gin.New()
	handler.Register(router)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("version = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"build_version":"v1.2.3"`) || !strings.Contains(rec.Body.String(), `"git_available":false`) || !strings.Contains(rec.Body.String(), `"deployment_mode":"release"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
