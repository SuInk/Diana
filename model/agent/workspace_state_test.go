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

// 老版本的开关文件在工作目录根下：升级后先照读不误，下一次保存写进 .diana/ 并删掉旧的。
func TestExtensionOverridesFallBackToLegacyAndMigrateOnSave(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, ".extension-overrides.json")
	if err := os.WriteFile(legacy, []byte(`{"bot-a":{"mcp:probe":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := LoadExtensionOverrides(root, "bot-a")
	if err != nil || values["mcp:probe"] {
		t.Fatalf("老位置没读到: %v %v", values, err)
	}
	if enabled, ok := values["mcp:probe"]; !ok || enabled {
		t.Fatalf("老位置的开关丢了: %v", values)
	}
	if err := SaveResidencyList(root, "bot-a", []string{"mcp:probe"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("保存后旧文件还在: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".diana", "extension-overrides.json"))
	if err != nil || !strings.Contains(string(data), `"mcp:probe": false`) {
		t.Fatalf("新位置内容不对: %v %s", err, data)
	}
	// 新位置优先：老位置再冒出一份（比如回滚过版本）也不生效。
	if err := os.WriteFile(legacy, []byte(`{"bot-a":{"mcp:probe":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	values, _ = LoadExtensionOverrides(root, "bot-a")
	if values["mcp:probe"] {
		t.Fatal("老位置盖过了新位置")
	}
}

// 启动迁移把三份状态文件都搬进 .diana/；新位置已有文件时以新的为准。
func TestMigrateWorkspaceStateMovesLegacyFiles(t *testing.T) {
	root := t.TempDir()
	for _, file := range legacyWorkspaceStateFiles {
		if err := os.WriteFile(file.legacyPath(root), []byte(`{"from":"legacy"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(DianaStateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extensionAudienceState.path(root), []byte(`{"from":"new"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateWorkspaceState(root); err != nil {
		t.Fatal(err)
	}
	for _, file := range legacyWorkspaceStateFiles {
		if _, err := os.Stat(file.legacyPath(root)); !os.IsNotExist(err) {
			t.Fatalf("%s 还在根下", file.legacy)
		}
		data, err := os.ReadFile(file.path(root))
		if err != nil {
			t.Fatal(err)
		}
		want := `{"from":"legacy"}`
		if file == extensionAudienceState {
			want = `{"from":"new"}`
		}
		if string(data) != want {
			t.Fatalf("%s = %s, want %s", file.name, data, want)
		}
	}
	// 再跑一次什么都不做。
	if err := MigrateWorkspaceState(root); err != nil {
		t.Fatal(err)
	}
}

// .diana/ 按目录交给命令沙盒：macOS 上读写都挡，Linux 上换成空 tmpfs。以前根下的
// 单个开关文件只挡读，白名单里有 rm、mv 时能被删掉或换掉。
func TestCommandSandboxProtectsDianaStateDir(t *testing.T) {
	root := t.TempDir()
	registry, err := NewDefaultToolRegistry(Config{WorkDir: root, CommandAllowlist: []string{"rm"}}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := registry.Get("run_command")
	if !ok {
		t.Fatal("run_command 没注册")
	}
	secrets := tool.(*RunCommandTool).sandboxSecrets()
	var stateDir string
	for _, path := range secrets {
		if filepath.Base(path) == DianaStateDirName {
			stateDir = path
		}
	}
	if stateDir == "" {
		t.Fatalf(".diana 没交给沙盒: %v", secrets)
	}
	profile := sandboxExecProfile(root, false, secrets)
	for _, rule := range []string{
		`(deny file-read* (subpath ` + sbplString(stateDir) + `))`,
		`(deny file-write* (subpath ` + sbplString(stateDir) + `))`,
	} {
		if !strings.Contains(profile, rule) {
			t.Fatalf("SBPL 缺少 %s: %s", rule, profile)
		}
	}
	// 写入挡住必须排在放开工作目录写入之后，否则被覆盖。
	if strings.Index(profile, `(deny file-write* (subpath `+sbplString(stateDir)+`))`) < strings.Index(profile, `(allow file-write* (subpath `+sbplString(root)+`))`) {
		t.Fatalf("写入挡在放行前面，等于没挡: %s", profile)
	}
	cmd := wrapWithBubblewrap("bwrap")(context.Background(), root, false, secrets, "rm", []string{"-rf", ".diana"})
	if !strings.Contains(strings.Join(cmd.Args, " "), "--tmpfs "+stateDir) {
		t.Fatalf("bubblewrap 没把 .diana 盖掉: %v", cmd.Args)
	}
}
