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

type countingUpdater struct {
	fakeSystemUpdater
	updates int
}

// Update 记录调用次数并转发到假实现。
func (c *countingUpdater) Update(ctx context.Context) (updater.Result, error) {
	c.updates++
	return c.fakeSystemUpdater.Update(ctx)
}

type memorySettingsStore struct {
	settings updater.Settings
	saved    bool
}

// LoadUpdateSettings 返回内存中的设置。
func (m *memorySettingsStore) LoadUpdateSettings(context.Context) (updater.Settings, bool, error) {
	return m.settings, m.saved, nil
}

// SaveUpdateSettings 保存设置到内存。
func (m *memorySettingsStore) SaveUpdateSettings(_ context.Context, settings updater.Settings) error {
	m.settings = settings
	m.saved = true
	return nil
}

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

// TestAutoUpdaterTickRespectsToggleAndInterval 验证对应功能场景。
func TestAutoUpdaterTickRespectsToggleAndInterval(t *testing.T) {
	fake := &countingUpdater{fakeSystemUpdater: fakeSystemUpdater{
		result: updater.Result{Updated: true, Status: updater.Status{HeadCommit: "abc1234"}},
	}}
	auto := NewAutoUpdater(fake, &memorySettingsStore{}, nil)

	if settings := auto.Settings(); !settings.AutoUpdateEnabled || settings.IntervalMinutes != 30 {
		t.Fatalf("default settings = %+v", settings)
	}

	// 显式关闭后 tick 不触发更新。
	if _, err := auto.SaveSettings(context.Background(), updater.Settings{AutoUpdateEnabled: false, IntervalMinutes: 30}); err != nil {
		t.Fatalf("SaveSettings(disabled) error = %v", err)
	}
	auto.tick(context.Background())
	if fake.updates != 0 {
		t.Fatalf("disabled auto updater ran %d times", fake.updates)
	}

	// 开启后立即触发一次，且间隔内不重复执行。
	if _, err := auto.SaveSettings(context.Background(), updater.Settings{AutoUpdateEnabled: true, IntervalMinutes: 60}); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	auto.tick(context.Background())
	auto.tick(context.Background())
	if fake.updates != 1 {
		t.Fatalf("updates = %d, want 1", fake.updates)
	}
	if runAt, result, _ := auto.LastRun(); runAt.IsZero() || !strings.Contains(result, "abc1234") {
		t.Fatalf("LastRun() = %v %q", runAt, result)
	}

	// 手动把上次执行时间拨回超过间隔，应再次执行。
	auto.mu.Lock()
	auto.lastRunAt = time.Now().Add(-2 * time.Hour)
	auto.mu.Unlock()
	auto.tick(context.Background())
	if fake.updates != 2 {
		t.Fatalf("updates = %d, want 2", fake.updates)
	}
}

func TestAutoUpdaterTreatsMissingRemoteAsManagedDeployment(t *testing.T) {
	fake := &countingUpdater{fakeSystemUpdater: fakeSystemUpdater{err: updater.ErrRemoteNotConfigured}}
	auto := NewAutoUpdater(fake, &memorySettingsStore{}, nil)

	auto.tick(context.Background())
	_, result, lastError := auto.LastRun()
	if fake.updates != 1 || result != "由部署环境管理更新" || lastError != "" {
		t.Fatalf("updates=%d result=%q error=%q", fake.updates, result, lastError)
	}
}

// TestUpdateSettingsEndpointsRoundtrip 验证对应功能场景。
func TestUpdateSettingsEndpointsRoundtrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &memorySettingsStore{}
	auto := NewAutoUpdater(&countingUpdater{}, store, nil)
	handler := NewSystemUpdateHandler(fakeSystemUpdater{})
	handler.SetAutoUpdater(auto)
	router := gin.New()
	handler.Register(router)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"auto_update_enabled":true,"interval_minutes":3}`)
	req := httptest.NewRequest(http.MethodPost, "/api/system/update/settings", body)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST settings = %d: %s", rec.Code, rec.Body.String())
	}
	var saved struct {
		Settings updater.Settings `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// interval 3 低于下限，应被收敛到 10。
	if !saved.Settings.AutoUpdateEnabled || saved.Settings.IntervalMinutes != 10 {
		t.Fatalf("saved = %+v", saved.Settings)
	}
	if !store.saved || store.settings.IntervalMinutes != 10 {
		t.Fatalf("store = %+v", store.settings)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/update/settings", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"interval_minutes":10`) {
		t.Fatalf("GET settings = %d: %s", rec.Code, rec.Body.String())
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

// TestRollbackEndpointDisablesAutoUpdate 验证对应功能场景。
func TestRollbackEndpointDisablesAutoUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &memorySettingsStore{}
	auto := NewAutoUpdater(&countingUpdater{}, store, nil)
	if _, err := auto.SaveSettings(context.Background(), updater.Settings{AutoUpdateEnabled: true, IntervalMinutes: 30}); err != nil {
		t.Fatal(err)
	}
	handler := NewSystemUpdateHandler(fakeSystemUpdater{result: updater.Result{Status: updater.Status{HeadCommit: "de9c9be"}}})
	handler.SetAutoUpdater(auto)
	router := gin.New()
	handler.Register(router)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/system/update/rollback", strings.NewReader(`{"ref":"de9c9be"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"auto_update_disabled":true`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if auto.Settings().AutoUpdateEnabled {
		t.Fatal("auto update should be paused after rollback")
	}
	// 非法 ref 直接 400。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/system/update/rollback", strings.NewReader(`{"ref":""}`))
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
