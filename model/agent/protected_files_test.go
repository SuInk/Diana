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

// 最稳的办法不是把令牌文件藏起来，是根本不放在工具够得着的地方：工作目录那道边界
// 本来就拦住了外面的一切。
func TestDefaultMCPConfigPathSitsOutsideWorkspace(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "workspace")
	cfg := Config{WorkDir: workDir}.WithDefaults()
	relation, err := filepath.Rel(workDir, cfg.MCPConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(relation, "..") {
		t.Fatalf("MCP 配置仍在工作目录内: %s", cfg.MCPConfigPath)
	}
	if filepath.Base(cfg.MCPConfigPath) != defaultMCPConfigFileName {
		t.Fatalf("MCP 配置文件名变了: %s", cfg.MCPConfigPath)
	}
}

// 老版本把配置写在工作目录里，升级后不能让它凭空消失。
func TestGlobalExtensionPathsMovesMCPConfigOutOfWorkspace(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(workDir, defaultMCPConfigFileName)
	content := `{"mcpServers":{"gitea":{"env":{"GITEA_ACCESS_TOKEN":"keep-me"}}}}`
	if err := os.WriteFile(legacy, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// 老装机的 .extension-paths.json 把位置钉在工作目录里。
	pinned := `{"skill_roots":[],"mcp_config_path":` + strconvQuote(legacy) + `}`
	if err := os.WriteFile(filepath.Join(workDir, extensionPathsFileName), []byte(pinned), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := GlobalExtensionPaths(Config{WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPConfigPath == legacy {
		t.Fatal("配置没有搬出工作目录")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("旧文件还留在工作目录里")
	}
	moved, err := os.ReadFile(cfg.MCPConfigPath)
	if err != nil || string(moved) != content {
		t.Fatalf("搬过去的内容不对: %v %s", err, moved)
	}
	// 钉住的位置也要跟着改，否则下次启动又指回工作目录。
	again, err := GlobalExtensionPaths(Config{WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if again.MCPConfigPath != cfg.MCPConfigPath {
		t.Fatalf("位置没钉住: %s vs %s", again.MCPConfigPath, cfg.MCPConfigPath)
	}
}

// 目标位置已经有文件时不许覆盖：那多半是用户自己放的真配置。
func TestMCPConfigMigrationKeepsExistingTarget(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(workDir, defaultMCPConfigFileName)
	if err := os.WriteFile(legacy, []byte(`{"legacy":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	target := defaultMCPConfigPath(workDir)
	if err := os.WriteFile(target, []byte(`{"existing":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	moved, err := migrateMCPConfigOutOfWorkspace(workDir, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if moved != legacy {
		t.Fatalf("覆盖了已有的配置: %s", moved)
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != `{"existing":true}` {
		t.Fatalf("目标文件被改了: %v %s", err, body)
	}
	// 搬不走就得继续挡着。
	if !agentProtectedFiles(Config{WorkDir: workDir, MCPConfigPath: legacy}.WithDefaults()).blocked(legacy) {
		t.Fatal("留在原处的配置没有被保护")
	}
}

func strconvQuote(value string) string {
	body, _ := json.Marshal(value)
	return string(body)
}
