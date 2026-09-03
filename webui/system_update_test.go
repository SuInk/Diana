// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/updater"

	"github.com/gin-gonic/gin"
)

type fakeSystemUpdater struct {
	status updater.Status
	result updater.Result
	err    error
}

type recordingSystemUpdater struct {
	fakeSystemUpdater
	target string
	force  bool
}

type recordingReleasePackageUpdater struct {
	unsupportedReason string
	status            updater.Status
	expected          string
	release           updater.ReleasePackage
	force             bool
	downloaded        bool
	installed         bool
	downloadErr       error
}

func (r *recordingReleasePackageUpdater) Supported() bool { return true }

func (r *recordingReleasePackageUpdater) UnsupportedReason() string { return r.unsupportedReason }

func (r *recordingReleasePackageUpdater) ExpectedAssetName() string { return r.expected }

func (r *recordingReleasePackageUpdater) Status(context.Context) (updater.Status, error) {
	return r.status, nil
}

func (r *recordingReleasePackageUpdater) Download(_ context.Context, release updater.ReleasePackage, force bool) (updater.Result, error) {
	r.release = release
	r.force = force
	if r.downloadErr != nil {
		r.status.Updating = true
		return updater.Result{}, r.downloadErr
	}
	r.downloaded = true
	r.status.DownloadReady = true
	r.status.DownloadedVersion = release.Tag
	return updater.Result{Status: r.status, Fetched: true, Downloaded: true, TargetCommit: release.Tag}, nil
}

func (r *recordingReleasePackageUpdater) InstallDownloaded(context.Context) (updater.Result, error) {
	r.installed = true
	r.status.DownloadReady = false
	r.status.RestartRequired = true
	return updater.Result{Status: r.status, Fetched: true, Updated: true, Downloaded: true, Applied: true, RestartRequired: true, TargetCommit: r.release.Tag}, nil
}

func (r *recordingReleasePackageUpdater) Install(_ context.Context, release updater.ReleasePackage, force bool) (updater.Result, error) {
	if _, err := r.Download(context.Background(), release, force); err != nil {
		return updater.Result{}, err
	}
	return r.InstallDownloaded(context.Background())
}

func (r *recordingSystemUpdater) UpdateToRelease(ctx context.Context, target string) (updater.Result, error) {
	r.target = target
	return r.fakeSystemUpdater.UpdateToRelease(ctx, target)
}

func (r *recordingSystemUpdater) ForceUpdateToRelease(ctx context.Context, target string) (updater.Result, error) {
	r.target = target
	r.force = true
	return r.fakeSystemUpdater.ForceUpdateToRelease(ctx, target)
}

// Status 返回当前状态快照。
func (f fakeSystemUpdater) Status(context.Context) (updater.Status, error) {
	if f.err != nil {
		return updater.Status{}, f.err
	}
	return f.status, nil
}

// Update 封装当前模块的 Update 逻辑。
func (f fakeSystemUpdater) Update(context.Context) (updater.Result, error) {
	if f.err != nil {
		return updater.Result{}, f.err
	}
	return f.result, nil
}

func (f fakeSystemUpdater) ForceUpdate(context.Context) (updater.Result, error) {
	if f.err != nil {
		return updater.Result{}, f.err
	}
	result := f.result
	result.Forced = true
	return result, nil
}

func (f fakeSystemUpdater) UpdateToRelease(context.Context, string) (updater.Result, error) {
	return f.Update(context.Background())
}

func (f fakeSystemUpdater) ForceUpdateToRelease(context.Context, string) (updater.Result, error) {
	return f.ForceUpdate(context.Background())
}

// Check 返回 fetch 后的状态快照。
func (f fakeSystemUpdater) Check(context.Context) (updater.Status, error) {
	if f.err != nil {
		return updater.Status{}, f.err
	}
	return f.status, nil
}

// Rollback 返回回退后的结果快照。
func (f fakeSystemUpdater) Rollback(_ context.Context, ref string) (updater.Result, error) {
	if f.err != nil {
		return updater.Result{}, f.err
	}
	result := f.result
	result.Updated = true
	if result.Status.HeadCommit == "" {
		result.Status.HeadCommit = ref
	}
	return result, nil
}

