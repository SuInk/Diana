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

	"github.com/gin-gonic/gin"
)

func newAgentWorkspaceTestRouter(t *testing.T, root string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewAgentWorkspaceHandler(func() string { return root }).Register(router)
	return router
}

func getWorkspace(t *testing.T, router *gin.Engine, endpoint, rel string, extra ...string) *httptest.ResponseRecorder {
	t.Helper()
	query := url.Values{"path": {rel}}
	for i := 0; i+1 < len(extra); i += 2 {
		query.Set(extra[i], extra[i+1])
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, endpoint+"?"+query.Encode(), nil))
	return recorder
}

func TestAgentWorkspaceListsDirectoriesFirst(t *testing.T) {
	root := t.TempDir()
	writeStorageSample(t, filepath.Join(root, "b.txt"), 3)
	writeStorageSample(t, filepath.Join(root, "A.png"), 5)
	writeStorageSample(t, filepath.Join(root, "coding", "repo", "main.go"), 7)
	router := newAgentWorkspaceTestRouter(t, root)

	recorder := getWorkspace(t, router, "/api/system/workspace", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var listing AgentWorkspaceListing
	if err := json.Unmarshal(recorder.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if !listing.Exists || listing.Path != "" || listing.Root != root {
		t.Fatalf("unexpected listing header %+v", listing)
	}
	var names []string
	for _, entry := range listing.Entries {
		names = append(names, entry.Kind+":"+entry.Name)
	}
	if got := strings.Join(names, ","); got != "dir:coding,file:A.png,file:b.txt" {
		t.Fatalf("unexpected order %s", got)
	}
	if listing.Entries[2].Size != 3 {
		t.Fatalf("file size missing: %+v", listing.Entries[2])
	}

	recorder = getWorkspace(t, router, "/api/system/workspace", "coding/repo")
	if err := json.Unmarshal(recorder.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.Path != "coding/repo" || len(listing.Entries) != 1 || listing.Entries[0].Path != "coding/repo/main.go" {
		t.Fatalf("unexpected nested listing %+v", listing)
	}
}

// 工作区要等 Agent 第一次写文件才会建出来，没建之前页面该说「还是空的」而不是报错。
func TestAgentWorkspaceMissingRootIsEmpty(t *testing.T) {
	router := newAgentWorkspaceTestRouter(t, filepath.Join(t.TempDir(), "workspace"))
	recorder := getWorkspace(t, router, "/api/system/workspace", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var listing AgentWorkspaceListing
	if err := json.Unmarshal(recorder.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.Exists || len(listing.Entries) != 0 {
		t.Fatalf("missing workspace should list as empty: %+v", listing)
	}
}

func TestAgentWorkspaceStaysInsideRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	writeStorageSample(t, filepath.Join(parent, "diana.db"), 10)
	writeStorageSample(t, filepath.Join(root, "note.txt"), 1)
	if err := os.Symlink(filepath.Join(parent, "diana.db"), filepath.Join(root, "db-link")); err != nil {
		t.Fatal(err)
	}
	router := newAgentWorkspaceTestRouter(t, root)

	for _, rel := range []string{"../diana.db", "coding/../../diana.db"} {
		if recorder := getWorkspace(t, router, "/api/system/workspace/file", rel); recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", rel, recorder.Code)
		}
	}
	// 链接指到工作区外面：列表里标成 link，打开被 os.Root 拦下。
	recorder := getWorkspace(t, router, "/api/system/workspace/file", "db-link")
	if recorder.Code == http.StatusOK {
		t.Fatalf("symlink escaping the workspace must not be served")
	}
	var listing AgentWorkspaceListing
	_ = json.Unmarshal(getWorkspace(t, router, "/api/system/workspace", "").Body.Bytes(), &listing)
	for _, entry := range listing.Entries {
		if entry.Name == "db-link" && (entry.Kind != "link" || !entry.Symlink) {
			t.Fatalf("escaping symlink should be listed as link: %+v", entry)
		}
	}
}

func TestAgentWorkspaceHidesRuntimeCredentials(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	router := newAgentWorkspaceTestRouter(t, root)
	recorder := getWorkspace(t, router, "/api/system/workspace/file", ".mcp.json")
	if recorder.Code != http.StatusForbidden || strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("credential file served: %d %s", recorder.Code, recorder.Body.String())
	}
	var listing AgentWorkspaceListing
	_ = json.Unmarshal(getWorkspace(t, router, "/api/system/workspace", "").Body.Bytes(), &listing)
	if len(listing.Entries) != 1 || !listing.Entries[0].Protected {
		t.Fatalf("credential file should be listed as protected: %+v", listing.Entries)
	}
}

// 文件是 Agent 写的，和控制台同源。HTML、SVG 只能当纯文本看源码，不能在页面里跑。
func TestAgentWorkspaceServesUntrustedFilesSafely(t *testing.T) {
	root := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	files := map[string][]byte{
		"shot.png":  png,
		"page.html": []byte("<html><script>alert(1)</script></html>"),
		"icon.svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"data.bin":  {0, 1, 2, 3, 0xff},
		"notes.md":  []byte("# hi"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	router := newAgentWorkspaceTestRouter(t, root)
	cases := []struct {
		name, contentType, disposition string
	}{
		{"shot.png", "image/png", "inline"},
		{"page.html", "text/plain; charset=utf-8", "inline"},
		{"icon.svg", "text/plain; charset=utf-8", "inline"},
		{"notes.md", "text/plain; charset=utf-8", "inline"},
		{"data.bin", "application/octet-stream", "attachment"},
	}
	for _, tc := range cases {
		recorder := getWorkspace(t, router, "/api/system/workspace/file", tc.name)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tc.name, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != tc.contentType {
			t.Fatalf("%s: content type %q", tc.name, got)
		}
		if got := recorder.Header().Get("Content-Disposition"); !strings.HasPrefix(got, tc.disposition) {
			t.Fatalf("%s: disposition %q", tc.name, got)
		}
		if recorder.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(recorder.Header().Get("Content-Security-Policy"), "sandbox") {
			t.Fatalf("%s: missing hardening headers %v", tc.name, recorder.Header())
		}
		if recorder.Body.String() != string(files[tc.name]) {
			t.Fatalf("%s: body mismatch", tc.name)
		}
	}
	recorder := getWorkspace(t, router, "/api/system/workspace/file", "shot.png", "download", "1")
	if got := recorder.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Fatalf("download=1 should force attachment, got %q", got)
	}
}
