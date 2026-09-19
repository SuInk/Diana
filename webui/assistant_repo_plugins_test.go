// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

// newRepoPluginTestHandler 搭一个带本地假 GitHub 的 BotHandler。
func newRepoPluginTestHandler(t *testing.T, manifest map[string]any, files map[string]string, ref string) (*BotHandler, *assistant.RepoPluginInstaller, *assistant.RepoPluginStore, *httptest.Server, *storage.SQLiteStore) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// 复用模型层测试的假 GitHub：直接内联一个同等语义的本地服务。
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.SplitN(path, "/", 4)
		if len(parts) == 4 && parts[2] == ref {
			rel := parts[3]
			if rel == "diana.plugin.json" {
				_, _ = w.Write(manifestJSON)
				return
			}
			if body, ok := files[rel]; ok {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	dataDir := filepath.Dir(store.Path())
	installer := assistant.NewRepoPluginInstaller(dataDir, server.Client())
	installer.RawBase = server.URL
	installer.ArchiveBase = server.URL
	sources := assistant.NewRepoPluginStore(dataDir)
	if err := sources.Load(); err != nil {
		t.Fatal(err)
	}

	m := assistant.NewDefaultPluginManager()
	r := assistant.NewRuntime(assistant.BotConfig{ID: "qq-a"}, fakeChannel{}, m, nil, nil, nil, nil)
	h := NewBotHandlerWithFactory(ctx, r, nil)
	h.SetSQLiteStore(store)
	h.SetRepoPluginInstaller(installer)
	h.SetRepoPluginSourceStore(sources)
	return h, installer, sources, server, store
}

func repoPluginManifest() map[string]any {
	return map[string]any{
		"id":          "suink.hello",
		"name":        "示例插件",
		"version":     "1.0.0",
		"description": "一句话说明",
		"permissions": []any{"message:read"},
		"entry":       "SKILL.md",
	}
}

func repoPluginSkill() string {
	return "---\nname: hello\ndescription: 示例插件\n---\n\n回复问候。"
}

func TestBotHandlerRepoPluginPreview(t *testing.T) {
	h, _, _, _, _ := newRepoPluginTestHandler(t, repoPluginManifest(), map[string]string{
		"SKILL.md": repoPluginSkill(),
	}, "HEAD")
	router := botTestRouter(h)

	rec := httptest.NewRecorder()
	// ParseGitHubRepoURL 只认 github.com 链接形态；假 GitHub 由 RawBase 接管实际请求。
	body := `{"url":"github.com/SuInk/diana-plugin-hello"}`
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/repo/preview", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var preview assistant.RepoPluginPreview
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Manifest.ID != "suink.hello" {
		t.Fatalf("manifest = %+v", preview.Manifest)
	}
	if len(preview.Permissions) != 1 || preview.Permissions[0].Label == "" {
		t.Fatalf("permissions = %+v", preview.Permissions)
	}
	if !preview.Risk.FloatingRef || len(preview.Risk.Warnings) == 0 {
		t.Fatalf("risk = %+v", preview.Risk)
	}

	// 非法链接返回 400。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/repo/preview", strings.NewReader(`{"url":"https://gitee.com/a/b"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法链接: %d %s", rec.Code, rec.Body.String())
	}

	// 清单格式错误返回 400。
	bad := repoPluginManifest()
	bad["permissions"] = []any{}
	h2, _, _, _, _ := newRepoPluginTestHandler(t, bad, map[string]string{"SKILL.md": repoPluginSkill()}, "HEAD")
	rec = httptest.NewRecorder()
	botTestRouter(h2).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/repo/preview", strings.NewReader(`{"url":"github.com/SuInk/diana-plugin-hello"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("格式错误: %d %s", rec.Code, rec.Body.String())
	}
}