// TestSystemUpdateHandlerStatus 验证对应功能场景。
func TestSystemUpdateHandlerStatus(t *testing.T) {
	handler := NewSystemUpdateHandler(fakeSystemUpdater{
		status: updater.Status{
			Root:       "/tmp/repo",
			Branch:     "main",
			RemoteName: "origin",
			RemoteURL:  "https://github.com/example/repo.git",
			HeadCommit: "abc1234",
			Dirty:      true,
		},
	})
	router := systemUpdateTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/api/system/update", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload updater.Status
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if payload.Root != "/tmp/repo" || payload.Branch != "main" || !payload.Dirty {
		t.Fatalf("payload = %#v", payload)
	}
}

// TestSystemUpdateHandlerRemoteMissing 验证对应功能场景。
func TestSystemUpdateHandlerRemoteMissing(t *testing.T) {
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRemoteNotConfigured})
	router := systemUpdateTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/system/update", strings.NewReader(`{"confirmation":"apply-update"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestSystemUpdateHandlerReleaseCheckUsesGitHubRelease(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/SuInk/Diana/releases") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"tag_name":"v1.3.0","name":"v1.3.0","published_at":"2026-08-03T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"}]}]`))
	}))
	defer github.Close()

	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRemoteNotConfigured})
	handler.SetBuildVersion("v1.2.3")
	handler.now = func() time.Time { return time.Date(2026, 8, 16, 12, 34, 56, 0, time.FixedZone("CST", 8*60*60)) }
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/system/update/check", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"deployment_mode":"release"`) || !strings.Contains(body, `"latest_version":"v1.3.0"`) || !strings.Contains(body, `"latest_published_at":"2026-08-03T10:00:00Z"`) || !strings.Contains(body, `"checked_at":"2026-08-16T04:34:56Z"`) || !strings.Contains(body, `"update_available":true`) || !strings.Contains(body, `"checksum_available":true`) {
		t.Fatalf("body = %s", body)
	}
}

// TestSystemUpdateHandlerReleaseCheckAllowsUnknownCurrentVersion 验证没有注入正式版本号的构建
// 仍然能检测到正式 Release，而不是因为版本号无法比较就报错。
func TestSystemUpdateHandlerReleaseCheckAllowsUnknownCurrentVersion(t *testing.T) {
	github := releaseTestServer(t, "v0.8.7")
	defer github.Close()

	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetBuildVersion("a620040-reply-hotfix")
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/system/update/check", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"current_version":"a620040-reply-hotfix"`) || !strings.Contains(body, `"latest_version":"v0.8.7"`) || !strings.Contains(body, `"update_available":true`) {
		t.Fatalf("body = %s", body)
	}
}

// TestSystemUpdateHandlerReleaseDownloadAllowsUnknownCurrentVersion 验证版本号无法比较时
// 依然可以下载正式 Release 包，让用户把本地构建换回正式版本。
func TestSystemUpdateHandlerReleaseDownloadAllowsUnknownCurrentVersion(t *testing.T) {
	const assetName = "diana-webui-darwin-arm64.tar.gz"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.8.7","published_at":"2026-08-09T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz","size":1234}]}]`))
	}))
	defer github.Close()

	releaseUpdater := &recordingReleasePackageUpdater{
		expected: assetName,
		status:   updater.Status{NearestTag: "a620040-reply-hotfix", RunningCommit: "a620040-reply-hotfix", ApplySupported: true},
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetReleasePackageUpdater(releaseUpdater)
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/system/update/download", strings.NewReader(`{"confirmation":"download-update"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !releaseUpdater.downloaded || releaseUpdater.release.Tag != "v0.8.7" {
		t.Fatalf("downloaded = %v, tag = %q", releaseUpdater.downloaded, releaseUpdater.release.Tag)
	}
}

// TestSystemUpdateHandlerSourceBuildOffersReleaseSwitch 验证 Release 目录下的源码构建
// 不提示更新，而是给出显式切换到正式 Release 的入口。
func TestSystemUpdateHandlerSourceBuildOffersReleaseSwitch(t *testing.T) {
	const assetName = "diana-webui-darwin-arm64.tar.gz"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.8.7","published_at":"2026-08-09T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz","size":1234}]}]`))
	}))
	defer github.Close()

	releaseUpdater := &recordingReleasePackageUpdater{
		expected: assetName,
		status:   updater.Status{NearestTag: "v0.8.7-dev", RunningCommit: "v0.8.7-dev", ApplySupported: true},
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetReleasePackageUpdater(releaseUpdater)
	handler.SetBuildVersion("v0.8.7-dev")
	handler.SetBuildType("source")
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/system/update/check", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"build_type":"source"`) || !strings.Contains(body, `"switch_to_release_available":true`) || !strings.Contains(body, `"update_available":false`) {
		t.Fatalf("body = %s", body)
	}

	// 落后于正式 Release 的源码构建同样走显式切换，不会被当成普通更新。
	handler.SetBuildVersion("v0.8.6-dev")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/system/update/check", nil))
	if body = rec.Body.String(); !strings.Contains(body, `"update_available":false`) || !strings.Contains(body, `"switch_to_release_available":true`) {
		t.Fatalf("outdated source build body = %s", body)
	}
}

