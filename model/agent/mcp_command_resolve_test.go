// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 配置里存的是裸名字（没带这份二进制的旧版本就是这么存的），升级到带它的版本
// 之后必须自己找到主程序旁边那份，而不是继续报「PATH 里找不到命令」。
func TestResolveLocalMCPCommandFallsBackToBundled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("这条只在类 Unix 上用得到可执行位")
	}
	dir := t.TempDir()
	bundled := filepath.Join(dir, "gitea-mcp")
	if err := os.WriteFile(bundled, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("准备二进制失败：%v", err)
	}
	if got := resolveLocalMCPCommandIn("gitea-mcp", dir); got != bundled {
		t.Fatalf("没找到随包发布的那份：%s", got)
	}
	// 旁边没有的时候维持原样：报错交给 checkLocalMCPCommand，那里的话更好懂。
	if got := resolveLocalMCPCommandIn("gitea-mcp", t.TempDir()); got != "gitea-mcp" {
		t.Fatalf("找不到时不该改写命令：%s", got)
	}
}

// 用户自己填的绝对路径不能被改写，PATH 里已经有的也不能被旁边那份顶掉。
func TestResolveLocalMCPCommandKeepsExplicitChoices(t *testing.T) {
	dir := t.TempDir()
	absolute := filepath.Join(dir, "my-mcp")
	if got := resolveLocalMCPCommandIn(absolute, dir); got != absolute {
		t.Fatalf("绝对路径被改写了：%s", got)
	}
	name := "sh"
	if runtime.GOOS == "windows" {
		name = "cmd"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("准备同名文件失败：%v", err)
	}
	if got := resolveLocalMCPCommandIn(name, dir); got != name {
		t.Fatalf("PATH 里有的命令应优先：%s", got)
	}
}
