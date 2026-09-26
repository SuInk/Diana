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

func newAgentWorkspaceTestHandler(root string) *AgentWorkspaceHandler {
	handler := NewAgentWorkspaceHandler(func() string { return root }, workspaceTestRuntime{})
	handler.now = func() time.Time { return time.Date(2026, 9, 26, 10, 0, 0, 0, time.Local) }
	return handler
}

func newAgentWorkspaceTestRouter(t *testing.T, root string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	newAgentWorkspaceTestHandler(root).Register(router)
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

func listWorkspaceForTest(t *testing.T, router *gin.Engine, rel string) AgentWorkspaceListing {
	t.Helper()
	recorder := getWorkspace(t, router, "/api/system/workspace", rel)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list %q: status %d: %s", rel, recorder.Code, recorder.Body.String())
	}
	var listing AgentWorkspaceListing
	if err := json.Unmarshal(recorder.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	return listing
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

func deleteWorkspacePath(router http.Handler, rel string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"path": rel})
	return serveWorkspace(router, http.MethodPost, "/api/workspace/delete", string(body))
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

func TestAgentWorkspaceListsDirectoriesFirst(t *testing.T) {
	root := t.TempDir()
	writeStorageSample(t, filepath.Join(root, "b.txt"), 3)
	writeStorageSample(t, filepath.Join(root, "A.png"), 5)
	writeStorageSample(t, filepath.Join(root, "coding", "repo", "main.go"), 7)
	router := newAgentWorkspaceTestRouter(t, root)

	listing := listWorkspaceForTest(t, router, "")
	if !listing.Exists || listing.Path != "" || listing.Root != root || listing.Area != nil {
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

	listing = listWorkspaceForTest(t, router, "coding/repo")
	if listing.Path != "coding/repo" || len(listing.Entries) != 1 || listing.Entries[0].Path != "coding/repo/main.go" {
		t.Fatalf("unexpected nested listing %+v", listing)
	}
}

// 工作区要等 Agent 第一次写文件才会建出来，没建之前页面该说「还是空的」而不是报错。
func TestAgentWorkspaceMissingRootIsEmpty(t *testing.T) {
	router := newAgentWorkspaceTestRouter(t, filepath.Join(t.TempDir(), "workspace"))
	listing := listWorkspaceForTest(t, router, "")
	if listing.Exists || len(listing.Entries) != 0 {
		t.Fatalf("missing workspace should list as empty: %+v", listing)
	}
	recorder := serveWorkspace(router, http.MethodGet, "/api/workspace/files", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"areas":[]`) {
		t.Fatalf("overview of missing workspace: %d %s", recorder.Code, recorder.Body)
	}
}

// 路径按字面走不出工作区，但 Agent 建的符号链接照常跟过去：能登录 WebUI 的只有
// 管理员，链到外面的东西本来就在他自己的机器上。
func TestAgentWorkspaceFollowsLinksButNotDotDot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	writeStorageSample(t, filepath.Join(parent, "outside", "report.txt"), 10)
	writeStorageSample(t, filepath.Join(root, "note.txt"), 1)
	if err := os.Symlink(filepath.Join(parent, "outside"), filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "gone"), filepath.Join(root, "broken-link")); err != nil {
		t.Fatal(err)
	}
	router := newAgentWorkspaceTestRouter(t, root)

	for _, rel := range []string{"../outside/report.txt", "coding/../../outside/report.txt"} {
		if recorder := getWorkspace(t, router, "/api/system/workspace/file", rel); recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", rel, recorder.Code)
		}
	}
	var listing AgentWorkspaceListing
	_ = json.Unmarshal(getWorkspace(t, router, "/api/system/workspace", "").Body.Bytes(), &listing)
	kinds := map[string]AgentWorkspaceEntry{}
	for _, entry := range listing.Entries {
		kinds[entry.Name] = entry
	}
	if got := kinds["outside-link"]; got.Kind != "dir" || !got.Symlink {
		t.Fatalf("directory symlink should list as dir: %+v", got)
	}
	if got := kinds["broken-link"]; got.Kind != "link" || !got.Symlink {
		t.Fatalf("broken symlink should list as link: %+v", got)
	}
	_ = json.Unmarshal(getWorkspace(t, router, "/api/system/workspace", "outside-link").Body.Bytes(), &listing)
	if len(listing.Entries) != 1 || listing.Entries[0].Path != "outside-link/report.txt" {
		t.Fatalf("listing through symlink failed: %+v", listing)
	}
	if recorder := getWorkspace(t, router, "/api/system/workspace/file", "outside-link/report.txt"); recorder.Code != http.StatusOK || recorder.Body.Len() != 10 {
		t.Fatalf("file through symlink: %d", recorder.Code)
	}
}

// 凭据配置照常给看，只在列表里打标记提醒里面是明文令牌。
func TestAgentWorkspaceMarksRuntimeCredentials(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	router := newAgentWorkspaceTestRouter(t, root)
	recorder := getWorkspace(t, router, "/api/system/workspace/file", ".mcp.json")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("credential file should be viewable: %d %s", recorder.Code, recorder.Body.String())
	}
	var listing AgentWorkspaceListing
	_ = json.Unmarshal(getWorkspace(t, router, "/api/system/workspace", "").Body.Bytes(), &listing)
	if len(listing.Entries) != 1 || !listing.Entries[0].Protected {
		t.Fatalf("credential file should be marked: %+v", listing.Entries)
	}
}

// 管理员什么都能看：.diana/ 下的运行时状态（含长期区索引）照常列出、能打开，打上
// protected 标记；指到工作区外面的链接标成 external，照样跟过去。
func TestAgentWorkspaceShowsRuntimeFilesAndExternalLinks(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	writeWorkspaceTestFile(t, root, ".diana/keep-index/bot-a.json", `{"entries":[]}`)
	writeWorkspaceTestFile(t, root, "coding-runtime/auth/auth.json", `{"token":"secret"}`)
	writeWorkspaceTestFile(t, root, "notes/a.txt", "note")
	writeStorageSample(t, filepath.Join(parent, "outside", "report.txt"), 10)
	for link, target := range map[string]string{
		"outside-link": filepath.Join(parent, "outside"),
		"notes-link":   filepath.Join(root, "notes"),
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	router := newAgentWorkspaceTestRouter(t, root)

	entries := map[string]AgentWorkspaceEntry{}
	for _, entry := range listWorkspaceForTest(t, router, "").Entries {
		entries[entry.Name] = entry
	}
	if got := entries[".diana"]; got.Kind != "dir" || !got.Protected {
		t.Fatalf(".diana should be listed and marked: %+v", got)
	}
	if got := entries["notes"]; got.Protected {
		t.Fatalf("ordinary directory marked protected: %+v", got)
	}
	if got := entries["outside-link"]; got.Kind != "dir" || !got.Symlink || !got.External {
		t.Fatalf("outside link should be followed and marked external: %+v", got)
	}
	if got := entries["notes-link"]; got.Kind != "dir" || !got.Symlink || got.External {
		t.Fatalf("link inside the workspace is not external: %+v", got)
	}
	index := listWorkspaceForTest(t, router, ".diana/keep-index")
	if len(index.Entries) != 1 || !index.Entries[0].Protected {
		t.Fatalf("keep index listing = %+v", index.Entries)
	}
	for rel, want := range map[string]string{".diana/keep-index/bot-a.json": "entries", "coding-runtime/auth/auth.json": "secret"} {
		if recorder := getWorkspace(t, router, "/api/system/workspace/file", rel); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("%s should be viewable: %d %s", rel, recorder.Code, recorder.Body)
		}
	}
	if recorder := getWorkspace(t, router, "/api/system/workspace/file", "outside-link/report.txt"); recorder.Code != http.StatusOK || recorder.Body.Len() != 10 {
		t.Fatalf("file through outside link: %d", recorder.Code)
	}
}

// 长期区的条目带上索引里的说明，当前目录带上分区和清理规则。
func TestAgentWorkspaceListsKeepDescriptionsAndArea(t *testing.T) {
	root := t.TempDir()
	if _, err := agent.WriteWorkspaceBytes(agent.Config{WorkDir: root}, "keep/poster.txt", []byte("keep me"), agent.WorkspaceWriteOptions{Keep: &agent.KeepMeta{BotID: "bot-a", Description: "活动海报", SavedBy: "主人"}}); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceTestFile(t, root, "keep/bot-a/plain.txt", "no index")
	writeWorkspaceTestFile(t, root, "downloads/a.txt", "hello")
	router := newAgentWorkspaceTestRouter(t, root)

	listing := listWorkspaceForTest(t, router, "keep/bot-a")
	if listing.Area == nil || listing.Area.Key != "keep" || listing.Area.BotID != "bot-a" || listing.Area.BotName != "小 A" || listing.Area.Retention != "不自动清理" {
		t.Fatalf("keep area = %+v", listing.Area)
	}
	byName := map[string]AgentWorkspaceEntry{}
	for _, entry := range listing.Entries {
		byName[entry.Name] = entry
	}
	if got := byName["poster.txt"]; got.Description != "活动海报" || got.SavedBy != "主人" || got.SavedAt.IsZero() {
		t.Fatalf("keep entry = %+v", got)
	}
	if got := byName["plain.txt"]; got.Description != "" {
		t.Fatalf("entry without index should have no description: %+v", got)
	}
	listing = listWorkspaceForTest(t, router, "downloads")
	if listing.Area == nil || listing.Area.Key != "downloads" || listing.Area.Retention != "7 天后自动清理" {
		t.Fatalf("downloads area = %+v", listing.Area)
	}
}

// 文件是 Agent 写的，和控制台同源。常用的图片、音视频和 PDF 直接打开；HTML 只给源码，
// 所有响应都不让脚本在控制台的源里跑。
func TestAgentWorkspaceServesUntrustedFilesSafely(t *testing.T) {
	root := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	files := map[string][]byte{
		"shot.png":  png,
		"page.html": []byte("<html><script>alert(1)</script></html>"),
		"icon.svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"data.bin":  {0, 1, 2, 3, 0xff},
		"notes.md":  []byte("# hi"),
		"clip.MP4":  []byte("\x00\x00\x00\x18ftypmp42"),
		"song.mp3":  []byte("ID3\x03"),
		"doc.pdf":   []byte("%PDF-1.7\n"),
		"noext":     png,
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
		{"icon.svg", "image/svg+xml", "inline"},
		{"notes.md", "text/plain; charset=utf-8", "inline"},
		{"clip.MP4", "video/mp4", "inline"},
		{"song.mp3", "audio/mpeg", "inline"},
		{"doc.pdf", "application/pdf", "inline"},
		{"noext", "image/png", "inline"},
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
		// PDF 带 sandbox 会被浏览器拒绝用内置阅读器打开，只有它不带。
		wantSandbox := tc.contentType != "application/pdf"
		if recorder.Header().Get("X-Content-Type-Options") != "nosniff" || strings.Contains(recorder.Header().Get("Content-Security-Policy"), "sandbox") != wantSandbox {
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
	if recorder := getWorkspace(t, router, "/api/system/workspace/file", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("root is not a file: %d", recorder.Code)
	}
}

// 概览按分区合计，长期区对上机器人名字；运行时状态不出现。
func TestAgentWorkspaceOverview(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceTestFile(t, root, "downloads/a.txt", "hello")
	writeWorkspaceTestFile(t, root, ".diana/extension-overrides.json", `{"secret":true}`)
	writeWorkspaceTestFile(t, root, "loose.txt", "x")
	if _, err := agent.WriteWorkspaceBytes(agent.Config{WorkDir: root}, "keep/poster.txt", []byte("keep me"), agent.WorkspaceWriteOptions{Keep: &agent.KeepMeta{BotID: "bot-a", Description: "海报"}}); err != nil {
		t.Fatal(err)
	}
	router := newAgentWorkspaceTestRouter(t, root)
	response := serveWorkspace(router, http.MethodGet, "/api/workspace/files", "")
	if response.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "extension-overrides") {
		t.Fatalf("概览里出现了运行时状态: %s", response.Body)
	}
	var overview agent.WorkspaceOverview
	if err := json.Unmarshal(response.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	areas := map[string]agent.WorkspaceArea{}
	for _, area := range overview.Areas {
		areas[area.Key] = area
	}
	if keep := areas["keep"]; keep.BotName != "小 A" || keep.Files != 1 || keep.Bytes != 7 || keep.QuotaBytes != agent.KeepQuotaBytes {
		t.Fatalf("keep = %+v", keep)
	}
	if downloads := areas["downloads"]; downloads.Files != 1 || downloads.Path != "downloads" {
		t.Fatalf("downloads = %+v", downloads)
	}
	if len(overview.Loose) != 1 || overview.Loose[0].Path != "loose.txt" {
		t.Fatalf("loose = %+v", overview.Loose)
	}
}

// 删除挪进回收站，长期区索引跟着清；回收站里的东西不单条删，只能整个清空。
func TestAgentWorkspaceDeleteMovesToTrashAndEmptyTrash(t *testing.T) {
	root := t.TempDir()
	if _, err := agent.WriteWorkspaceBytes(agent.Config{WorkDir: root}, "keep/poster.txt", []byte("keep me"), agent.WorkspaceWriteOptions{Keep: &agent.KeepMeta{BotID: "bot-a", Description: "海报"}}); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceTestFile(t, root, "downloads/batch/a.txt", "a")
	writeWorkspaceTestFile(t, root, "downloads/batch/b.txt", "b")
	router := newAgentWorkspaceTestRouter(t, root)

	response := deleteWorkspacePath(router, "keep/bot-a/poster.txt")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), ".trash/") {
		t.Fatalf("delete file: %d %s", response.Code, response.Body)
	}
	if entries, _ := agent.LoadKeepIndex(root, "bot-a"); len(entries) != 0 {
		t.Fatalf("删除后长期区索引没清: %+v", entries)
	}
	if response := deleteWorkspacePath(router, "downloads/batch"); response.Code != http.StatusOK {
		t.Fatalf("delete dir: %d %s", response.Code, response.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "downloads", "batch")); !os.IsNotExist(err) {
		t.Fatalf("目录没挪走: %v", err)
	}
	// 凭据和 .diana/ 里的文件管理员也能删，照样挪进回收站。
	writeWorkspaceTestFile(t, root, ".mcp.json", `{"token":"secret"}`)
	writeWorkspaceTestFile(t, root, ".diana/extension-overrides.json", `{}`)
	for _, rel := range []string{".mcp.json", ".diana/extension-overrides.json"} {
		if response := deleteWorkspacePath(router, rel); response.Code != http.StatusOK {
			t.Fatalf("delete %s: %d %s", rel, response.Code, response.Body)
		}
	}
	// 删链接挪走的是链接本身，指向的东西原样留着。
	outside := t.TempDir()
	writeWorkspaceTestFile(t, outside, "keep-me.txt", "outside")
	if err := os.Symlink(filepath.Join(outside, "keep-me.txt"), filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if response := deleteWorkspacePath(router, "outside-link"); response.Code != http.StatusOK {
		t.Fatalf("delete link: %d %s", response.Code, response.Body)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep-me.txt")); err != nil {
		t.Fatalf("删链接把目标也挪走了: %v", err)
	}
	trash := listWorkspaceForTest(t, router, ".trash")
	if trash.Area == nil || trash.Area.Key != "trash" || len(trash.Entries) == 0 {
		t.Fatalf("trash listing = %+v", trash)
	}
	for _, rel := range []string{trash.Entries[0].Path, "keep", ".", "", "../x", "downloads/missing.txt"} {
		if response := deleteWorkspacePath(router, rel); response.Code == http.StatusOK {
			t.Fatalf("delete %q 放行了: %s", rel, response.Body)
		}
	}
	response = serveWorkspace(router, http.MethodPost, "/api/workspace/trash/empty", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"deleted_files":6`) {
		t.Fatalf("empty trash: %d %s", response.Code, response.Body)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, agent.WorkspaceTrashDir)); len(entries) != 0 {
		t.Fatalf("回收站没清空: %v", entries)
	}
}

// 不能整个删的目录（keep/、keep/<机器人>/、.trash/）按真实文件认，不按字面：大小写不
// 敏感的文件系统上 Keep 就是 keep，工作区里 loop -> . 的链接也能绕到长期区根目录。经
// 别名删掉的长期区文件，索引按真实位置清。
func TestAgentWorkspaceDeleteGuardsAliasesOfProtectedRoots(t *testing.T) {
	root := t.TempDir()
	if _, err := agent.WriteWorkspaceBytes(agent.Config{WorkDir: root}, "keep/poster.txt", []byte("keep me"), agent.WorkspaceWriteOptions{Keep: &agent.KeepMeta{BotID: "bot-a", Description: "海报"}}); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceTestFile(t, root, ".trash/20260901-000000/b.txt", "bye")
	if err := os.Symlink(".", filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	router := newAgentWorkspaceTestRouter(t, root)
	for _, rel := range []string{"Keep", "KEEP/bot-a", ".Trash", "loop/keep", "loop/keep/bot-a", "loop/.trash", "loop/.trash/20260901-000000", "loop/.trash/20260901-000000/b.txt"} {
		response := deleteWorkspacePath(router, rel)
		if response.Code == http.StatusOK {
			t.Fatalf("delete %s 放行了: %s", rel, response.Body)
		}
	}
	// 大小写不敏感的文件系统上（macOS、Windows 默认），Keep 真的能打开，必须是被规则挡下
	// 而不是恰好找不到。
	if _, err := os.Stat(filepath.Join(root, "KEEP")); err == nil {
		if response := deleteWorkspacePath(router, "Keep"); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "长期保存区的根目录") {
			t.Fatalf("case variant: %d %s", response.Code, response.Body)
		}
	}
	for _, rel := range []string{"keep/bot-a/poster.txt", ".trash/20260901-000000/b.txt"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s 被挪走了: %v", rel, err)
		}
	}
	response := deleteWorkspacePath(router, "loop/keep/bot-a/poster.txt")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "/keep/bot-a/poster.txt") || strings.Contains(response.Body.String(), "loop") {
		t.Fatalf("delete via alias: %d %s", response.Code, response.Body)
	}
	if entries, _ := agent.LoadKeepIndex(root, "bot-a"); len(entries) != 0 {
		t.Fatalf("经别名删除后长期区索引没清: %+v", entries)
	}
}