// TestSystemUpdateHandlerSourceBuildSkipsAutoUpdate 验证源码构建不会被后台自动替换。
func TestSystemUpdateHandlerSourceBuildSkipsAutoUpdate(t *testing.T) {
	const assetName = "diana-webui-darwin-arm64.tar.gz"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.8.7","published_at":"2026-08-09T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz","size":1234}]}]`))
	}))
	defer github.Close()

	releaseUpdater := &recordingReleasePackageUpdater{
		expected: assetName,
		status:   updater.Status{NearestTag: "v0.8.6-dev", RunningCommit: "v0.8.6-dev", ApplySupported: true},
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetReleasePackageUpdater(releaseUpdater)
	handler.SetBuildVersion("v0.8.6-dev")
	handler.SetBuildType("source")
	handler.githubAPIBase = github.URL

	handler.runAutoUpdate(context.Background())
	if releaseUpdater.downloaded {
		t.Fatal("source build must not auto download a release package")
	}

	// 正式构建保持原有的自动下载行为。
	handler.SetBuildType("release")
	handler.runAutoUpdate(context.Background())
	if !releaseUpdater.downloaded {
		t.Fatal("release build should still auto download")
	}
}

func TestSystemUpdateHandlerDownloadsThenInstallsCompleteReleasePackage(t *testing.T) {
	const assetName = "diana-webui-darwin-arm64.tar.gz"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v1.3.0","published_at":"2026-08-03T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz","size":1234}]}]`))
	}))
	defer github.Close()

	releaseUpdater := &recordingReleasePackageUpdater{
		status:   updater.Status{Root: "/Applications/Diana", NearestTag: "v1.2.3", RunningCommit: "v1.2.3", ApplySupported: true},
		expected: assetName,
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetBuildVersion("v1.2.3")
	handler.SetReleasePackageUpdater(releaseUpdater)
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	checkRecorder := httptest.NewRecorder()
	router.ServeHTTP(checkRecorder, httptest.NewRequest(http.MethodPost, "/api/system/update/check", nil))
	if checkRecorder.Code != http.StatusOK {
		t.Fatalf("check status = %d, body = %s", checkRecorder.Code, checkRecorder.Body.String())
	}
	var check systemUpdateCheckResponse
	if err := json.NewDecoder(checkRecorder.Body).Decode(&check); err != nil {
		t.Fatal(err)
	}
	if !check.UpdateAvailable || !check.UpdateSupported || check.DeploymentMode != "release" {
		t.Fatalf("check = %#v", check)
	}

	downloadRecorder := httptest.NewRecorder()
	downloadRequest := httptest.NewRequest(http.MethodPost, "/api/system/update/download", strings.NewReader(`{"confirmation":"download-update"}`))
	downloadRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(downloadRecorder, downloadRequest)
	if downloadRecorder.Code != http.StatusOK {
		t.Fatalf("download status = %d, body = %s", downloadRecorder.Code, downloadRecorder.Body.String())
	}
	if !releaseUpdater.downloaded || releaseUpdater.installed || releaseUpdater.force || releaseUpdater.release.Tag != "v1.3.0" || releaseUpdater.release.Archive.Name != assetName || releaseUpdater.release.Checksums.Name != "SHA256SUMS" {
		t.Fatalf("release updater = %#v", releaseUpdater)
	}

	installRecorder := httptest.NewRecorder()
	installRequest := httptest.NewRequest(http.MethodPost, "/api/system/update/install", strings.NewReader(`{"confirmation":"install-restart"}`))
	installRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(installRecorder, installRequest)
	if installRecorder.Code != http.StatusOK {
		t.Fatalf("install status = %d, body = %s", installRecorder.Code, installRecorder.Body.String())
	}
	if !releaseUpdater.installed {
		t.Fatal("downloaded release was not installed")
	}
}

