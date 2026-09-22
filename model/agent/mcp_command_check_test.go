// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeFakeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckLocalMCPCommand(t *testing.T) {
	dir := t.TempDir()
	executable := writeFakeExecutable(t, dir, "fake-mcp")

	// 远程接法没有本地命令可查。
	if err := checkLocalMCPCommand(mcpServerConfig{URL: "https://example.com/mcp"}); err != nil {
		t.Fatalf("远程配置不该报本地命令问题：%v", err)
	}
	if err := checkLocalMCPCommand(mcpServerConfig{Command: executable}); err != nil {
		t.Fatalf("存在的可执行文件被判成缺失：%v", err)
	}
	if err := checkLocalMCPCommand(mcpServerConfig{Command: filepath.Join(dir, "nope")}); err == nil {
		t.Fatal("不存在的路径应当报错")
	}
	if err := checkLocalMCPCommand(mcpServerConfig{Command: dir}); err == nil {
		t.Fatal("目录不是可执行文件")
	}
	if err := checkLocalMCPCommand(mcpServerConfig{Command: "diana-definitely-not-installed"}); err == nil {
		t.Fatal("PATH 里没有的命令应当报错")
	}
	if runtime.GOOS != "windows" {
		plain := filepath.Join(dir, "no-exec-bit")
		if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := checkLocalMCPCommand(mcpServerConfig{Command: plain}); err == nil {
			t.Fatal("没有可执行权限的文件应当报错")
		}
	}
}

// TestPresetSaveReportsMissingLocalCommand 干净环境里没有 gitea-mcp 时，装预设必须
// 当场说清楚，而不是保存成功、启用成功，等到某次对话调用工具才在后台日志里冒出
// exec: "gitea-mcp": executable file not found in $PATH。
func TestPresetSaveReportsMissingLocalCommand(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir(), ExtensionManagement: true}
	ctx := context.Background()
	gitea := giteaAPIStub(t, "good-token")

	result, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{
		Operation: "save", Kind: "mcp", Name: "gitea", Preset: "gitea", Transport: "stdio",
		Values: map[string]string{"host": gitea.URL, "token": "good-token", "command": filepath.Join(t.TempDir(), "gitea-mcp")},
	})
	if err != nil {
		t.Fatalf("命令缺失不该拦住保存：%v", err)
	}
	saved, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("保存结果 = %#v", result)
	}
	warning, _ := saved["warning"].(string)
	for _, want := range []string{"找不到可执行文件", "镜像或安装包", "可执行文件", "远程地址"} {
		if !strings.Contains(warning, want) {
			t.Fatalf("警告里缺少 %q：%s", want, warning)
		}
	}
	// 拦不拦得住是一回事，配置还是要落盘：二进制可能是等会儿才挂上去的。
	servers, err := loadMCPServers(resolveMCPConfigPath(cfg.WithDefaults()))
	if err != nil || len(servers) != 1 {
		t.Fatalf("配置没有落盘：%#v %v", servers, err)
	}

	// 目录页不启进程，但这一下查得起：缺二进制的服务不能在卡片上和正常的一模一样。
	listed, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{Operation: "list"})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := listed.(map[string]any)["items"].([]ExtensionState)
	var found bool
	for _, item := range items {
		if item.Kind != ExtensionKindMCP || item.Name != "gitea" {
			continue
		}
		found = true
		if !strings.Contains(item.Error, "找不到可执行文件") {
			t.Fatalf("卡片上没有写明命令缺失：%q", item.Error)
		}
	}
	if !found {
		t.Fatal("列表里没有这条 MCP")
	}
}

// TestPresetSaveStaysQuietWhenCommandExists 命令在的时候不许乱报警告，否则这条
// 提示会被当成背景噪声。
func TestPresetSaveStaysQuietWhenCommandExists(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir(), ExtensionManagement: true}
	ctx := context.Background()
	gitea := giteaAPIStub(t, "good-token")
	// 这个「gitea-mcp」不会说 MCP 协议，连接测试必然失败；这里只看命令缺失这条
	// 提示不再出现，连接失败是另一条独立的警告。
	fake := writeFakeExecutable(t, t.TempDir(), "gitea-mcp")

	result, err := AdministerExtensions(ctx, cfg, ExtensionAdminRequest{
		Operation: "save", Kind: "mcp", Name: "gitea", Preset: "gitea", Transport: "stdio",
		Values: map[string]string{"host": gitea.URL, "token": "good-token", "command": fake},
	})
	if err != nil {
		t.Fatal(err)
	}
	warning, _ := result.(map[string]any)["warning"].(string)
	if strings.Contains(warning, "找不到可执行文件") {
		t.Fatalf("命令在却报缺失：%s", warning)
	}
}
