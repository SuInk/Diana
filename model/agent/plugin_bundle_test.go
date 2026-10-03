// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPluginBundleSkillsAndResourcesStayInsideBundle(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "upstream")
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: upstream\ndescription: Query upstream accounts\n---\nRead references/api.md then run scripts/admin.js."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "references", "api.md"), []byte("upstream API reference"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills, err := LoadPluginSkills("alice.accounts", root, "https://github.com/alice/accounts", []string{"skills"}, true)
	if err != nil || len(skills) != 1 {
		t.Fatalf("skills=%v err=%v", skills, err)
	}
	if skills[0].Name != "alice.accounts:upstream" || !strings.Contains(skills[0].Content, "skills/upstream") || !strings.Contains(skills[0].Content, PluginToolPrefix("alice.accounts")+"__run") {
		t.Fatalf("skill=%+v", skills[0])
	}
	reader := NewPluginResourceTool("alice.accounts", root, Config{}, nil, nil)
	output, err := reader.Run(context.Background(), map[string]any{"path": "skills/upstream/references/api.md"})
	if err != nil || !strings.Contains(output, "upstream API reference") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	for _, path := range []string{"../outside", filepath.Join(t.TempDir(), "outside")} {
		if _, err := reader.Run(context.Background(), map[string]any{"path": path}); err == nil {
			t.Fatalf("allowed path %q", path)
		}
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err == nil {
		if _, err := reader.Run(context.Background(), map[string]any{"path": "escape"}); err == nil {
			t.Fatal("resource symlink escaped bundle")
		}
	}
	if err := os.Remove(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(skillDir, "SKILL.md")); err == nil {
		if _, err := LoadPluginSkills("alice.accounts", root, "", []string{"skills"}, false); err == nil {
			t.Fatal("skill entry symlink escaped bundle")
		}
	}
}

func TestPluginScriptUsesFixedEntryAndLiteralScopedEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	root := t.TempDir()
	entry := filepath.Join(root, "echo.sh")
	if err := os.WriteFile(entry, []byte("printf '%s|%s|%s|%s' \"$BUNDLE_VALUE\" \"${DIANA_BUNDLE_PRIVATE:-absent}\" \"$1\" \"$2\""), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIANA_BUNDLE_PRIVATE", "must-not-inherit")
	t.Setenv("EXPAND_ME", "incorrect-expansion")
	cfg := Config{WorkDir: t.TempDir(), CommandAllowlist: []string{"sh"}, CommandSandbox: CommandSandboxOff}
	scripts := map[string]PluginScript{"echo": {Command: "sh", Entry: "echo.sh", Args: []string{"fixed"}, Env: map[string]string{"BUNDLE_VALUE": "literal$EXPAND_ME"}}}
	tool := NewPluginScriptTool("alice.echo", root, cfg, scripts)
	output, err := tool.Run(context.Background(), map[string]any{"script": "echo", "args": []any{"business"}, "command": "ignored", "cwd": "../", "env": map[string]any{"BUNDLE_VALUE": "ignored"}})
	if err != nil || !strings.Contains(output, "literal$EXPAND_ME|absent|fixed|business") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil || result["cwd"] != "." {
		t.Fatalf("script did not use workspace: %s", output)
	}
	cfg.CommandAllowlist = nil
	blocked := NewPluginScriptTool("alice.echo", root, cfg, scripts)
	if _, err := blocked.Run(context.Background(), map[string]any{"script": "echo"}); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("err=%v", err)
	}
	cfg.CommandAllowlist = []string{"sh"}
	script := scripts["echo"]
	script.AllowNetwork = true
	scripts["echo"] = script
	blocked = NewPluginScriptTool("alice.echo", root, cfg, scripts)
	if _, err := blocked.Run(context.Background(), map[string]any{"script": "echo"}); err == nil || !strings.Contains(err.Error(), "network") {
		t.Fatalf("err=%v", err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"script": "missing"}); err == nil {
		t.Fatal("allowed undeclared script")
	}
}

func TestPluginMCPRegistryCallsNamespacedStdioTool(t *testing.T) {
	if os.Getenv("DIANA_AGENT_MCP_TEST_SERVER") == "1" {
		runMCPTestServer()
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture")
	}
	root := t.TempDir()
	command := "diana-plugin-mcp-fixture"
	if err := os.Symlink(os.Args[0], filepath.Join(root, command)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := Config{WorkDir: t.TempDir(), CommandAllowlist: []string{command}, CommandSandbox: CommandSandboxOff, MCPStartupTimeoutMS: 3000}
	servers := map[string]PluginMCPServer{"demo": {Command: command, Args: []string{"-test.run=^TestPluginMCPRegistryCallsNamespacedStdioTool$"}, Env: map[string]string{"DIANA_AGENT_MCP_TEST_SERVER": "1"}, Required: true, EnabledTools: []string{"echo"}}}
	registry, err := NewPluginMCPRegistry(context.Background(), "alice.mcp", root, servers, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if len(registry.Tools) != 1 || !strings.Contains(registry.Tools[0].Name(), "alice_mcp") {
		t.Fatalf("tools=%v", registry.Tools)
	}
	output, err := registry.Tools[0].Run(context.Background(), map[string]any{"text": "bundle works"})
	if err != nil || output != "echo: bundle works" {
		t.Fatalf("output=%q err=%v", output, err)
	}
	cfg.CommandAllowlist = nil
	if _, err := NewPluginMCPRegistry(context.Background(), "alice.mcp", root, servers, cfg); err == nil {
		t.Fatal("MCP bypassed command allowlist")
	}
}

func TestPluginOutputRedactsBeforeTruncation(t *testing.T) {
	secret := "test-admin-token-0123456789"
	for _, output := range []string{strings.Repeat("x", 30) + secret + strings.Repeat("z", 100), strings.Repeat(secret, 12) + secret[:10] + strings.Repeat("z", 100)} {
		file, err := os.CreateTemp(t.TempDir(), "output")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(output); err != nil {
			t.Fatal(err)
		}
		got, truncated, err := readPluginCommandOutput(file, 32, []string{secret})
		file.Close()
		if err != nil || !truncated || strings.Contains(got, "test-admin") {
			t.Fatalf("got=%q truncated=%v err=%v", got, truncated, err)
		}
	}
}