func TestBotHandlerRepoPluginInstallRequiresRiskAck(t *testing.T) {
	h, _, _, _, _ := newRepoPluginTestHandler(t, repoPluginManifest(), map[string]string{
		"SKILL.md": repoPluginSkill(),
	}, "HEAD")
	router := botTestRouter(h)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/repo/install", strings.NewReader(`{"url":"github.com/SuInk/diana-plugin-hello"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未确认风险应拒绝: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "风险") {
		t.Fatalf("错误信息应提示风险: %s", rec.Body.String())
	}
}

func TestBotHandlerRepoPluginInstallUninstallRoundTrip(t *testing.T) {
	h, installer, sources, _, store := newRepoPluginTestHandler(t, repoPluginManifest(), map[string]string{
		"SKILL.md": repoPluginSkill(),
	}, "HEAD")
	router := botTestRouter(h)
	// 假 GitHub 不提供归档下载，安装会失败；这里只验证 502 映射与风险校验路径。
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/repo/install", strings.NewReader(`{"url":"github.com/SuInk/diana-plugin-hello","accept_risk":true}`)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("归档下载失败应返回 502: %d %s", rec.Code, rec.Body.String())
	}

	// 直接落盘一个插件，走卸载清理路径验证来源记录与目录删除。
	source := assistant.RepoPluginSource{ID: "suink.hello", Owner: "SuInk", Repo: "diana-plugin-hello", Version: "1.0.0", URL: "https://github.com/SuInk/diana-plugin-hello"}
	if err := sources.Save(source); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(installer.DataDir, "plugin-sources", "suink.hello")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(repoPluginSkill()), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestJSON, err := json.Marshal(repoPluginManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "diana.plugin.json"), manifestJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	plugin, err := assistant.LoadRepoPlugin(installer.DataDir, source)
	if err != nil {
		t.Fatal(err)
	}
	manager := h.runtime.Plugins()
	if err := manager.RegisterPlugin(plugin); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(source.ID); err != nil {
		t.Fatal(err)
	}

	// 列表接口给第三方插件附带安装来源，更新入口据此渲染。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/plugins", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var listed []assistant.PluginState
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	var found *assistant.PluginState
	for i := range listed {
		if listed[i].Manifest.ID == source.ID {
			found = &listed[i]
		}
	}
	if found == nil || found.RepoSource == nil {
		t.Fatalf("列表应包含 repo_source: %+v", found)
	}
	if found.RepoSource.Owner != "SuInk" || found.RepoSource.Version != "1.0.0" {
		t.Fatalf("repo_source = %+v", found.RepoSource)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/suink.hello/uninstall", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("卸载: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("卸载后目录应删除")
	}
	if _, ok := sources.Get(source.ID); ok {
		t.Fatal("卸载后来源记录应删除")
	}
	// 卸载状态已持久化，重启恢复不会再出现。
	saved, ok, err := store.LoadPluginStates(context.Background())
	if err != nil || !ok {
		t.Fatalf("读取插件状态: %v ok=%v", err, ok)
	}
	if state, exists := saved[source.ID]; !exists || state.Installed {
		t.Fatalf("持久化状态 = %+v exists=%v", state, exists)
	}
}

func TestBotHandlerRepoPluginUpdateNotFound(t *testing.T) {
	h, _, _, _, _ := newRepoPluginTestHandler(t, repoPluginManifest(), map[string]string{
		"SKILL.md": repoPluginSkill(),
	}, "HEAD")
	router := botTestRouter(h)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/repo/update/suink.missing", strings.NewReader(`{"accept_risk":true}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非仓库插件更新: %d %s", rec.Code, rec.Body.String())
	}
}

func TestBotHandlerRepoPluginRoutesRequireInstaller(t *testing.T) {
	ctx := context.Background()
	m := assistant.NewDefaultPluginManager()
	r := assistant.NewRuntime(assistant.BotConfig{ID: "qq-a"}, fakeChannel{}, m, nil, nil, nil, nil)
	h := NewBotHandlerWithFactory(ctx, r, nil)
	router := botTestRouter(h)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/repo/preview", strings.NewReader(`{"url":"github.com/a/b"}`)))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("未注入安装器: %d %s", rec.Code, rec.Body.String())
	}
}
