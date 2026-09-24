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
	"time"
)

const codingLoginSecret = "chatgpt-device-login-SHOULD-NOT-LEAK"

// seedCodingRuntime 按 assistant 包的布局放好编码代理的登录态：Codex 设备登录的
// auth.json、交给 Claude Code 当 CLAUDE_CONFIG_DIR 的 .credentials.json，以及同级
// 不含凭据的安装目录。
func seedCodingRuntime(t *testing.T, workDir string) (authFile, claudeCreds, installed string) {
	t.Helper()
	base := filepath.Join(workDir, CodingRuntimeDirName)
	authFile = filepath.Join(base, "auth", "codex", "default", "auth.json")
	claudeCreds = filepath.Join(base, "state", "claude", ".credentials.json")
	installed = filepath.Join(base, "claude", "README.txt")
	for path, body := range map[string]string{
		authFile:    `{"tokens":{"access_token":"` + codingLoginSecret + `"}}`,
		claudeCreds: `{"claudeAiOauth":{"accessToken":"` + codingLoginSecret + `"}}`,
		installed:   "managed install",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return authFile, claudeCreds, installed
}

// 编码代理的登录令牌就放在工作目录里：文件工具读、搜、列、写都不能碰到。
func TestFileToolsRefuseCodingRuntimeCredentials(t *testing.T) {
	workDir := t.TempDir()
	seedCodingRuntime(t, workDir)
	registry, err := NewDefaultToolRegistry(Config{WorkDir: workDir, FileWriteEnabled: true}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	run := func(name string, input map[string]any) (string, error) {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("%s 没注册", name)
		}
		return tool.Run(context.Background(), input)
	}
	for _, rel := range []string{
		"coding-runtime/auth/codex/default/auth.json",
		"coding-runtime/state/claude/.credentials.json",
		// CLI 以后新写的任何文件同样挡住：按目录挡，不按文件名。
		"coding-runtime/state/claude/.claude.json",
	} {
		if out, err := run("read_file", map[string]any{"path": rel}); err == nil {
			t.Fatalf("read_file 读出了 %s：%s", rel, out)
		}
		if _, err := run("write_file", map[string]any{"path": rel, "content": "{}"}); err == nil {
			t.Fatalf("write_file 写进了 %s", rel)
		}
	}
	if out, err := run("grep", map[string]any{"pattern": "SHOULD-NOT-LEAK"}); err == nil && strings.Contains(out, codingLoginSecret) {
		t.Fatalf("grep 把登录令牌捞出来了：%s", out)
	}
	if out, err := run("find_files", map[string]any{"pattern": "**/*.json"}); err == nil && (strings.Contains(out, "auth.json") || strings.Contains(out, "credentials")) {
		t.Fatalf("find_files 列出了登录态文件：%s", out)
	}
	for _, rel := range []string{"coding-runtime/auth", "coding-runtime/state/claude"} {
		if out, err := run("list_files", map[string]any{"path": rel}); err == nil {
			t.Fatalf("list_files 列出了凭据目录 %s：%s", rel, out)
		}
	}
	listed, err := run("list_files", map[string]any{"path": "coding-runtime"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(listed, `"auth"`) || strings.Contains(listed, `"state"`) {
		t.Fatalf("list_files 在 coding-runtime 下列出了凭据目录：%s", listed)
	}
	// 同级的安装目录不是凭据，别把整个 coding-runtime 锁死。
	if !strings.Contains(listed, `"claude"`) {
		t.Fatalf("安装目录从列表里消失了：%s", listed)
	}
	if out, err := run("read_file", map[string]any{"path": "coding-runtime/claude/README.txt"}); err != nil || !strings.Contains(out, "managed install") {
		t.Fatalf("安装目录里的普通文件读不了：%v %s", err, out)
	}
}

// 发附件、看图走 WorkspaceFileProtected，和文件工具同一份名单。软链接绕不过去。
func TestWorkspaceFileProtectedCoversCodingRuntime(t *testing.T) {
	workDir := t.TempDir()
	authFile, _, _ := seedCodingRuntime(t, workDir)
	cfg := Config{WorkDir: workDir}.WithDefaults()
	for _, rel := range []string{
		"coding-runtime/auth/codex/default/auth.json",
		"coding-runtime/state/claude/.credentials.json",
		"coding-runtime/auth",
	} {
		if !WorkspaceFileProtected(cfg, rel) {
			t.Fatalf("%s 没被挡住", rel)
		}
	}
	if err := os.Symlink(authFile, filepath.Join(workDir, "innocent.json")); err == nil {
		if !WorkspaceFileProtected(cfg, "innocent.json") {
			t.Fatal("指向登录态的软链接绕过了拦截")
		}
	}
	for _, rel := range []string{"coding-runtime/claude/README.txt", "coding-runtime/authors.txt", "notes/auth.json"} {
		if WorkspaceFileProtected(cfg, rel) {
			t.Fatalf("误伤了普通文件 %s", rel)
		}
	}
}

// 沙盒按目录挡：SBPL 用 subpath 挡读、并在放开工作目录写入之后再挡写；bubblewrap
// 用空 tmpfs 盖住整个目录。
func TestSandboxHidesCodingRuntimeCredentialDirs(t *testing.T) {
	workDir := t.TempDir()
	seedCodingRuntime(t, workDir)
	secrets := agentProtectedFiles(Config{WorkDir: workDir}.WithDefaults()).existingPaths()
	authDir, _ := filepath.Abs(filepath.Join(workDir, CodingRuntimeDirName, "auth"))
	found := false
	for _, path := range secrets {
		if path == authDir {
			found = true
		}
	}
	if !found {
		t.Fatalf("凭据目录没交给沙盒：%v", secrets)
	}

	profile := sandboxExecProfile(workDir, false, secrets)
	denyRead := `(deny file-read* (subpath ` + sbplString(authDir) + `))`
	denyWrite := `(deny file-write* (subpath ` + sbplString(authDir) + `))`
	allowWrite := `(allow file-write* (subpath ` + sbplString(workDir) + `))`
	if !strings.Contains(profile, denyRead) || !strings.Contains(profile, denyWrite) {
		t.Fatalf("SBPL 没有按目录挡住登录态：%s", profile)
	}
	if strings.Index(profile, denyWrite) < strings.Index(profile, allowWrite) {
		t.Fatalf("挡写的规则排在放开工作目录写入之前，等于没挡：%s", profile)
	}

	cmd := wrapWithBubblewrap("bwrap")(context.Background(), workDir, false, secrets, "cat", nil)
	if joined := strings.Join(cmd.Args, " "); !strings.Contains(joined, "--tmpfs "+authDir) {
		t.Fatalf("bubblewrap 没有盖住凭据目录：%v", cmd.Args)
	}
}

// 真跑一次：白名单里有 cat 时，沙盒里也读不出登录令牌。
func TestRunCommandSandboxCannotReadCodingLogin(t *testing.T) {
	sandbox := detectCommandSandbox()
	if !sandbox.available() {
		t.Skipf("no sandbox available on %s", runtime.GOOS)
	}
	workDir := t.TempDir()
	seedCodingRuntime(t, workDir)
	cfg := Config{WorkDir: workDir}.WithDefaults()
	tool := &RunCommandTool{
		root:        workDir,
		allowlist:   commandAllowlistSet([]string{"cat"}),
		timeout:     10 * time.Second,
		maxBytes:    DefaultMaxToolOutputChars,
		sandboxMode: CommandSandboxAuto,
		sandbox:     sandbox,
		protected:   agentProtectedFiles(cfg),
	}
	for _, rel := range []string{
		"coding-runtime/auth/codex/default/auth.json",
		"coding-runtime/state/claude/.credentials.json",
	} {
		out, _ := tool.Run(context.Background(), map[string]any{"command": "cat", "args": []any{rel}})
		if strings.Contains(out, codingLoginSecret) {
			t.Fatalf("沙盒里 cat 读出了 %s：%s", rel, out)
		}
	}
	out, err := tool.Run(context.Background(), map[string]any{"command": "cat", "args": []any{"coding-runtime/claude/README.txt"}})
	if err != nil || !strings.Contains(out, "managed install") {
		t.Fatalf("沙盒把普通文件也挡了：%v %s", err, out)
	}
}