func TestSystemUpdateHandlerReplacesStaleDownloadedRelease(t *testing.T) {
	const assetName = "diana-webui-linux-amd64.tar.gz"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.8.83","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz"}]}]`))
	}))
	defer github.Close()

	releaseUpdater := &recordingReleasePackageUpdater{
		expected: assetName,
		status: updater.Status{
			Root:              "/opt/diana",
			NearestTag:        "v0.8.81",
			RunningCommit:     "v0.8.81",
			ApplySupported:    true,
			DownloadReady:     true,
			DownloadedVersion: "v0.8.82",
		},
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetReleasePackageUpdater(releaseUpdater)
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/system/update/download", strings.NewReader(`{"confirmation":"download-update"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("download status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !releaseUpdater.downloaded || releaseUpdater.release.Tag != "v0.8.83" || releaseUpdater.status.DownloadedVersion != "v0.8.83" {
		t.Fatalf("stale package was not replaced: %#v", releaseUpdater)
	}
}

func TestSystemUpdateHandlerRejectsInstallingStaleDownloadedRelease(t *testing.T) {
	const assetName = "diana-webui-linux-amd64.tar.gz"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.8.83","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz"}]}]`))
	}))
	defer github.Close()

	releaseUpdater := &recordingReleasePackageUpdater{
		expected: assetName,
		status: updater.Status{
			Root:              "/opt/diana",
			NearestTag:        "v0.8.81",
			RunningCommit:     "v0.8.81",
			ApplySupported:    true,
			DownloadReady:     true,
			DownloadedVersion: "v0.8.82",
		},
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetReleasePackageUpdater(releaseUpdater)
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/system/update/install", strings.NewReader(`{"confirmation":"install-restart"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "v0.8.82") || !strings.Contains(recorder.Body.String(), "v0.8.83") {
		t.Fatalf("install status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if releaseUpdater.installed {
		t.Fatal("stale downloaded release must not be installed")
	}
}

func TestSystemUpdateHandlerRollsBackCompleteReleasePackageWithinRecentFive(t *testing.T) {
	const assetName = "diana-webui-darwin-arm64.tar.gz"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/SuInk/Diana/releases") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
            {"tag_name":"v1.3.0","published_at":"2026-08-09T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz"}]},
            {"tag_name":"v1.2.0","published_at":"2026-08-08T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz"}]},
            {"tag_name":"v1.1.0","published_at":"2026-08-07T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz"}]},
            {"tag_name":"v1.0.0","published_at":"2026-08-06T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz"}]},
            {"tag_name":"v0.9.0","published_at":"2026-08-05T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz"}]},
            {"tag_name":"v0.8.0","published_at":"2026-08-04T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz"}]},
            {"tag_name":"v0.7.0","published_at":"2026-08-03T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz"}]}
        ]`))
	}))
	defer github.Close()

	releaseUpdater := &recordingReleasePackageUpdater{
		status:   updater.Status{Root: "/Applications/Diana", NearestTag: "v1.3.0", RunningCommit: "v1.3.0", ApplySupported: true},
		expected: assetName,
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetReleasePackageUpdater(releaseUpdater)
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/system/update/rollback", strings.NewReader(`{"ref":"v1.0.0","confirmation":"rollback-version"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !releaseUpdater.installed || releaseUpdater.release.Tag != "v1.0.0" {
		t.Fatalf("rollback status = %d, body = %s, updater = %#v", rec.Code, rec.Body.String(), releaseUpdater)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/system/update/rollback", strings.NewReader(`{"ref":"v0.7.0","confirmation":"rollback-version"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "最近 5 个稳定版本") {
		t.Fatalf("old rollback status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestSystemUpdateHandlerJoinsConcurrentReleaseDownload(t *testing.T) {
	const assetName = "diana-webui-darwin-arm64.tar.gz"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v1.3.0","published_at":"2026-08-03T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz","size":1234}]}]`))
	}))
	defer github.Close()

	releaseUpdater := &recordingReleasePackageUpdater{
		status:      updater.Status{Root: "/Applications/Diana", NearestTag: "v1.2.3", RunningCommit: "v1.2.3", ApplySupported: true},
		expected:    assetName,
		downloadErr: updater.ErrUpdateInProgress,
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetReleasePackageUpdater(releaseUpdater)
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/system/update/download", strings.NewReader(`{"confirmation":"download-update"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var result updater.Result
	if err := json.NewDecoder(recorder.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Status.Updating || result.TargetCommit != "v1.3.0" || !strings.Contains(result.Output, "already in progress") {
		t.Fatalf("result = %#v", result)
	}
}

type memoryReleaseCacheStore struct {
	mu      sync.Mutex
	payload []byte
}

type memoryUpdatePolicyStore struct {
	policy updater.UpdatePolicy
	ok     bool
}

func TestReleaseCacheCadence(t *testing.T) {
	if releaseCacheTTL != 30*time.Minute {
		t.Fatalf("release cache TTL = %s", releaseCacheTTL)
	}
}

func (s *memoryUpdatePolicyStore) LoadUpdatePolicy(context.Context) (updater.UpdatePolicy, bool, error) {
	return s.policy, s.ok, nil
}

func (s *memoryUpdatePolicyStore) SaveUpdatePolicy(_ context.Context, policy updater.UpdatePolicy) error {
	s.policy = policy
	s.ok = true
	return nil
}

func TestSystemUpdatePolicyDefaultsAndPersists(t *testing.T) {
	store := &memoryUpdatePolicyStore{}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{})
	if err := handler.SetUpdatePolicyStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	router := systemUpdateTestRouter(handler)

	getRecorder := httptest.NewRecorder()
	router.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/api/system/update/policy", nil))
	if getRecorder.Code != http.StatusOK || !strings.Contains(getRecorder.Body.String(), `"auto_download":true`) || !strings.Contains(getRecorder.Body.String(), `"auto_install":false`) {
		t.Fatalf("default policy response = %d %s", getRecorder.Code, getRecorder.Body.String())
	}

	putRecorder := httptest.NewRecorder()
	putRequest := httptest.NewRequest(http.MethodPut, "/api/system/update/policy", strings.NewReader(`{"auto_download":false,"auto_install":true}`))
	putRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(putRecorder, putRequest)
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("save policy response = %d %s", putRecorder.Code, putRecorder.Body.String())
	}
	if !store.policy.AutoDownload || !store.policy.AutoInstall {
		t.Fatalf("stored policy = %#v", store.policy)
	}

	restarted := NewSystemUpdateHandler(fakeSystemUpdater{})
	if err := restarted.SetUpdatePolicyStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if got := restarted.currentPolicy(); !got.AutoDownload || !got.AutoInstall {
		t.Fatalf("policy after restart = %#v", got)
	}
}

