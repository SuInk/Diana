// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func uploadTestFiles(t *testing.T) map[string]string {
	t.Helper()
	manifest, err := json.Marshal(testManifestMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"diana.plugin.json": string(manifest),
		"SKILL.md":          "---\nname: hello\ndescription: 示例插件\n---\n\n回复问候。",
		"prompts/a.txt":     "prompt a",
		"README.md":         "不在白名单里",
	}
}

func uploadTestZip(t *testing.T, prefix string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uploadTestTarGz(t *testing.T, prefix string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: prefix + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
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

func TestRepoPluginArchivePreviewLayouts(t *testing.T) {
	files := uploadTestFiles(t)
	// macOS「压缩」会带 __MACOSX/ 和 .DS_Store，不能因此认不出唯一顶层目录。
	withJunk := map[string]string{}
	for name, body := range files {
		withJunk[name] = body
	}
	withJunk[".DS_Store"] = "junk"
	cases := map[string][]byte{
		"zip 根目录":        uploadTestZip(t, "", files),
		"zip 顶层文件夹":      uploadTestZip(t, "hello-main/", files),
		"zip 带 macOS 垃圾": uploadTestZipWithMacJunk(t, withJunk),
		"tar.gz 根目录":     uploadTestTarGz(t, "./", files),
		"tar.gz 顶层文件夹":   uploadTestTarGz(t, "hello/", files),
	}
	installer := NewRepoPluginInstaller(t.TempDir(), nil)
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			preview, err := installer.PreviewArchive("/tmp/fake/hello.zip", data)
			if err != nil {
				t.Fatal(err)
			}
			if preview.Manifest.ID != "suink.hello" || preview.Archive != "hello.zip" {
				t.Fatalf("preview = %+v", preview)
			}
			if len(preview.Commit) != 64 || preview.Source.Owner != "" || preview.Risk.FloatingRef {
				t.Fatalf("commit/source/risk = %q %+v %+v", preview.Commit, preview.Source, preview.Risk)
			}
		})
	}
}

func uploadTestZipWithMacJunk(t *testing.T, files map[string]string) []byte {
	t.Helper()
	all := map[string]string{"__MACOSX/hello/._SKILL.md": "junk"}
	for name, body := range files {
		all["hello/"+name] = body
	}
	return uploadTestZip(t, "", all)
}

func TestRepoPluginArchiveRejectsBadUploads(t *testing.T) {
	installer := NewRepoPluginInstaller(t.TempDir(), nil)
	files := uploadTestFiles(t)
	twoRoots := map[string]string{}
	for name, body := range files {
		twoRoots["a/"+name] = body
	}
	twoRoots["b/other.txt"] = "x"
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"不是压缩包", []byte("plain text"), ErrRepoPluginArchive},
		{"坏 zip", []byte("PK\x03\x04broken"), ErrRepoPluginArchive},
		{"没有清单", uploadTestZip(t, "", map[string]string{"SKILL.md": files["SKILL.md"]}), ErrRepoPluginManifest},
		{"清单埋在两个顶层目录之一", uploadTestZip(t, "", twoRoots), ErrRepoPluginManifest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := installer.PreviewArchive("x.zip", tc.data); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, 想要 %v", err, tc.want)
			}
		})
	}
}

func TestRepoPluginArchiveDropsTraversalEntries(t *testing.T) {
	files := uploadTestFiles(t)
	files["../../escape.txt"] = "evil"
	dataDir := t.TempDir()
	installer := NewRepoPluginInstaller(dataDir, nil)
	data := uploadTestZip(t, "", files)
	preview, err := installer.PreviewArchive("x.zip", data)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := installer.InstallArchiveGuarded("x.zip", data, preview.Commit, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("越路径条目不应落盘: %v", err)
	}
}

func TestRepoPluginArchiveInstallRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	installer := NewRepoPluginInstaller(dataDir, nil)
	data := uploadTestZip(t, "hello/", uploadTestFiles(t))
	preview, err := installer.PreviewArchive("hello.zip", data)
	if err != nil {
		t.Fatal(err)
	}

	// 预览之后换了文件，摘要对不上就拒绝。
	other := uploadTestTarGz(t, "", uploadTestFiles(t))
	if _, _, err := installer.InstallArchiveGuarded("hello.zip", other, preview.Commit, nil); !errors.Is(err, ErrRepoPluginArchiveChanged) {
		t.Fatalf("err = %v, 想要 ErrRepoPluginArchiveChanged", err)
	}

	plugin, source, err := installer.InstallArchiveGuarded("hello.zip", data, strings.ToUpper(preview.Commit), nil)
	if err != nil {
		t.Fatal(err)
	}
	if source.Archive != "hello.zip" || source.URL != "" || source.Owner != "" || source.Commit != preview.Commit {
		t.Fatalf("source = %+v", source)
	}
	dir := filepath.Join(dataDir, "plugin-sources", "suink.hello")
	if _, err := os.Stat(filepath.Join(dir, "prompts", "a.txt")); err != nil {
		t.Fatalf("落盘缺少 prompts/a.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("白名单外的文件不应落盘: %v", err)
	}
	resp, err := plugin.Handle(context.Background(), PluginRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || !strings.Contains(resp.Context, "上传的压缩包 hello.zip") {
		t.Fatalf("上下文应标出压缩包出处: %+v", resp)
	}
	if _, err := LoadRepoPlugin(dataDir, source); err != nil {
		t.Fatalf("重启恢复失败: %v", err)
	}
}