// 别名指到 keep/<机器人>/ 下面的子目录（ksub -> keep/bot-a/sub），或者写法大小写不同：
// 索引按真实位置清，回收站里也按真实位置放。
func TestAgentWorkspaceDeleteViaSubdirAliasCleansKeepIndex(t *testing.T) {
	root := t.TempDir()
	cfg := agent.Config{WorkDir: root}
	for _, rel := range []string{"keep/sub/b.txt", "keep/c.txt"} {
		if _, err := agent.WriteWorkspaceBytes(cfg, rel, []byte("keep me"), agent.WorkspaceWriteOptions{Keep: &agent.KeepMeta{BotID: "bot-a", Description: rel}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("keep", "bot-a", "sub"), filepath.Join(root, "ksub")); err != nil {
		t.Fatal(err)
	}
	router := newAgentWorkspaceTestRouter(t, root)
	response := deleteWorkspacePath(router, "ksub/b.txt")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "/keep/bot-a/sub/b.txt") {
		t.Fatalf("delete via subdir alias: %d %s", response.Code, response.Body)
	}
	entries, _ := agent.LoadKeepIndex(root, "bot-a")
	if len(entries) != 1 || entries[0].Path != "keep/bot-a/c.txt" {
		t.Fatalf("经子目录别名删除后索引 = %+v", entries)
	}
	if _, err := os.Stat(filepath.Join(root, "ksub")); err != nil {
		t.Fatalf("别名本身不该被挪走: %v", err)
	}
	// 大小写不敏感的文件系统上 KEEP/BOT-A/C.TXT 就是 keep/bot-a/c.txt。
	if _, err := os.Stat(filepath.Join(root, "KEEP")); err == nil {
		response := deleteWorkspacePath(router, "KEEP/BOT-A/C.TXT")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "/keep/bot-a/c.txt") {
			t.Fatalf("delete via case variant: %d %s", response.Code, response.Body)
		}
		if entries, _ := agent.LoadKeepIndex(root, "bot-a"); len(entries) != 0 {
			t.Fatalf("按大小写变体删除后索引没清: %+v", entries)
		}
	}
}