func TestAutoUpdateDownloadsByDefaultAndInstallsOnlyWhenEnabled(t *testing.T) {
	const assetName = "diana-webui-darwin-arm64.tar.gz"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v1.3.0","published_at":"2026-08-03T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"` + assetName + `","browser_download_url":"https://example.test/package.tar.gz"}]}]`))
	}))
	defer github.Close()

	releaseUpdater := &recordingReleasePackageUpdater{
		status:   updater.Status{NearestTag: "v1.2.3", RunningCommit: "v1.2.3", ApplySupported: true},
		expected: assetName,
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetReleasePackageUpdater(releaseUpdater)
	handler.githubAPIBase = github.URL

	handler.runAutoUpdate(context.Background())
	if !releaseUpdater.downloaded || releaseUpdater.installed {
		t.Fatalf("default automatic update = %#v", releaseUpdater)
	}

	handler.policyMu.Lock()
	handler.policy = updater.UpdatePolicy{AutoDownload: true, AutoInstall: true}
	handler.policyMu.Unlock()
	handler.runAutoUpdate(context.Background())
	if !releaseUpdater.installed {
		t.Fatal("automatic install was not started after it was explicitly enabled")
	}
}

func TestAutoUpdateStopsRetryingFailedReleaseButAllowsNewerRelease(t *testing.T) {
	const assetName = "diana-webui-darwin-arm64.tar.gz"
	latestTag := "v1.3.0"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"tag_name":%q,"published_at":"2026-08-03T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":%q,"browser_download_url":"https://example.test/package.tar.gz"}]}]`, latestTag, assetName)
	}))
	defer github.Close()

	releaseUpdater := &recordingReleasePackageUpdater{
		status: updater.Status{
			NearestTag: "v1.2.3", RunningCommit: "v1.2.3", ApplySupported: true,
			LastUpdateVersion: "v1.3.0", LastUpdateFailures: 3,
		},
		expected: assetName,
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRepositoryNotFound})
	handler.SetReleasePackageUpdater(releaseUpdater)
	handler.githubAPIBase = github.URL
	handler.policy = updater.UpdatePolicy{AutoDownload: true, AutoInstall: true}

	handler.runAutoUpdate(context.Background())
	if releaseUpdater.downloaded || releaseUpdater.installed {
		t.Fatalf("blocked release was retried: %#v", releaseUpdater)
	}

	latestTag = "v1.4.0"
	handler.releaseCacheMu.Lock()
	handler.releaseCache = persistedReleaseCache{}
	handler.releaseCacheMu.Unlock()
	handler.runAutoUpdate(context.Background())
	if !releaseUpdater.downloaded || !releaseUpdater.installed {
		t.Fatalf("newer release was blocked: %#v", releaseUpdater)
	}
}

