// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

	// 假 GitHub 只提供 codeload 归档：安装器从同一份归档里读清单、入口和提交。
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]string{"diana.plugin.json": string(manifestJSON)}
	for name, body := range files {
		all[name] = body
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 4)
		if len(parts) == 4 && parts[2] == "tar.gz" && parts[3] == ref {
			_, _ = w.Write(repoPluginArchive(t, parts[0]+"-"+parts[1]+"-"+ref, testRepoCommit, all))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	dataDir := filepath.Dir(store.Path())
	installer := assistant.NewRepoPluginInstaller(dataDir, server.Client())
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

const testRepoCommit = "0123456789abcdef0123456789abcdef01234567"

// repoPluginArchive 按 GitHub codeload 的形态打包，pax 全局头 comment 里写提交 SHA。
func repoPluginArchive(t *testing.T, root, commit string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": commit}, Format: tar.FormatPAX}); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: root + "/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
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
	// ParseGitHubRepoURL 只认 github.com 链接形态；假 GitHub 由 ArchiveBase 接管实际请求。
	body := `{"url":"github.com/SuInk/diana-plugin-hello"}`
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/repo/preview", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var preview assistant.RepoPluginPreview
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Manifest.ID != "suink.hello" || preview.Commit != testRepoCommit {
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
	install := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/assistant/plugins/repo/install", strings.NewReader(body)))
		return rec
	}
	// 没有预览提交不能装：装的必须是确认框里那一版。
	if rec := install(`{"url":"github.com/SuInk/diana-plugin-hello","accept_risk":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少预览提交应拒绝: %d %s", rec.Code, rec.Body.String())
	}
	// 预览之后仓库有了新提交：拒绝并提示重新预览。
	if rec := install(`{"url":"github.com/SuInk/diana-plugin-hello","accept_risk":true,"commit":"fedcba9876543210fedcba9876543210fedcba98"}`); rec.Code != http.StatusConflict {
		t.Fatalf("提交不一致应返回 409: %d %s", rec.Code, rec.Body.String())
	}
	rec := install(`{"url":"github.com/SuInk/diana-plugin-hello","accept_risk":true,"commit":"` + testRepoCommit + `"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("安装: %d %s", rec.Code, rec.Body.String())
	}
	source, ok := sources.Get("suink.hello")
	if !ok || source.Commit != testRepoCommit {
		t.Fatalf("来源记录应带实际提交: %+v ok=%v", source, ok)
	}
	dir := filepath.Join(installer.DataDir, "plugin-sources", "suink.hello")
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatalf("落盘缺少 SKILL.md: %v", err)
	}
	if state, ok := h.runtime.Plugins().Get(source.ID); !ok || !state.Installed {
		t.Fatalf("安装后插件应已登记并安装: %+v ok=%v", state, ok)
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
