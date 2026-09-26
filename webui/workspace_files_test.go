// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/assistant"
	"github.com/gin-gonic/gin"
)

type workspaceTestRuntime struct{}

func (workspaceTestRuntime) ProfileConfigs() []assistant.BotConfig {
	return []assistant.BotConfig{{ID: "bot-a", Name: "小 A"}}
}

func (workspaceTestRuntime) CodingWorkspaceReferenced() func(string) bool {
	return func(string) bool { return false }
}

func newWorkspaceTestHandler(t *testing.T) (*WorkspaceFilesHandler, string) {
	t.Helper()
	root := t.TempDir()
	handler := NewWorkspaceFilesHandler(workspaceTestRuntime{})
	handler.root = func() string { return root }
	handler.now = func() time.Time { return time.Date(2026, 9, 26, 10, 0, 0, 0, time.Local) }
	return handler, root
}

func writeWorkspaceTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func serveWorkspace(router http.Handler, method, target, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(response, request)
	return response
}

// 工作目录接口和其他管理接口一样过 WebUI 登录：没登录一律 401，文件原样不动。
func TestWorkspaceFilesRequireConsoleAuthentication(t *testing.T) {
	auth := NewAuthManager(&memoryAuthStore{})
	if _, err := auth.Bootstrap("admin", "test-password"); err != nil {
		t.Fatal(err)
	}
	handler, root := newWorkspaceTestHandler(t)
	writeWorkspaceTestFile(t, root, "downloads/a.txt", "hello")
	writeWorkspaceTestFile(t, root, ".trash/20260901-000000/b.txt", "bye")
	router := gin.New()
	router.Use(auth.Middleware())
	handler.Register(router)
	for _, item := range []struct{ method, target, body string }{
		{http.MethodGet, "/api/workspace/files", ""},
		{http.MethodGet, "/api/workspace/download?path=downloads/a.txt", ""},
		{http.MethodPost, "/api/workspace/delete", `{"path":"downloads/a.txt"}`},
		{http.MethodPost, "/api/workspace/trash/empty", ""},
	} {
		if response := serveWorkspace(router, item.method, item.target, item.body); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录也能访问: %d", item.method, item.target, response.Code)
		}
	}
	for _, rel := range []string{"downloads/a.txt", ".trash/20260901-000000/b.txt"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("未登录的请求动了文件 %s", rel)
		}
	}
}

func TestWorkspaceFilesListDownloadDeleteAndEmptyTrash(t *testing.T) {
	handler, root := newWorkspaceTestHandler(t)
	router := gin.New()
	handler.Register(router)
	writeWorkspaceTestFile(t, root, "downloads/a.txt", "hello")
	writeWorkspaceTestFile(t, root, ".diana/extension-overrides.json", `{"secret":true}`)
	if _, err := agent.WriteWorkspaceBytes(agent.Config{WorkDir: root}, "keep/poster.txt", []byte("keep me"), agent.WorkspaceWriteOptions{Keep: &agent.KeepMeta{BotID: "bot-a", Description: "海报"}}); err != nil {
		t.Fatal(err)
	}

	response := serveWorkspace(router, http.MethodGet, "/api/workspace/files", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list: %d %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "extension-overrides") {
		t.Fatalf("列表里出现了运行时状态: %s", response.Body)
	}
	var listing agent.WorkspaceListing
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	var keep *agent.WorkspaceArea
	for i := range listing.Areas {
		if listing.Areas[i].Key == "keep" {
			keep = &listing.Areas[i]
		}
	}
	if keep == nil || keep.BotName != "小 A" || len(keep.Entries) != 1 || keep.Entries[0].Description != "海报" {
		t.Fatalf("长期区 = %+v", keep)
	}

	response = serveWorkspace(router, http.MethodGet, "/api/workspace/download?path="+url.QueryEscape("downloads/a.txt"), "")
	if response.Code != http.StatusOK || response.Body.String() != "hello" || !strings.Contains(response.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %d %q %v", response.Code, response.Body, response.Header())
	}

	response = serveWorkspace(router, http.MethodPost, "/api/workspace/delete", `{"path":"keep/bot-a/poster.txt"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), ".trash/") {
		t.Fatalf("delete: %d %s", response.Code, response.Body)
	}
	if entries, _ := agent.LoadKeepIndex(root, "bot-a"); len(entries) != 0 {
		t.Fatalf("删除后长期区索引没清: %+v", entries)
	}
	response = serveWorkspace(router, http.MethodPost, "/api/workspace/trash/empty", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"deleted_files":1`) {
		t.Fatalf("empty trash: %d %s", response.Code, response.Body)
	}
}

