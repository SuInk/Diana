// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newManageFilesTestTool(t *testing.T, writeEnabled bool) (*ManageFilesTool, string) {
	t.Helper()
	workDir := t.TempDir()
	registry, err := NewDefaultToolRegistry(Config{WorkDir: workDir, FileWriteEnabled: writeEnabled}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := registry.Get(ManageFilesToolName)
	if !ok {
		t.Fatal("manage_files 没注册")
	}
	manage := tool.(*ManageFilesTool)
	manage.now = func() time.Time { return time.Date(2026, 9, 26, 15, 30, 12, 0, time.UTC) }
	return manage, manage.root
}

func runManageFiles(t *testing.T, tool *ManageFilesTool, input map[string]any) map[string]any {
	t.Helper()
	out, err := tool.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("manage_files %v: %v", input, err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("结果不是 JSON: %s", out)
	}
	return result
}

func writeManageTestFile(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManageFilesStat(t *testing.T) {
	tool, root := newManageFilesTestTool(t, false)
	writeManageTestFile(t, root, "downloads/cat.png", testJPEG(t, 30, 20))
	result := runManageFiles(t, tool, map[string]any{"action": "stat", "path": "downloads/cat.png"})
	if result["mime"] != "image/jpeg" || result["width"] != float64(30) || result["height"] != float64(20) || result["is_dir"] != false {
		t.Fatalf("stat 结果不对: %v", result)
	}
	if !strings.Contains(result["extension_mismatch"].(string), "cat.jpg") {
		t.Fatalf("扩展名和内容不符时应该指出来: %v", result)
	}
	if result["size"].(float64) <= 0 || result["modified"] == "" {
		t.Fatalf("缺少大小或修改时间: %v", result)
	}
	dir := runManageFiles(t, tool, map[string]any{"action": "stat", "path": "downloads"})
	if dir["is_dir"] != true || dir["entries"] != float64(1) {
		t.Fatalf("目录 stat 不对: %v", dir)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"action": "delete", "path": "downloads/cat.png"}); err == nil {
		t.Fatal("写入没打开时 delete 应被拒绝")
	}
	if _, err := tool.Run(context.Background(), map[string]any{"action": "stat", "path": "downloads/cat.jpg"}); err == nil || !strings.Contains(err.Error(), "downloads/cat.png") {
		t.Fatalf("找不到时应列出同名候选: %v", err)
	}
}

func TestManageFilesMoveCopyMkdir(t *testing.T) {
	tool, root := newManageFilesTestTool(t, true)
	writeManageTestFile(t, root, "a.txt", []byte("alpha"))
	writeManageTestFile(t, root, "b.txt", []byte("beta"))

	if got := runManageFiles(t, tool, map[string]any{"action": "mkdir", "path": "archive/2026"}); got["existed"] != false {
		t.Fatalf("mkdir: %v", got)
	}
	moved := runManageFiles(t, tool, map[string]any{"action": "move", "path": "a.txt", "to": "archive/2026"})
	if moved["to"] != "archive/2026/a.txt" {
		t.Fatalf("挪进已有目录应保留文件名: %v", moved)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("move 后原文件还在")
	}
	copied := runManageFiles(t, tool, map[string]any{"action": "copy", "path": "b.txt", "to": "archive/b-copy.txt"})
	if copied["bytes"] != float64(4) {
		t.Fatalf("copy: %v", copied)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "archive", "b-copy.txt")); string(data) != "beta" {
		t.Fatalf("复制内容不对: %q", data)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"action": "copy", "path": "b.txt", "to": "archive/b-copy.txt"}); err == nil {
		t.Fatal("目标已存在时默认应拒绝覆盖")
	}
	over := runManageFiles(t, tool, map[string]any{"action": "move", "path": "b.txt", "to": "archive/b-copy.txt", "overwrite": true})
	if over["overwrote"] != true {
		t.Fatalf("overwrite=true 应覆盖: %v", over)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"action": "move", "path": "archive", "to": "archive/2026/inner"}); err == nil {
		t.Fatal("目录不能挪进自己里面")
	}
}

func TestManageFilesDeleteMovesToTrash(t *testing.T) {
	tool, root := newManageFilesTestTool(t, true)
	writeManageTestFile(t, root, "outputs/image.png", testPNG(t, 2, 2))
	result := runManageFiles(t, tool, map[string]any{"action": "delete", "path": "outputs/image.png"})
	want := WorkspaceTrashDir + "/20260926-153012/outputs/image.png"
	if result["trash_path"] != want {
		t.Fatalf("trash_path = %v, want %s", result["trash_path"], want)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(want))); err != nil {
		t.Fatalf("回收站里没有这个文件: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "outputs", "image.png")); !os.IsNotExist(err) {
		t.Fatal("delete 后原位置还有文件")
	}
	// 同一秒再删一个同路径的文件，不能盖掉回收站里的上一份。
	writeManageTestFile(t, root, "outputs/image.png", testPNG(t, 2, 2))
	again := runManageFiles(t, tool, map[string]any{"action": "delete", "path": "outputs/image.png"})
	if again["trash_path"] != WorkspaceTrashDir+"/20260926-153012-2/outputs/image.png" {
		t.Fatalf("同一时刻重复删除: %v", again)
	}
	for _, input := range []map[string]any{
		{"action": "delete", "path": WorkspaceTrashDir + "/20260926-153012/outputs/image.png"},
		{"action": "move", "path": WorkspaceTrashDir + "/20260926-153012/outputs/image.png", "to": "restored.png"},
		{"action": "stat", "path": WorkspaceTrashDir},
		{"action": "delete", "path": "."},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("%v 应被拒绝", input)
		}
	}
}

func TestManageFilesRefusesProtectedPaths(t *testing.T) {
	tool, root := newManageFilesTestTool(t, true)
	writeManageTestFile(t, root, ".mcp.json", []byte(`{"token":"secret"}`))
	writeManageTestFile(t, root, CodingRuntimeDirName+"/auth/auth.json", []byte(`{"token":"secret"}`))
	writeManageTestFile(t, root, "notes.txt", []byte("x"))
	for _, input := range []map[string]any{
		{"action": "stat", "path": ".mcp.json"},
		{"action": "delete", "path": ".mcp.json"},
		{"action": "copy", "path": ".mcp.json", "to": "leak.json"},
		{"action": "move", "path": "notes.txt", "to": ".mcp.json", "overwrite": true},
		// 整个目录挪走或删掉会把里面的凭据一起带走。
		{"action": "delete", "path": CodingRuntimeDirName},
		{"action": "move", "path": CodingRuntimeDirName, "to": "elsewhere"},
		{"action": "move", "path": "notes.txt", "to": "../escape.txt"},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("%v 应被拒绝", input)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(root, ".mcp.json")); !strings.Contains(string(data), "secret") {
		t.Fatal("凭据文件被改动了")
	}
}
