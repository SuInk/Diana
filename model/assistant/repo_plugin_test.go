// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGitHubRepoURL(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    RepoPluginRef
		wantErr bool
	}{
		{name: "完整链接", raw: "https://github.com/SuInk/diana-plugin-hello", want: RepoPluginRef{Owner: "SuInk", Repo: "diana-plugin-hello"}},
		{name: "短链自动补协议", raw: "github.com/SuInk/diana-plugin-hello", want: RepoPluginRef{Owner: "SuInk", Repo: "diana-plugin-hello"}},
		{name: "固定 tag", raw: "https://github.com/SuInk/diana-plugin-hello/tree/v1.0.0", want: RepoPluginRef{Owner: "SuInk", Repo: "diana-plugin-hello", Ref: "v1.0.0"}},
		{name: "固定 commit", raw: "https://github.com/SuInk/diana-plugin-hello/tree/0123456789abcdef0123456789abcdef01234567", want: RepoPluginRef{Owner: "SuInk", Repo: "diana-plugin-hello", Ref: "0123456789abcdef0123456789abcdef01234567"}},
		{name: "空串", raw: "   ", wantErr: true},
		{name: "非 github 域名", raw: "https://gitee.com/SuInk/hello", wantErr: true},
		{name: "blob 路径", raw: "https://github.com/SuInk/hello/blob/main/a.json", wantErr: true},
		{name: "只有 owner", raw: "https://github.com/SuInk", wantErr: true},
		{name: "多余层级", raw: "https://github.com/SuInk/hello/tree/a/b", wantErr: true},
		{name: "http 不加密", raw: "http://github.com/SuInk/hello", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseGitHubRepoURL(tc.raw)
			if tc.wantErr {
				if !errors.Is(err, ErrRepoPluginURL) {
					t.Fatalf("err = %v, 想要 ErrRepoPluginURL", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDecodeRepoPluginManifestRejectsUnknownFields(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{name: "合法清单", json: `{"id":"suink.hello","name":"示例","version":"1.0.0","description":"说明","permissions":["message:read"],"entry":"SKILL.md"}`},
		{name: "内置字段 official", json: `{"id":"suink.hello","name":"示例","version":"1.0.0","description":"说明","permissions":["message:read"],"entry":"SKILL.md","official":true}`, wantErr: true},
		{name: "内置字段 built_in", json: `{"id":"suink.hello","name":"示例","version":"1.0.0","description":"说明","permissions":["message:read"],"entry":"SKILL.md","built_in":true}`, wantErr: true},
		{name: "未知字段", json: `{"id":"suink.hello","name":"示例","version":"1.0.0","description":"说明","permissions":["message:read"],"entry":"SKILL.md","can_ask_agent":true}`, wantErr: true},
		{name: "两份 JSON", json: `{"id":"suink.hello","name":"示例","version":"1.0.0","description":"说明","permissions":["message:read"],"entry":"SKILL.md"} {}`, wantErr: true},
		{name: "坏 JSON", json: `{`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeRepoPluginManifest([]byte(tc.json))
			if tc.wantErr {
				if !errors.Is(err, ErrRepoPluginFormat) {
					t.Fatalf("err = %v, 想要 ErrRepoPluginFormat", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func validManifestJSON(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	raw := map[string]any{
		"id":          "suink.hello",
		"name":        "示例插件",
		"version":     "1.0.0",
		"description": "一句话说明",
		"permissions": []any{"message:read", "network:https"},
		"entry":       "SKILL.md",
	}
	if mutate != nil {
		mutate(raw)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRepoPluginManifestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(map[string]any)
		wantErr string
	}{
		{name: "合法", mutate: nil},
		{name: "official 前缀", mutate: func(m map[string]any) { m["id"] = "official.hello" }, wantErr: "official"},
		{name: "ID 大写", mutate: func(m map[string]any) { m["id"] = "Suink.Hello" }, wantErr: "id"},
		{name: "版本带 v 前缀", mutate: func(m map[string]any) { m["version"] = "v1.0.0" }, wantErr: "version"},
		{name: "版本只有两位", mutate: func(m map[string]any) { m["version"] = "1.0" }, wantErr: "version"},
		{name: "权限为空", mutate: func(m map[string]any) { m["permissions"] = []any{} }, wantErr: "permissions"},
		{name: "未知权限", mutate: func(m map[string]any) { m["permissions"] = []any{"message:read", "root:everything"} }, wantErr: "未知权限"},
		{name: "未知平台", mutate: func(m map[string]any) { m["platforms"] = []any{"carrier-pigeon"} }, wantErr: "平台"},
		{name: "entry 不是 SKILL.md", mutate: func(m map[string]any) { m["entry"] = "main.go" }, wantErr: "entry"},
		{name: "min_diana 非法", mutate: func(m map[string]any) { m["min_diana"] = "latest" }, wantErr: "min_diana"},
		{name: "homepage 非 http", mutate: func(m map[string]any) { m["homepage"] = "ftp://x" }, wantErr: "homepage"},
		{
			name: "select 无 options",
			mutate: func(m map[string]any) {
				m["settings"] = []any{map[string]any{"key": "mode", "label": "模式", "type": "select", "default": "a"}}
			},
			wantErr: "options",
		},
		{
			name: "secret 用在 bool 上",
			mutate: func(m map[string]any) {
				m["settings"] = []any{map[string]any{"key": "flag", "label": "开关", "type": "bool", "default": true, "secret": true}}
			},
			wantErr: "secret",
		},
		{
			name: "设置缺默认值",
			mutate: func(m map[string]any) {
				m["settings"] = []any{map[string]any{"key": "name", "label": "名称", "type": "string"}}
			},
			wantErr: "默认值",
		},
		{
			name: "设置 key 重复",
			mutate: func(m map[string]any) {
				m["settings"] = []any{
					map[string]any{"key": "name", "label": "名称", "type": "string", "default": ""},
					map[string]any{"key": "name", "label": "名称2", "type": "string", "default": ""},
				}
			},
			wantErr: "重复",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := decodeRepoPluginManifest(validManifestJSON(t, tc.mutate))
			if err != nil {
				t.Fatal(err)
			}
			err = manifest.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !errors.Is(err, ErrRepoPluginFormat) {
				t.Fatalf("err = %v, 想要包装 ErrRepoPluginFormat", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, 想要包含 %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateSkillFrontmatter(t *testing.T) {
	valid := "---\nname: hello\ndescription: 示例插件\n---\n\n# 指令\n"
	if err := validateSkillFrontmatter([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		body string
	}{
		{name: "缺 name", body: "---\ndescription: 示例\n---\n"},
		{name: "缺 description", body: "---\nname: hello\n---\n"},
		{name: "name 为空", body: "---\nname: \"\"\ndescription: 示例\n---\n"},
		{name: "没有 frontmatter", body: "# 直接正文\n"},
		{name: "frontmatter 未闭合", body: "---\nname: hello\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSkillFrontmatter([]byte(tc.body)); !errors.Is(err, ErrRepoPluginSkill) {
				t.Fatalf("err = %v, 想要 ErrRepoPluginSkill", err)
			}
		})
	}
}

func TestDescribeRepoPluginPermissionsSensitiveFirst(t *testing.T) {
	described := DescribeRepoPluginPermissions([]string{"message:read", "process:execute", "network:https", "message:write"})
	if len(described) != 4 {
		t.Fatalf("len = %d", len(described))
	}
	if !described[0].Sensitive || !described[1].Sensitive {
		t.Fatalf("高敏感权限应排在前面: %+v", described)
	}
	if described[0].ID != "message:write" || described[1].ID != "process:execute" {
		t.Fatalf("敏感权限内部按 ID 排序: %+v", described)
	}
	for _, permission := range described {
		if permission.Label == "" {
			t.Fatalf("权限缺少翻译文案: %+v", permission)
		}
	}
}

func TestExtractRepoArchiveWhitelistAndSafety(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	entries := []struct {
		name     string
		body     string
		typeflag byte
	}{
		{name: "owner-repo-v1/SKILL.md", body: "skill"},
		{name: "owner-repo-v1/prompts/a.txt", body: "prompt a"},
		{name: "owner-repo-v1/prompts/nested/b.txt", body: "prompt b"},
		{name: "owner-repo-v1/secret.key", body: "不在白名单"},
		{name: "owner-repo-v1/../evil.txt", body: "越路径"},
		{name: "owner-repo-v1/link", body: "", typeflag: tar.TypeSymlink},
	}
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.body)), Typeflag: entry.typeflag}
		if entry.typeflag == 0 {
			header.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.body != "" {
			if _, err := tw.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	err := extractRepoArchive(buf.Bytes(), dest, []string{"SKILL.md", "prompts/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SKILL.md", "prompts/a.txt", "prompts/nested/b.txt"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(want))); err != nil {
			t.Fatalf("缺少文件 %s: %v", want, err)
		}
	}
	for _, denied := range []string{"secret.key", "evil.txt", "link"} {
		if _, err := os.Stat(filepath.Join(dest, denied)); !os.IsNotExist(err) {
			t.Fatalf("不应存在 %s", denied)
		}
	}
	body, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "skill" {
		t.Fatalf("SKILL.md = %q", body)
	}
}

// repoPluginTestServer 在本地起一个假 GitHub：raw 文件按路径提供，
// tar.gz 归档按插件内容现场打包。
func repoPluginTestServer(t *testing.T, manifest map[string]any, files map[string]string, ref string) *httptest.Server {
	t.Helper()
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.SplitN(path, "/", 4)
		if len(parts) == 4 && parts[2] == "tar.gz" && parts[3] == ref {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			root := parts[0] + "-" + parts[1] + "-" + ref
			for name, body := range files {
				header := &tar.Header{Name: root + "/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
				if err := tw.WriteHeader(header); err != nil {
					t.Error(err)
					return
				}
				if _, err := tw.Write([]byte(body)); err != nil {
					t.Error(err)
					return
				}
			}
			if err := tw.Close(); err != nil {
				t.Error(err)
				return
			}
			if err := gz.Close(); err != nil {
				t.Error(err)
				return
			}
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(buf.Bytes())
			return
		}
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
	return server
}

func testManifestMap(mutate func(map[string]any)) map[string]any {
	raw := map[string]any{
		"id":          "suink.hello",
		"name":        "示例插件",
		"version":     "1.0.0",
		"description": "一句话说明",
		"permissions": []any{"message:read"},
		"entry":       "SKILL.md",
		"files":       []any{"SKILL.md", "prompts/"},
	}
	if mutate != nil {
		mutate(raw)
	}
	return raw
}

func testInstaller(t *testing.T, server *httptest.Server, dataDir string) *RepoPluginInstaller {
	t.Helper()
	installer := NewRepoPluginInstaller(dataDir, server.Client())
	installer.RawBase = server.URL
	installer.ArchiveBase = server.URL
	return installer
}

func TestRepoPluginInstallerPreview(t *testing.T) {
	skill := "---\nname: hello\ndescription: 示例插件\n---\n\n回复问候。"
	server := repoPluginTestServer(t, testManifestMap(nil), map[string]string{
		"SKILL.md":        skill,
		"prompts/a.txt":   "prompt a",
		"prompts/b/c.txt": "prompt b",
	}, "HEAD")
	installer := testInstaller(t, server, t.TempDir())

	preview, err := installer.Preview(context.Background(), "github.com/SuInk/diana-plugin-hello")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Manifest.ID != "suink.hello" {
		t.Fatalf("ID = %q", preview.Manifest.ID)
	}
	if len(preview.Permissions) != 1 || preview.Permissions[0].ID != "message:read" {
		t.Fatalf("permissions = %+v", preview.Permissions)
	}
	if !preview.Risk.FloatingRef {
		t.Fatal("不带 ref 的链接应标记浮动风险")
	}
	if len(preview.Risk.Warnings) == 0 {
		t.Fatal("应有固定风险文案")
	}
	if len(preview.Files) != 2 || preview.Files[0] != "SKILL.md" {
		t.Fatalf("files = %+v", preview.Files)
	}

	// 带 tag 的链接不再提示浮动风险。
	serverRef := repoPluginTestServer(t, testManifestMap(func(m map[string]any) { m["version"] = "1.0.0" }), map[string]string{"SKILL.md": skill}, "v1.0.0")
	installerRef := testInstaller(t, serverRef, t.TempDir())
	previewRef, err := installerRef.Preview(context.Background(), "https://github.com/SuInk/diana-plugin-hello/tree/v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if previewRef.Risk.FloatingRef {
		t.Fatal("固定 tag 不应提示浮动风险")
	}
}

func TestRepoPluginInstallerPreviewMissingManifest(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	installer := testInstaller(t, server, t.TempDir())
	_, err := installer.Preview(context.Background(), "github.com/SuInk/nothing")
	if !errors.Is(err, ErrRepoPluginManifest) {
		t.Fatalf("err = %v, 想要 ErrRepoPluginManifest", err)
	}
}

func TestRepoPluginInstallerPreviewBadSkill(t *testing.T) {
	server := repoPluginTestServer(t, testManifestMap(nil), map[string]string{
		"SKILL.md": "# 没有 frontmatter",
	}, "HEAD")
	installer := testInstaller(t, server, t.TempDir())
	_, err := installer.Preview(context.Background(), "github.com/SuInk/diana-plugin-hello")
	if !errors.Is(err, ErrRepoPluginSkill) {
		t.Fatalf("err = %v, 想要 ErrRepoPluginSkill", err)
	}
}

func TestRepoPluginInstallerPreviewMissingDeclaredFile(t *testing.T) {
	server := repoPluginTestServer(t, testManifestMap(func(m map[string]any) {
		m["files"] = []any{"SKILL.md", "assets/logo.png"}
	}), map[string]string{
		"SKILL.md": "---\nname: h\ndescription: d\n---\n",
	}, "HEAD")
	installer := testInstaller(t, server, t.TempDir())
	_, err := installer.Preview(context.Background(), "github.com/SuInk/diana-plugin-hello")
	if !errors.Is(err, ErrRepoPluginFormat) {
		t.Fatalf("err = %v, 想要 ErrRepoPluginFormat", err)
	}
}

func TestRepoPluginInstallerInstallRoundTrip(t *testing.T) {
	skill := "---\nname: hello\ndescription: 示例插件\n---\n\n回复问候。"
	dataDir := t.TempDir()
	server := repoPluginTestServer(t, testManifestMap(nil), map[string]string{
		"SKILL.md":      skill,
		"prompts/a.txt": "prompt a",
	}, "HEAD")
	installer := testInstaller(t, server, dataDir)

	plugin, source, err := installer.Install(context.Background(), "github.com/SuInk/diana-plugin-hello")
	if err != nil {
		t.Fatal(err)
	}
	if source.ID != "suink.hello" || source.Version != "1.0.0" {
		t.Fatalf("source = %+v", source)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "plugin-sources", "suink.hello", "SKILL.md")); err != nil {
		t.Fatalf("落盘缺少 SKILL.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "plugin-sources", "suink.hello", "prompts", "a.txt")); err != nil {
		t.Fatalf("落盘缺少 prompts/a.txt: %v", err)
	}
	// 清单副本随插件落盘，供重启恢复读取。
	manifestCopy, err := os.ReadFile(filepath.Join(dataDir, "plugin-sources", "suink.hello", "diana.plugin.json"))
	if err != nil {
		t.Fatalf("落盘缺少清单副本: %v", err)
	}
	if !strings.Contains(string(manifestCopy), "suink.hello") {
		t.Fatalf("清单副本内容异常: %q", manifestCopy)
	}

	resp, err := plugin.Handle(context.Background(), PluginRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || !strings.Contains(resp.Context, "回复问候") {
		t.Fatalf("resp = %+v", resp)
	}

	// 重新加载：启动恢复路径。
	reloaded, err := LoadRepoPlugin(dataDir, source)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Manifest().ID != "suink.hello" {
		t.Fatalf("manifest = %+v", reloaded.Manifest())
	}
}

func TestRepoPluginInstallerInstallTagVersionMismatch(t *testing.T) {
	skill := "---\nname: h\ndescription: d\n---\n"
	server := repoPluginTestServer(t, testManifestMap(nil), map[string]string{"SKILL.md": skill}, "v2.0.0")
	installer := testInstaller(t, server, t.TempDir())
	_, _, err := installer.Install(context.Background(), "github.com/SuInk/diana-plugin-hello/tree/v2.0.0")
	if !errors.Is(err, ErrRepoPluginFormat) {
		t.Fatalf("err = %v, 想要 ErrRepoPluginFormat", err)
	}
}

func TestRepoPluginStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewRepoPluginStore(dir)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 0 {
		t.Fatal("空存储应返回空列表")
	}
	source := RepoPluginSource{ID: "suink.hello", Owner: "SuInk", Repo: "diana-plugin-hello", Version: "1.0.0", URL: "https://github.com/SuInk/diana-plugin-hello"}
	if err := store.Save(source); err != nil {
		t.Fatal(err)
	}
	reloaded := NewRepoPluginStore(dir)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Get("suink.hello")
	if !ok || got.Repo != "diana-plugin-hello" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	if err := reloaded.Remove("suink.hello"); err != nil {
		t.Fatal(err)
	}
	again := NewRepoPluginStore(dir)
	if err := again.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Get("suink.hello"); ok {
		t.Fatal("删除后不应再读到记录")
	}
}

func TestRemoveRepoPluginRejectsTraversal(t *testing.T) {
	if err := RemoveRepoPlugin(t.TempDir(), "../evil"); err == nil {
		t.Fatal("应拒绝含路径分隔符的 ID")
	}
}

func TestPluginManagerRegisterPlugin(t *testing.T) {
	manager := NewPluginManager()
	plugin := NewRepoPlugin(t.TempDir(), RepoPluginSource{ID: "suink.hello"}, PluginManifest{ID: "suink.hello", Name: "示例", Version: "1.0.0", Permissions: []string{"message:read"}})
	if err := manager.RegisterPlugin(plugin); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install("suink.hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetEnabledForProfile("suink.hello", "p1", false); err != nil {
		t.Fatal(err)
	}
	// 同 ID 重新登记保留状态。
	if err := manager.RegisterPlugin(plugin); err != nil {
		t.Fatal(err)
	}
	state, ok := manager.Get("suink.hello")
	if !ok || !state.Installed || state.ProfileEnabled["p1"] {
		t.Fatalf("重新登记后状态应保留: %+v", state)
	}
	if err := manager.UnregisterPlugin("suink.hello"); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Get("suink.hello"); ok {
		t.Fatal("注销后不应再读到状态")
	}
}

func TestPluginManagerRegisterPluginRejectsOfficialPrefix(t *testing.T) {
	manager := NewPluginManager()
	plugin := NewRepoPlugin(t.TempDir(), RepoPluginSource{ID: "official.fake"}, PluginManifest{ID: "official.fake", Name: "冒名", Version: "1.0.0"})
	if err := manager.RegisterPlugin(plugin); err == nil {
		t.Fatal("第三方插件使用 official. 前缀应被拒绝")
	}
}

func TestPluginManagerUnregisterPluginKeepsBuiltIn(t *testing.T) {
	manager := NewPluginManager(NewStatusCommandPlugin())
	if err := manager.UnregisterPlugin(statusCommandPluginID); err == nil {
		t.Fatal("内置插件不能通过 UnregisterPlugin 摘除")
	}
}
