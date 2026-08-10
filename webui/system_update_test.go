package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	status    updater.Status
	expected  string
	release   updater.ReleasePackage
	force     bool
	installed bool
}

func (r *recordingReleasePackageUpdater) Supported() bool { return true }

func (r *recordingReleasePackageUpdater) ExpectedAssetName() string { return r.expected }

func (r *recordingReleasePackageUpdater) Status(context.Context) (updater.Status, error) {
	return r.status, nil
}

func (r *recordingReleasePackageUpdater) Install(_ context.Context, release updater.ReleasePackage, force bool) (updater.Result, error) {
	r.release = release
	r.force = force
	r.installed = true
	status := r.status
	status.RestartRequired = true
	return updater.Result{Status: status, Fetched: true, Updated: true, Applied: true, RestartRequired: true, TargetCommit: release.Tag}, nil
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

	req := httptest.NewRequest(http.MethodPost, "/api/system/update", nil)
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
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/system/update/check", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"deployment_mode":"release"`) || !strings.Contains(body, `"latest_version":"v1.3.0"`) || !strings.Contains(body, `"update_available":true`) || !strings.Contains(body, `"checksum_available":true`) {
		t.Fatalf("body = %s", body)
	}
}

func TestSystemUpdateHandlerInstallsCompleteReleasePackage(t *testing.T) {
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

	updateRecorder := httptest.NewRecorder()
	router.ServeHTTP(updateRecorder, httptest.NewRequest(http.MethodPost, "/api/system/update", nil))
	if updateRecorder.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateRecorder.Code, updateRecorder.Body.String())
	}
	if !releaseUpdater.installed || releaseUpdater.force || releaseUpdater.release.Tag != "v1.3.0" || releaseUpdater.release.Archive.Name != assetName || releaseUpdater.release.Checksums.Name != "SHA256SUMS" {
		t.Fatalf("release updater = %#v", releaseUpdater)
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

func TestSystemUpdateHandlerForceUpdateRequiresConfirmation(t *testing.T) {
	handler := NewSystemUpdateHandler(fakeSystemUpdater{result: updater.Result{
		Status: updater.Status{HeadCommit: "abc1234"},
	}, status: updater.Status{RemoteURL: "https://github.com/SuInk/Diana.git", NearestTag: "v1.2.3"}})
	github := releaseTestServer(t, "v1.3.0")
	defer github.Close()
	handler.githubAPIBase = github.URL
	router := systemUpdateTestRouter(handler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/system/update", strings.NewReader(`{"force":true}`))
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
	if !isNewerVersion("v1.2.3", "v1.3.0") {
		t.Fatal("newer minor version not detected")
	}
	if !isNewerVersion("v0.3.4+11", "v0.4.0") {
		t.Fatal("development commit count should not hide a newer release")
	}
	if isNewerVersion("v1.3.0", "v1.2.9") || isNewerVersion("dev", "v1.3.0") {
		t.Fatal("older or invalid version detected as newer")
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

	req := httptest.NewRequest(http.MethodPost, "/api/system/update", nil)
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
