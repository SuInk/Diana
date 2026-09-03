// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCommandSandboxMode(t *testing.T) {
	for input, want := range map[string]string{
		"":            CommandSandboxAuto,
		"auto":        CommandSandboxAuto,
		" AUTO ":      CommandSandboxAuto,
		"off":         CommandSandboxOff,
		"OFF":         CommandSandboxOff,
		"require":     CommandSandboxRequire,
		"  require  ": CommandSandboxRequire,
		// 拼错的值按最安全的 auto 处理，不能静默变成关闭。
		"sandbox": CommandSandboxAuto,
		"no":      CommandSandboxAuto,
	} {
		if got := normalizeCommandSandboxMode(input); got != want {
			t.Fatalf("normalizeCommandSandboxMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestConfigDefaultsSandboxToAuto(t *testing.T) {
	cfg := Config{WorkDir: t.TempDir()}.WithDefaults()
	if cfg.CommandSandbox != CommandSandboxAuto {
		t.Fatalf("CommandSandbox = %q, want %q", cfg.CommandSandbox, CommandSandboxAuto)
	}
	if cfg.CommandSandboxAllowNetwork {
		t.Fatal("sandbox network access must be disabled by default")
	}
}

func TestSandboxExecProfileConfinesWritesAndNetwork(t *testing.T) {
	profile := sandboxExecProfile("/tmp/agent work", false)
	for _, want := range []string{
		"(deny default)",
		"(allow file-read*)",
		`(allow file-write* (subpath "/tmp/agent work"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile = %q, missing %q", profile, want)
		}
	}
	if strings.Contains(profile, "(allow network*)") {
		t.Fatal("network must stay denied unless explicitly allowed")
	}
	if allowed := sandboxExecProfile("/tmp/work", true); !strings.Contains(allowed, "(allow network*)") {
		t.Fatalf("profile with network = %q", allowed)
	}
}

func TestSBPLStringEscapesQuotes(t *testing.T) {
	// 工作目录来自配置，可能含引号或反斜杠；不转义会让策略语法破裂，
	// 而破裂的策略等于没有沙盒。
	if got := sbplString(`/tmp/a"b\c`); got != `"/tmp/a\"b\\c"` {
		t.Fatalf("sbplString = %s", got)
	}
}

func TestCommandForRequireModeRefusesWithoutSandbox(t *testing.T) {
	tool := &RunCommandTool{root: t.TempDir(), sandboxMode: CommandSandboxRequire}
	if _, _, err := tool.commandFor(context.Background(), "echo", []string{"hi"}); err == nil {
		t.Fatal("require mode must refuse to run without a sandbox")
	}
}

func TestCommandForOffModeRunsBare(t *testing.T) {
	tool := &RunCommandTool{root: t.TempDir(), sandboxMode: CommandSandboxOff, sandbox: detectCommandSandbox()}
	cmd, kind, err := tool.commandFor(context.Background(), "echo", []string{"hi"})
	if err != nil {
		t.Fatal(err)
	}
	if kind != "" {
		t.Fatalf("sandbox kind = %q, want empty in off mode", kind)
	}
	if !strings.HasSuffix(cmd.Path, "echo") {
		t.Fatalf("cmd.Path = %q, want the bare command", cmd.Path)
	}
}

func TestCommandForAutoModeWrapsWhenAvailable(t *testing.T) {
	sandbox := detectCommandSandbox()
	if !sandbox.available() {
		t.Skipf("no sandbox available on %s", runtime.GOOS)
	}
	tool := &RunCommandTool{root: t.TempDir(), sandboxMode: CommandSandboxAuto, sandbox: sandbox}
	cmd, kind, err := tool.commandFor(context.Background(), "echo", []string{"hi"})
	if err != nil {
		t.Fatal(err)
	}
	if kind != sandbox.kind {
		t.Fatalf("sandbox kind = %q, want %q", kind, sandbox.kind)
	}
	if strings.HasSuffix(cmd.Path, "/echo") {
		t.Fatalf("auto mode ran the command unwrapped: %q", cmd.Path)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "echo") {
		t.Fatalf("wrapped args lost the command: %#v", cmd.Args)
	}
}

// TestRunCommandSandboxBlocksWriteOutsideWorkdir 是这个特性的核心断言：白名单
// 允许跑 sh，但沙盒必须挡住它往工作目录之外写。没有沙盒的平台跳过。
func TestRunCommandSandboxBlocksWriteOutsideWorkdir(t *testing.T) {
	sandbox := detectCommandSandbox()
	if !sandbox.available() {
		t.Skipf("no sandbox available on %s", runtime.GOOS)
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "escaped.txt")
	tool := &RunCommandTool{
		root:        root,
		allowlist:   commandAllowlistSet([]string{"sh"}),
		timeout:     10 * time.Second,
		maxBytes:    DefaultMaxToolOutputChars,
		sandboxMode: CommandSandboxAuto,
		sandbox:     sandbox,
	}
	_, err := tool.Run(context.Background(), map[string]any{
		"command": "sh",
		"args":    []any{"-c", "echo pwned > " + outside},
	})
	if err == nil {
		t.Fatal("sandbox let the command write outside the work directory")
	}
	if _, statErr := os.Stat(outside); statErr == nil {
		t.Fatalf("file was created outside the work directory: %s", outside)
	}
}

// TestRunCommandSandboxBlocksNetworkByDefault 覆盖第二条边界：命令即使被放行，
// 默认也不该能把工作目录里的东西发出去。
func TestRunCommandSandboxBlocksNetworkByDefault(t *testing.T) {
	sandbox := detectCommandSandbox()
	if !sandbox.available() {
		t.Skipf("no sandbox available on %s", runtime.GOOS)
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is unavailable")
	}
	tool := &RunCommandTool{
		root:        t.TempDir(),
		allowlist:   commandAllowlistSet([]string{"curl"}),
		timeout:     10 * time.Second,
		maxBytes:    DefaultMaxToolOutputChars,
		sandboxMode: CommandSandboxAuto,
		sandbox:     sandbox,
	}
	// 目标是本机回环上的任意端口：连得上说明网络没断，连不上正是预期。
	if _, err := tool.Run(context.Background(), map[string]any{
		"command": "curl",
		"args":    []any{"-sS", "--max-time", "3", "http://127.0.0.1:9/"},
	}); err == nil {
		t.Fatal("sandbox allowed network access while it should be denied by default")
	}
}

// TestRunCommandSandboxAllowsWriteInsideWorkdir 保证沙盒没有把正常用途一起挡掉。
func TestRunCommandSandboxAllowsWriteInsideWorkdir(t *testing.T) {
	sandbox := detectCommandSandbox()
	if !sandbox.available() {
		t.Skipf("no sandbox available on %s", runtime.GOOS)
	}
	root := t.TempDir()
	tool := &RunCommandTool{
		root:        root,
		allowlist:   commandAllowlistSet([]string{"sh"}),
		timeout:     10 * time.Second,
		maxBytes:    DefaultMaxToolOutputChars,
		sandboxMode: CommandSandboxAuto,
		sandbox:     sandbox,
	}
	output, err := tool.Run(context.Background(), map[string]any{
		"command": "sh",
		"args":    []any{"-c", "echo ok > inside.txt && cat inside.txt"},
	})
	if err != nil {
		t.Fatalf("sandbox blocked a legitimate write inside the work directory: %v", err)
	}
	var payload struct {
		Sandbox string `json:"sandbox"`
		Output  string `json:"output"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Sandbox != sandbox.kind {
		t.Fatalf("result sandbox = %q, want %q", payload.Sandbox, sandbox.kind)
	}
	if !strings.Contains(payload.Output, "ok") {
		t.Fatalf("output = %q", payload.Output)
	}
	if _, err := os.Stat(filepath.Join(root, "inside.txt")); err != nil {
		t.Fatalf("file inside the work directory was not created: %v", err)
	}
}
