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
	"time"
)

func TestDefaultRegistryDoesNotExposeRunCommand(t *testing.T) {
	registry, err := NewDefaultToolRegistry(Config{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("run_command"); ok {
		t.Fatal("run_command must require an explicit command allowlist")
	}
}

func TestSafePathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := safePath(root, "escape/secret.txt"); err == nil {
		t.Fatal("safePath followed a symlink outside the workdir")
	}
}

// TestSafePathRejectsEscapes 验证对应功能场景。
func TestSafePathRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	if _, err := safePath(root, "../secret.txt"); err == nil {
		t.Fatal("expected escape to be rejected")
	}
	if _, err := safePath(root, "/etc/passwd"); err == nil {
		t.Fatal("expected absolute path to be rejected")
	}
	got, err := safePath(root, "sub/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "sub", "file.txt")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

// TestReadFileToolLimitsContent 验证对应功能场景。
func TestReadFileToolLimitsContent(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "abcdef")
	tool := &ReadFileTool{root: root, maxBytes: 3}
	got, err := tool.Run(context.Background(), map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// 结果不再包 JSON：字节上限照样生效，截断也照样说明，只是换成一行人话。
	if !strings.Contains(got, "已在中途截断") || !strings.HasSuffix(got, "abc") {
		t.Fatalf("unexpected output: %s", got)
	}
}

// TestRunCommandToolRunsAllowedCommand 验证命令工具执行白名单命令并返回结构化结果。
func TestRunCommandToolRunsAllowedCommand(t *testing.T) {
	root := t.TempDir()
	tool := &RunCommandTool{
		root:      root,
		allowlist: map[string]bool{"go": true},
		timeout:   time.Duration(DefaultCommandTimeoutMS) * time.Millisecond,
		maxBytes:  DefaultMaxToolOutputChars,
	}
	got, err := tool.Run(context.Background(), map[string]any{"command": "go", "args": []any{"version"}})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Command  string `json:"command"`
		ExitCode int    `json:"exit_code"`
		Output   string `json:"output"`
	}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v, json = %s", err, got)
	}
	if payload.Command != "go" || payload.ExitCode != 0 || !strings.Contains(payload.Output, "go version") {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestRunCommandToolPreservesCompleteFailureOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	message := strings.Repeat("failure-output-", 80) + "tail-marker"
	tool := &RunCommandTool{
		root:      t.TempDir(),
		allowlist: map[string]bool{"sh": true},
		timeout:   time.Duration(DefaultCommandTimeoutMS) * time.Millisecond,
		maxBytes:  32,
	}
	_, err := tool.Run(context.Background(), map[string]any{
		"command": "sh",
		"args":    []any{"-c", `printf '%s' "$1" >&2; exit 7`, "sh", message},
	})
	if err == nil {
		t.Fatal("expected command failure")
	}
	var payload struct {
		ExitCode  int    `json:"exit_code"`
		Truncated bool   `json:"truncated"`
		Output    string `json:"output"`
	}
	if unmarshalErr := json.Unmarshal([]byte(err.Error()), &payload); unmarshalErr != nil {
		t.Fatalf("error is not structured JSON: %v; error = %s", unmarshalErr, err)
	}
	if payload.ExitCode != 7 || payload.Truncated || payload.Output != message {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestRunCommandToolStillLimitsSuccessfulOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	message := strings.Repeat("success-output-", 20)
	tool := &RunCommandTool{
		root:      t.TempDir(),
		allowlist: map[string]bool{"sh": true},
		timeout:   time.Duration(DefaultCommandTimeoutMS) * time.Millisecond,
		maxBytes:  32,
	}
	got, err := tool.Run(context.Background(), map[string]any{
		"command": "sh",
		"args":    []any{"-c", `printf '%s' "$1"`, "sh", message},
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Truncated bool   `json:"truncated"`
		Output    string `json:"output"`
	}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Truncated || len(payload.Output) != 32 {
		t.Fatalf("payload = %#v", payload)
	}
}

// TestRunCommandToolRejectsBlockedCommand 验证命令工具拒绝非白名单命令和路径命令。
func TestRunCommandToolRejectsBlockedCommand(t *testing.T) {
	root := t.TempDir()
	tool := &RunCommandTool{
		root:      root,
		allowlist: map[string]bool{"go": true},
		timeout:   time.Duration(DefaultCommandTimeoutMS) * time.Millisecond,
		maxBytes:  DefaultMaxToolOutputChars,
	}
	if _, err := tool.Run(context.Background(), map[string]any{"command": "python3"}); err == nil {
		t.Fatal("expected blocked command error")
	}
	if _, err := tool.Run(context.Background(), map[string]any{"command": "../go"}); err == nil {
		t.Fatal("expected path command error")
	}
}

// TestValidateBrowserURL 验证浏览器工具只接受 http/https URL。
func TestValidateBrowserURL(t *testing.T) {
	for _, value := range []string{"http://127.0.0.1:5173", "https://example.com"} {
		if err := validateBrowserURL(value); err != nil {
			t.Fatalf("validateBrowserURL(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"file:///etc/passwd", "javascript:alert(1)", "https://"} {
		if err := validateBrowserURL(value); err == nil {
			t.Fatalf("validateBrowserURL(%q) error = nil", value)
		}
	}
}

// writeTestFile 封装当前模块的 writeTestFile 逻辑。
func writeTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestToolDefinitionsCarryRealSchemas(t *testing.T) {
	registry, err := NewDefaultToolRegistry(Config{WorkDir: t.TempDir(), CommandAllowlist: []string{"echo"}})
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) == 0 {
		t.Fatal("默认注册表没有暴露任何工具")
	}
	for _, definition := range definitions {
		if _, ok := definition.Parameters["properties"].(map[string]any); !ok {
			t.Fatalf("%s 没有声明参数: %#v", definition.Name, definition.Parameters)
		}
		if definition.Parameters["additionalProperties"] != false {
			t.Fatalf("%s 仍然接受未声明参数: %#v", definition.Name, definition.Parameters)
		}
		if strings.Contains(definition.Description, "input: {") {
			t.Fatalf("%s 的参数契约还写在描述散文里: %s", definition.Name, definition.Description)
		}
	}
}

type verboseTool struct{ countingTool }

func (t *verboseTool) Description() string {
	return "第一句说明工具用途。" + strings.Repeat("后续是只有工具定义才需要携带的详细行为规则。", 30)
}

func TestSystemPromptCatalogDoesNotRepeatFullDescriptions(t *testing.T) {
	registry := NewToolRegistry(&verboseTool{countingTool{name: "verbose"}})
	catalog := registry.SystemPromptCatalog()
	full := registry.Descriptions()
	if len([]rune(catalog)) >= len([]rune(full)) {
		t.Fatalf("目录没有比完整清单更短: %d vs %d", len([]rune(catalog)), len([]rune(full)))
	}
	if !strings.Contains(catalog, "verbose") || !strings.Contains(catalog, "第一句说明工具用途") {
		t.Fatalf("目录丢掉了工具身份: %s", catalog)
	}
	// 权威文本仍然通过工具定义送达模型。
	definitions := registry.Definitions()
	if len(definitions) != 1 || definitions[0].Description != (&verboseTool{}).Description() {
		t.Fatalf("工具定义丢掉了完整描述: %#v", definitions)
	}
}

func TestFinishAndSearchRequestStrictDecoding(t *testing.T) {
	registry := NewToolRegistry(&WebSearchTool{})
	definitions := (&Runner{registry: registry, cfg: Config{}.WithDefaults()}).turnDefinitions(newClaimEvidenceLedger(), false)
	strictByName := map[string]bool{}
	for _, definition := range definitions {
		strictByName[definition.Name] = definition.Strict
	}
	// 这两个是仅有的、一旦结构出错就必然花掉一整轮修复的工具，因此要求供应商
	// 在解码层约束。
	if !strictByName[webSearchToolName] || !strictByName[finalizeToolName] {
		t.Fatalf("strict 标记=%#v", strictByName)
	}
}
