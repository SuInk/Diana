// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MCP 配置默认就落在 Agent 工作目录里，里面是 access token 原文。文件工具只拦
// 「不许走出工作目录」，所以一句「读一下 .mcp.json」就能把令牌打进聊天记录。
func TestFileToolsRefuseRuntimeCredentialFiles(t *testing.T) {
	workDir := t.TempDir()
	secret := "gitea-token-SHOULD-NOT-LEAK"
	mcpPath := filepath.Join(workDir, ".mcp.json")
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"gitea":{"env":{"GITEA_ACCESS_TOKEN":"`+secret+`"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".extension-overrides.json"), []byte(`{"bot-a":{"mcp:gitea":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "notes.txt"), []byte("ordinary file"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{WorkDir: workDir, FileWriteEnabled: true}.WithDefaults()
	registry, err := NewDefaultToolRegistry(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run := func(name string, input map[string]any) (string, error) {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("%s 没注册", name)
		}
		return tool.Run(ctx, input)
	}

	for _, target := range []string{".mcp.json", ".extension-overrides.json"} {
		out, err := run("read_file", map[string]any{"path": target})
		if err == nil {
			t.Fatalf("read_file 读出了 %s: %s", target, out)
		}
		if !strings.Contains(err.Error(), "运行时配置") {
			t.Fatalf("错误没说清原因: %v", err)
		}
		if _, err := run("write_file", map[string]any{"path": target, "content": "{}"}); err == nil {
			t.Fatalf("write_file 覆盖了 %s", target)
		}
	}
	// 软链接绕不过去：safePath 只保证解析后仍在工作目录内，没说不能指向凭据文件。
	if err := os.Symlink(mcpPath, filepath.Join(workDir, "link.json")); err == nil {
		if out, err := run("read_file", map[string]any{"path": "link.json"}); err == nil {
			t.Fatalf("软链接绕过了拦截: %s", out)
		}
	}
	// 内容检索同样不能把令牌捞出来。
	if out, err := run("grep", map[string]any{"pattern": "SHOULD-NOT-LEAK"}); err == nil && strings.Contains(out, secret) {
		t.Fatalf("grep 把令牌捞出来了: %s", out)
	}
	if out, err := run("find_files", map[string]any{"pattern": ".mcp.json"}); err == nil && strings.Contains(out, ".mcp.json") {
		t.Fatalf("find_files 列出了凭据文件: %s", out)
	}
	listed, err := run("list_files", map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(listed, ".mcp.json") {
		t.Fatalf("list_files 列出了凭据文件: %s", listed)
	}
	// 普通文件不受影响，别把工作目录整个锁死。
	if !strings.Contains(listed, "notes.txt") {
		t.Fatalf("普通文件从列表里消失了: %s", listed)
	}
	if out, err := run("read_file", map[string]any{"path": "notes.txt"}); err != nil || !strings.Contains(out, "ordinary file") {
		t.Fatalf("普通文件读不了: %v %s", err, out)
	}
}

// MCP 配置放在工作目录外面时（默认路径被改过），拦截不能反过来把工作目录里的同名
// 文件也当成凭据——那是用户自己的文件。
func TestProtectedFilesFollowConfiguredMCPPath(t *testing.T) {
	workDir, outside := t.TempDir(), t.TempDir()
	cfg := Config{WorkDir: workDir, MCPConfigPath: filepath.Join(outside, "custom-mcp.json")}.WithDefaults()
	protected := agentProtectedFiles(cfg)
	if !protected.blocked(filepath.Join(outside, "custom-mcp.json")) {
		t.Fatal("配置指定的 MCP 路径没有被保护")
	}
	if protected.blocked(filepath.Join(workDir, "custom-mcp.json")) {
		t.Fatal("误伤了工作目录里的同名文件")
	}
}
