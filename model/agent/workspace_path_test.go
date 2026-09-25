package agent

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkspaceTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestNormalizeWorkspacePath 覆盖模型常见的几种路径写法：能落进工作目录的都整理成
// 相对路径，落不进去的一律拒绝并带上 ErrWorkspacePath。
func TestNormalizeWorkspacePath(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceTestFile(t, root, "x.jpg", "x")
	writeWorkspaceTestFile(t, root, "outputs/a.png", "a")
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"普通相对路径", "x.jpg", "x.jpg"},
		{"空路径是根", "", "."},
		{"首尾空白", "  outputs/a.png \n", "outputs/a.png"},
		{"点斜杠前缀", "./x.jpg", "x.jpg"},
		{"反斜杠", `outputs\a.png`, filepath.Join("outputs", "a.png")},
		{"容器习惯的 /workspace 前缀", "/workspace/outputs/a.png", filepath.Join("outputs", "a.png")},
		{"只写 /workspace", "/workspace", "."},
		{"多写一层 workspace/", "workspace/x.jpg", "x.jpg"},
		{"多写一层 workspace/ 的新文件", "workspace/new/b.png", filepath.Join("new", "b.png")},
		{"工作目录绝对路径", filepath.Join(root, "outputs", "a.png"), filepath.Join("outputs", "a.png")},
		{"解析软链接后的绝对路径", filepath.Join(resolvedRoot, "x.jpg"), "x.jpg"},
		{"工作目录本身", root + string(filepath.Separator), "."},
		{"内部 .. 仍在目录内", "outputs/../x.jpg", "x.jpg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeWorkspacePath(root, tc.in)
			if err != nil {
				t.Fatalf("NormalizeWorkspacePath(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeWorkspacePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	rejects := []struct {
		name string
		in   string
	}{
		{"系统绝对路径", "/etc/passwd"},
		{"相对路径逃逸", "../secret"},
		{"中途逃逸", "outputs/../../secret"},
		{"/workspace 前缀后逃逸", "/workspace/../etc/passwd"},
		{"workspace/ 前缀后逃逸", "workspace/../../secret"},
		{"兄弟目录绝对路径", filepath.Join(filepath.Dir(root), "other", "x.jpg")},
		{"Windows 盘符路径", `C:\Users\miku\x.jpg`},
	}
	for _, tc := range rejects {
		t.Run("拒绝/"+tc.name, func(t *testing.T) {
			got, err := NormalizeWorkspacePath(root, tc.in)
			if err == nil {
				t.Fatalf("NormalizeWorkspacePath(%q) = %q, want error", tc.in, got)
			}
			if !errors.Is(err, ErrWorkspacePath) {
				t.Fatalf("error should wrap ErrWorkspacePath: %v", err)
			}
			if !strings.Contains(err.Error(), "find_files") {
				t.Fatalf("error should point to find_files: %v", err)
			}
		})
	}
}

// TestNormalizeWorkspacePathResolvedRootAcceptsLinkForm 覆盖根本身给的是解析后形式、
// 模型写的是软链接形式（macOS 的 /var 与 /private/var）的情况。
func TestNormalizeWorkspacePathResolvedRootAcceptsLinkForm(t *testing.T) {
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedRoot == root {
		// 临时目录不经过软链接的平台上，额外造一个指向工作目录的链接来复现。
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(resolvedRoot, link); err != nil {
			t.Skip("symlink unavailable:", err)
		}
		root = link
	}
	writeWorkspaceTestFile(t, resolvedRoot, "x.jpg", "x")
	got, err := NormalizeWorkspacePath(resolvedRoot, filepath.Join(root, "x.jpg"))
	if err != nil || got != "x.jpg" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// TestNormalizeWorkspacePathRealWorkspaceSubdir 工作目录里真有个叫 workspace 的子目录时，
// workspace/ 前缀必须按字面理解，只有字面找不到、去掉前缀能找到时才去掉。
func TestNormalizeWorkspacePathRealWorkspaceSubdir(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceTestFile(t, root, "workspace/inner.txt", "inner")
	writeWorkspaceTestFile(t, root, "top.txt", "top")
	cases := []struct {
		in   string
		want string
	}{
		{"workspace/inner.txt", filepath.Join("workspace", "inner.txt")},
		{"workspace/top.txt", "top.txt"},
		{"workspace/new.txt", filepath.Join("workspace", "new.txt")},
		{"workspace", "workspace"},
		{"/workspace/workspace/inner.txt", filepath.Join("workspace", "inner.txt")},
	}
	for _, tc := range cases {
		got, err := NormalizeWorkspacePath(root, tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("NormalizeWorkspacePath(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

// TestReadFileToolAcceptsModelPathForms read_file 对各种写法都读到同一个文件，
// 写错的给出中文、可操作的报错。
func TestReadFileToolAcceptsModelPathForms(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceTestFile(t, root, "outputs/a.txt", "hello")
	tool := &ReadFileTool{root: root, maxBytes: DefaultReadFileMaxBytes}
	for _, in := range []string{
		"outputs/a.txt",
		"./outputs/a.txt ",
		"/workspace/outputs/a.txt",
		"workspace/outputs/a.txt",
		`outputs\a.txt`,
		filepath.Join(root, "outputs", "a.txt"),
	} {
		t.Run(in, func(t *testing.T) {
			out, err := tool.Run(context.Background(), map[string]any{"path": in})
			if err != nil {
				t.Fatalf("read_file(%q): %v", in, err)
			}
			if !strings.Contains(out, "hello") {
				t.Fatalf("read_file(%q) = %q", in, out)
			}
		})
	}

	errorCases := []struct {
		in      string
		is      error
		wantMsg string
	}{
		{"/etc/passwd", ErrWorkspacePath, "相对路径"},
		{"../secret", ErrWorkspacePath, "跑出了工作目录"},
		{"outputs/missing.txt", fs.ErrNotExist, "find_files"},
	}
	for _, tc := range errorCases {
		t.Run("错误/"+tc.in, func(t *testing.T) {
			_, err := tool.Run(context.Background(), map[string]any{"path": tc.in})
			if err == nil {
				t.Fatalf("read_file(%q) should fail", tc.in)
			}
			if !errors.Is(err, tc.is) || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("read_file(%q) error = %v", tc.in, err)
			}
		})
	}
}

// TestReadWorkspaceFileAcceptsModelPathForms ReadWorkspaceFile 是看图、发附件的入口，
// 写法要和 read_file 一致，同时仍挡住逃逸和指向外面的软链接。
func TestReadWorkspaceFileAcceptsModelPathForms(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceTestFile(t, root, "outputs/a.png", "png")
	for _, in := range []string{"outputs/a.png", "/workspace/outputs/a.png", "workspace/outputs/a.png", filepath.Join(root, "outputs", "a.png")} {
		data, err := ReadWorkspaceFile(root, in, 16)
		if err != nil || string(data) != "png" {
			t.Fatalf("ReadWorkspaceFile(%q) = %q, %v", in, data, err)
		}
	}
	if _, err := ReadWorkspaceFile(root, "outputs/b.png", 16); !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "find_files") {
		t.Fatalf("missing file error = %v", err)
	}
	if _, err := ReadWorkspaceFile(root, "outputs", 16); err == nil || !strings.Contains(err.Error(), "是目录") {
		t.Fatalf("directory error = %v", err)
	}
	if _, err := ReadWorkspaceFile(root, "", 16); !errors.Is(err, ErrWorkspacePath) {
		t.Fatalf("empty path error = %v", err)
	}
}

// TestFindFilesReportsMissingBase 起点目录写错时要明说，而不是回一句「没有匹配的文件」。
func TestFindFilesReportsMissingBase(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceTestFile(t, root, "outputs/a.png", "a")
	tool := &FindFilesTool{root: root}
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "*.png", "path": "/workspace/outputs"})
	if err != nil || !strings.Contains(out, "a.png") {
		t.Fatalf("find_files = %q, %v", out, err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"pattern": "*.png", "path": "nope"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing base error = %v", err)
	}
}