func (s *memoryReleaseCacheStore) LoadReleaseCache(context.Context) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.payload) == 0 {
		return nil, false, nil
	}
	return append([]byte(nil), s.payload...), true, nil
}

func (s *memoryReleaseCacheStore) SaveReleaseCache(_ context.Context, payload []byte) error {
	s.mu.Lock()
	s.payload = append([]byte(nil), payload...)
	s.mu.Unlock()
	return nil
}

func TestReleaseCachePersistsAcrossHandlerRestart(t *testing.T) {
	var calls atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`[{"tag_name":"v1.3.0","published_at":"2026-08-03T10:00:00Z","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"}]}]`))
	}))
	store := &memoryReleaseCacheStore{}

	first := NewSystemUpdateHandler(fakeSystemUpdater{})
	first.githubAPIBase = github.URL
	if err := first.SetReleaseCacheStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	release, err := first.latestStableRelease(context.Background(), "")
	if err != nil || release.Tag != "v1.3.0" {
		t.Fatalf("first release = %#v, err = %v", release, err)
	}
	github.Close()

	second := NewSystemUpdateHandler(fakeSystemUpdater{})
	second.githubAPIBase = github.URL
	if err := second.SetReleaseCacheStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	release, err = second.latestStableRelease(context.Background(), "")
	if err != nil || release.Tag != "v1.3.0" || !release.ChecksumAvailable {
		t.Fatalf("persisted release = %#v, err = %v", release, err)
	}
	if _, ok := release.asset("SHA256SUMS"); !ok {
		t.Fatalf("persisted release lost assets: %#v", release)
	}
	if calls.Load() != 1 {
		t.Fatalf("GitHub calls = %d, want 1", calls.Load())
	}
}

func TestReleaseCacheSingleflightCoalescesConcurrentChecks(t *testing.T) {
	var calls atomic.Int32
	requestStarted := make(chan struct{}, 1)
	releaseResponse := make(chan struct{})
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		requestStarted <- struct{}{}
		<-releaseResponse
		_, _ = w.Write([]byte(`[{"tag_name":"v1.3.0","published_at":"2026-08-03T10:00:00Z"}]`))
	}))
	defer github.Close()

	handler := NewSystemUpdateHandler(fakeSystemUpdater{})
	handler.githubAPIBase = github.URL
	const callers = 8
	start := make(chan struct{})
	errCh := make(chan error, callers)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	for range callers {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			release, err := handler.latestStableRelease(context.Background(), "")
			if err == nil && release.Tag != "v1.3.0" {
				err = errors.New("unexpected release tag " + release.Tag)
			}
			errCh <- err
		}()
	}
	ready.Wait()
	close(start)
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("GitHub request did not start")
	}
	time.Sleep(25 * time.Millisecond)
	close(releaseResponse)
	done.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("GitHub calls = %d, want 1", calls.Load())
	}
}