// 下载和删除走文件工具那一套路径校验：出不了工作目录，碰不到运行时配置和回收站内部。
func TestWorkspaceFilesRejectUnsafePaths(t *testing.T) {
	handler, root := newWorkspaceTestHandler(t)
	router := gin.New()
	handler.Register(router)
	outside := t.TempDir()
	writeWorkspaceTestFile(t, outside, "secret.txt", "top secret")
	writeWorkspaceTestFile(t, root, ".diana/extension-overrides.json", `{}`)
	writeWorkspaceTestFile(t, root, ".trash/20260901-000000/b.txt", "bye")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../secret.txt", filepath.Join(outside, "secret.txt"), "link.txt", ".diana/extension-overrides.json", ".diana", "downloads"} {
		response := serveWorkspace(router, http.MethodGet, "/api/workspace/download?path="+url.QueryEscape(rel), "")
		if response.Code == http.StatusOK || strings.Contains(response.Body.String(), "top secret") {
			t.Fatalf("download %s 放行了: %d %s", rel, response.Code, response.Body)
		}
	}
	for _, rel := range []string{"../secret.txt", ".diana", ".diana/extension-overrides.json", ".trash/20260901-000000/b.txt", "keep", "."} {
		body, _ := json.Marshal(map[string]string{"path": rel})
		response := serveWorkspace(router, http.MethodPost, "/api/workspace/delete", string(body))
		if response.Code == http.StatusOK {
			t.Fatalf("delete %s 放行了: %s", rel, response.Body)
		}
	}
	for _, path := range []string{filepath.Join(outside, "secret.txt"), filepath.Join(root, ".diana", "extension-overrides.json"), filepath.Join(root, ".trash", "20260901-000000", "b.txt")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s 被动了", path)
		}
	}
}

func TestStorageDirectoryKeys(t *testing.T) {
	for rel, want := range map[string]string{
		"diana.db":                         "database",
		"config.yaml":                      "files",
		"history-media/g1/a.jpg":           "history-media",
		"workspace/keep/bot-a/a.png":       "workspace/keep",
		"workspace/.trash/2026/x":          "workspace/.trash",
		"workspace/.agents/skills/x.md":    "workspace/skills",
		"workspace/random/x":               "workspace/other",
		"workspace/loose.txt":              "workspace/other",
		"workspace/.diana/keep-index/a.js": "workspace/.diana",
	} {
		category := storageCategoryKey(rel)
		if got := storageDirectoryKey(rel, category); got != want {
			t.Fatalf("storageDirectoryKey(%q) = %q, want %q", rel, got, want)
		}
	}
	measured := walkDirectoryBreakdown(func() string {
		dir := t.TempDir()
		writeWorkspaceTestFile(t, dir, "workspace/downloads/a.png", "12345")
		writeWorkspaceTestFile(t, dir, "history-media/b.jpg", "123")
		return dir
	}())
	if measured.dirBytes["workspace/downloads"] != 5 || measured.dirBytes["history-media"] != 3 {
		t.Fatalf("按目录统计 = %+v", measured.dirBytes)
	}
}

// workspaceStateTestPath 返回工作目录里 .diana/ 下的一份运行时状态文件路径，目录先建好。
func workspaceStateTestPath(t *testing.T, workDir, name string) string {
	t.Helper()
	dir := agent.DianaStateDir(workDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}
