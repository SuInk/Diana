// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/browserctl"

	"github.com/gin-gonic/gin"
)

func newExtensionRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	registry := browserctl.NewRegistry(context.Background(), &memoryBrowserControlStore{})
	handler := NewBrowserControlHandler(registry, browserctl.NewHub(registry))
	router := gin.New()
	handler.Register(router)
	return router
}

// 容器部署的用户没有仓库检出，扩展只能从 WebUI 下载，所以这条链路要能打出
// 一个解压即可加载的 zip。
func TestBrowserControlExtensionDownloadServesZip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"manifest_version":3}`), 0o600); err != nil {
		t.Fatalf("写 manifest 失败：%v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("建子目录失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "policy.js"), []byte("export const x = 1;\n"), 0o600); err != nil {
		t.Fatalf("写子文件失败：%v", err)
	}
	t.Setenv("DIANA_BROWSER_EXTENSION_DIR", dir)

	recorder := httptest.NewRecorder()
	newExtensionRouter(t).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browser-control/extension.zip", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("下载应成功，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.Bytes()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("返回的不是合法 zip：%v", err)
	}
	names := map[string]bool{}
	for _, file := range reader.File {
		names[file.Name] = true
	}
	if !names["manifest.json"] || !names["sub/policy.js"] {
		t.Fatalf("zip 里应包含整个扩展目录，实际 %v", names)
	}
}

// 没带扩展源码的部署（比如只拷了二进制）要明确说没有，而不是给一个空 zip。
func TestBrowserControlExtensionDownloadMissingDirReports404(t *testing.T) {
	t.Setenv("DIANA_BROWSER_EXTENSION_DIR", filepath.Join(t.TempDir(), "not-there"))
	t.Chdir(t.TempDir())

	recorder := httptest.NewRecorder()
	newExtensionRouter(t).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browser-control/extension.zip", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("没有扩展源码时应报 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
}

// status 要告诉前端这个部署有没有下载入口，免得按钮点下去是 404。
func TestBrowserControlStatusReportsExtensionDownload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"manifest_version":3}`), 0o600); err != nil {
		t.Fatalf("写 manifest 失败：%v", err)
	}
	t.Setenv("DIANA_BROWSER_EXTENSION_DIR", dir)

	recorder := httptest.NewRecorder()
	newExtensionRouter(t).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/browser-control/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态接口应成功，得到 %d", recorder.Code)
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"extension_download":true`)) {
		t.Fatalf("状态里应报有扩展可下载，实际 %s", recorder.Body.String())
	}
}