func TestReleaseCacheHonorsRateLimitResetAndReturnsStaleData(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	var resetUnix atomic.Int64
	var calls atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			_, _ = w.Write([]byte(`[{"tag_name":"v1.3.0","published_at":"2026-08-03T10:00:00Z"}]`))
		case 2:
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetUnix.Load(), 10))
			w.WriteHeader(http.StatusForbidden)
		default:
			_, _ = w.Write([]byte(`[{"tag_name":"v1.4.0","published_at":"2026-08-15T10:00:00Z"}]`))
		}
	}))
	defer github.Close()

	store := &memoryReleaseCacheStore{}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{})
	handler.githubAPIBase = github.URL
	handler.now = func() time.Time { return now }
	if err := handler.SetReleaseCacheStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if release, err := handler.latestStableRelease(context.Background(), ""); err != nil || release.Tag != "v1.3.0" {
		t.Fatalf("initial release = %#v, err = %v", release, err)
	}

	now = now.Add(releaseCacheTTL + time.Minute)
	resetAt := now.Add(20 * time.Minute)
	resetUnix.Store(resetAt.Unix())
	if release, err := handler.latestStableRelease(context.Background(), ""); err != nil || release.Tag != "v1.3.0" {
		t.Fatalf("stale release after limit = %#v, err = %v", release, err)
	}
	if release, err := handler.latestStableRelease(context.Background(), ""); err != nil || release.Tag != "v1.3.0" {
		t.Fatalf("stale release during cooldown = %#v, err = %v", release, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("GitHub calls during cooldown = %d, want 2", calls.Load())
	}

	restarted := NewSystemUpdateHandler(fakeSystemUpdater{})
	restarted.githubAPIBase = github.URL
	restarted.now = func() time.Time { return now }
	if err := restarted.SetReleaseCacheStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if release, err := restarted.latestStableRelease(context.Background(), ""); err != nil || release.Tag != "v1.3.0" {
		t.Fatalf("persisted cooldown release = %#v, err = %v", release, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("GitHub calls after restart during cooldown = %d, want 2", calls.Load())
	}

	now = resetAt.Add(time.Second)
	if release, err := restarted.latestStableRelease(context.Background(), ""); err != nil || release.Tag != "v1.4.0" {
		t.Fatalf("release after reset = %#v, err = %v", release, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("GitHub calls after reset = %d, want 3", calls.Load())
	}
}

func TestReleaseCacheUsesTokenAndETag(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if calls.Add(1) == 1 {
			w.Header().Set("ETag", `"release-v1"`)
			_, _ = w.Write([]byte(`[{"tag_name":"v1.3.0","published_at":"2026-09-01T10:00:00Z"}]`))
			return
		}
		if got := r.Header.Get("If-None-Match"); got != `"release-v1"` {
			t.Errorf("If-None-Match = %q", got)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer github.Close()

	handler := NewSystemUpdateHandler(fakeSystemUpdater{})
	handler.githubAPIBase = github.URL
	handler.githubToken = "test-token"
	handler.now = func() time.Time { return now }
	if release, err := handler.latestStableRelease(context.Background(), ""); err != nil || release.Tag != "v1.3.0" {
		t.Fatalf("initial release = %#v, %v", release, err)
	}
	now = now.Add(releaseCacheTTL + time.Second)
	if release, err := handler.latestStableRelease(context.Background(), ""); err != nil || release.Tag != "v1.3.0" {
		t.Fatalf("304 release = %#v, %v", release, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestStaticReleaseManifestFallback(t *testing.T) {
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"releases":[{"tag":"v1.5.0","checksum_available":true,"assets":[{"name":"SHA256SUMS","url":"https://example.test/SHA256SUMS","size":12}]}]}`))
	}))
	defer manifest.Close()

	handler := NewSystemUpdateHandler(fakeSystemUpdater{})
	handler.githubAPIBase = "http://127.0.0.1:1"
	handler.staticReleaseURL = manifest.URL
	if release, err := handler.latestStableRelease(context.Background(), ""); err != nil || release.Tag != "v1.5.0" {
		t.Fatalf("fallback release = %#v, %v", release, err)
	}
}

func TestSystemUpdateHandlerGitCheckUsesLatestReleaseInsteadOfBranchBehind(t *testing.T) {
	github := releaseTestServer(t, "v0.4.0")
	defer github.Close()

	handler := NewSystemUpdateHandler(fakeSystemUpdater{status: updater.Status{
		RemoteURL:       "https://github.com/SuInk/Diana.git",
		NearestTag:      "v0.3.4",
		CommitsSinceTag: 11,
		Behind:          0,
	}})
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/system/update/check", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload systemUpdateCheckResponse
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.CurrentVersion != "v0.3.4+11" || payload.LatestVersion != "v0.4.0" || !payload.UpdateAvailable {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestSystemUpdateHandlerUpdateRequiresConfirmation(t *testing.T) {
	handler := NewSystemUpdateHandler(fakeSystemUpdater{result: updater.Result{
		Status: updater.Status{HeadCommit: "abc1234"},
	}, status: updater.Status{RemoteURL: "https://github.com/SuInk/Diana.git", NearestTag: "v1.2.3"}})
	github := releaseTestServer(t, "v1.3.0")
	defer github.Close()
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/system/update", strings.NewReader(`{"force":false}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed update status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/system/update", strings.NewReader(`{"force":true}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed force status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/system/update", strings.NewReader(`{"force":true,"confirmation":"force-update"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"forced":true`) {
		t.Fatalf("confirmed force status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestVersionComparison(t *testing.T) {
	newer, err := isNewerVersion("v1.2.3", "v1.3.0")
	if err != nil || !newer {
		t.Fatal("newer minor version not detected")
	}
	newer, err = isNewerVersion("v0.3.4+11", "v0.4.0")
	if err != nil || !newer {
		t.Fatal("development commit count should not hide a newer release")
	}
	newer, err = isNewerVersion("v1.3.0", "v1.2.9")
	if err != nil || newer {
		t.Fatal("older version detected as newer")
	}
	if _, err = isNewerVersion("dev", "v1.3.0"); !errors.Is(err, errInvalidUpdateVersion) {
		t.Fatalf("invalid current version error = %v", err)
	}
	if _, err = isNewerVersion("v1.3.0", "latest"); !errors.Is(err, errInvalidUpdateVersion) {
		t.Fatalf("invalid latest version error = %v", err)
	}
}

// TestUpdateAvailableAgainstUnknownCurrentVersion 验证没有注入版本号的构建仍然能识别到正式 Release。
func TestUpdateAvailableAgainstUnknownCurrentVersion(t *testing.T) {
	available, err := updateAvailableAgainst("dev", "v1.3.0")
	if err != nil || !available {
		t.Fatalf("unknown current version should allow update: available = %v, err = %v", available, err)
	}
	available, err = updateAvailableAgainst("v1.3.0-dev", "v1.3.0")
	if err != nil || available {
		t.Fatalf("same source baseline should not report update: available = %v, err = %v", available, err)
	}
	available, err = updateAvailableAgainst("v1.3.0-dev+038932f", "v1.4.0")
	if err != nil || !available {
		t.Fatalf("newer release should be detected for dev build: available = %v, err = %v", available, err)
	}
	if _, err = updateAvailableAgainst("v1.3.0", "latest"); !errors.Is(err, errInvalidUpdateVersion) {
		t.Fatalf("invalid latest version error = %v", err)
	}
}

// TestSystemUpdateHandlerUpdate 验证对应功能场景。
func TestSystemUpdateHandlerUpdate(t *testing.T) {
	now := time.Now()
	recorder := &recordingSystemUpdater{fakeSystemUpdater: fakeSystemUpdater{
		status: updater.Status{
			RemoteURL:  "https://github.com/SuInk/Diana.git",
			NearestTag: "v1.2.3",
		},
		result: updater.Result{
			Status: updater.Status{
				Root:       "/tmp/repo",
				Branch:     "main",
				RemoteName: "origin",
				RemoteURL:  "https://github.com/example/repo.git",
			},
			Fetched: true,
			Updated: true,
			Output:  "Updating abc..def",
			At:      now,
		},
	}}
	handler := NewSystemUpdateHandler(recorder)
	github := releaseTestServer(t, "v1.3.0")
	defer github.Close()
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/system/update", strings.NewReader(`{"confirmation":"apply-update"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload updater.Result
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !payload.Fetched || !payload.Updated || payload.Output == "" {
		t.Fatalf("payload = %#v", payload)
	}
	if recorder.target != "v1.3.0" || recorder.force {
		t.Fatalf("release target = %q force=%v", recorder.target, recorder.force)
	}
}

func releaseTestServer(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/SuInk/Diana/releases") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"tag_name":"` + tag + `","name":"` + tag + `","published_at":"2026-08-09T10:00:00Z"}]`))
	}))
}

// systemUpdateTestRouter 封装当前模块的 systemUpdateTestRouter 逻辑。
func systemUpdateTestRouter(handler *SystemUpdateHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router)
	return router
}

// 「你源码在哪」不给真链接，模型就会编一个像模像样的 GitHub 地址。源码部署
// 跟着自己的 git 远端走，其余回落到官方仓库。
func TestSystemUpdateHandlerRepositoryURL(t *testing.T) {
	forked := NewSystemUpdateHandler(fakeSystemUpdater{
		status: updater.Status{Root: "/tmp/repo", RemoteURL: "https://github.com/example/repo.git"},
	})
	if got := forked.repositoryURL(context.Background()); got != "https://github.com/example/repo" {
		t.Fatalf("forked repository url = %q", got)
	}

	packaged := NewSystemUpdateHandler(fakeSystemUpdater{err: updater.ErrRemoteNotConfigured})
	if got := packaged.repositoryURL(context.Background()); got != "https://github.com/SuInk/Diana" {
		t.Fatalf("packaged repository url = %q", got)
	}
}