// 经外部链接进到工作区外面的目录：列表标 external，删除给一句中文说明，不再弹英文原话。
func TestAgentWorkspaceExternalDirectoryIsReadOnly(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	writeStorageSample(t, filepath.Join(parent, "outside", "report.txt"), 10)
	writeWorkspaceTestFile(t, root, "notes/a.txt", "note")
	if err := os.Symlink(filepath.Join(parent, "outside"), filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	router := newAgentWorkspaceTestRouter(t, root)
	if listing := listWorkspaceForTest(t, router, "outside-link"); !listing.External {
		t.Fatalf("listing through outside link should be external: %+v", listing)
	}
	if listing := listWorkspaceForTest(t, router, "notes"); listing.External {
		t.Fatalf("ordinary directory marked external: %+v", listing)
	}
	response := deleteWorkspacePath(router, "outside-link/report.txt")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "工作目录外面") || strings.Contains(response.Body.String(), "escapes") {
		t.Fatalf("delete outside file: %d %s", response.Code, response.Body)
	}
	if _, err := os.Stat(filepath.Join(parent, "outside", "report.txt")); err != nil {
		t.Fatalf("工作区外的文件被动了: %v", err)
	}
}

// 概览里分区卡片总在，目录还没建出来时点进去是「还没有文件」，不是 404。
func TestAgentWorkspaceMissingAreaDirectoryIsEmpty(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceTestFile(t, root, "notes.md", "# hi")
	router := newAgentWorkspaceTestRouter(t, root)
	for _, rel := range []string{"downloads", "keep", ".trash", ".agent-browser"} {
		listing := listWorkspaceForTest(t, router, rel)
		if !listing.Missing || len(listing.Entries) != 0 || listing.Area == nil {
			t.Fatalf("%s: %+v", rel, listing)
		}
	}
	if recorder := getWorkspace(t, router, "/api/system/workspace", "no-such-dir"); recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown directory should still be 404, got %d", recorder.Code)
	}
}

// 文件页的接口和其他管理接口一样过 WebUI 登录：没登录一律 401，文件原样不动。
func TestAgentWorkspaceRequiresConsoleAuthentication(t *testing.T) {
	auth := NewAuthManager(&memoryAuthStore{})
	if _, err := auth.Bootstrap("admin", "test-password"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeWorkspaceTestFile(t, root, "downloads/a.txt", "hello")
	writeWorkspaceTestFile(t, root, ".trash/20260901-000000/b.txt", "bye")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(auth.Middleware())
	newAgentWorkspaceTestHandler(root).Register(router)
	for _, item := range []struct{ method, target, body string }{
		{http.MethodGet, "/api/system/workspace?path=downloads", ""},
		{http.MethodGet, "/api/system/workspace/file?path=downloads/a.txt", ""},
		{http.MethodGet, "/api/system/workspace/file?path=downloads/a.txt&download=1", ""},
		{http.MethodGet, "/api/workspace/files", ""},
		{http.MethodPost, "/api/workspace/delete", `{"path":"downloads/a.txt"}`},
		{http.MethodPost, "/api/workspace/trash/empty", ""},
	} {
		response := serveWorkspace(router, item.method, item.target, item.body)
		if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "hello") {
			t.Fatalf("%s %s 未登录也能访问: %d", item.method, item.target, response.Code)
		}
	}
	for _, rel := range []string{"downloads/a.txt", ".trash/20260901-000000/b.txt"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("未登录的请求动了文件 %s", rel)
		}
	}
}
