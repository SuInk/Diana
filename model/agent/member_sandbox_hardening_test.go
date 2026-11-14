// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestMemberSandboxProfileBlocksHardlinkAndEscapeIPC(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "member")
	profile := memberSandboxExecProfile(memberSandboxSpec{dir: dir})
	for _, want := range []string{
		"(deny file-link)",
		`"com.apple.coreservices.launchservicesd"`,
		`"com.apple.Pasteboard.pboard"`,
		`"com.apple.securityd"`,
		`"com.apple.nsurlsessiond"`,
	} {
		if !strings.Contains(profile, want) {
			t.Fatalf("member profile missing %s: %s", want, profile)
		}
	}
	if runtime.GOOS == "darwin" {
		if strings.Contains(profile, `(subpath "/private/etc")`) {
			t.Fatalf("member profile still exposes full /private/etc: %s", profile)
		}
		if strings.Contains(profile, `(subpath "/dev")`) {
			t.Fatalf("member profile still exposes full /dev: %s", profile)
		}
	}
}

func TestCommandSandboxBlocksProxyAndAgentEnvAndSupportsStdin(t *testing.T) {
	cleaned := commandEnvironmentFor([]string{
		"PATH=/usr/bin:/bin",
		"SSH_AUTH_SOCK=/tmp/ssh.sock",
		"GPG_AGENT_INFO=/tmp/gpg.sock",
		"NORMAL_VAR=kept",
	}, "")
	joined := strings.Join(cleaned, "\n")
	if strings.Contains(joined, "SSH_AUTH_SOCK=") || strings.Contains(joined, "GPG_AGENT_INFO=") {
		t.Fatalf("agent sockets leaked into command env: %v", cleaned)
	}
	if !strings.Contains(joined, "NORMAL_VAR=kept") {
		t.Fatalf("normal env lost: %v", cleaned)
	}

	noProxy := stripNetworkProxyEnv([]string{
		"HTTP_PROXY=http://127.0.0.1:7890",
		"https_proxy=http://127.0.0.1:7890",
		"ALL_PROXY=socks5://127.0.0.1:7890",
		"NORMAL_VAR=kept",
	})
	proxyJoined := strings.Join(noProxy, "\n")
	for _, forbidden := range []string{"HTTP_PROXY=", "https_proxy=", "ALL_PROXY="} {
		if strings.Contains(proxyJoined, forbidden) {
			t.Fatalf("proxy env %s leaked: %v", forbidden, noProxy)
		}
	}
	if !strings.Contains(proxyJoined, "NO_PROXY=*") {
		t.Fatalf("expected NO_PROXY=*, got %v", noProxy)
	}

	tool := &RunCommandTool{
		root:        t.TempDir(),
		timeout:     5 * time.Second,
		maxBytes:    4096,
		sandboxMode: CommandSandboxOff,
	}
	out, err := tool.Run(context.Background(), map[string]any{
		"command": "cat",
		"stdin":   "hello-stdin-payload",
	})
	if err != nil {
		t.Fatalf("RunCommandTool with stdin failed: %v", err)
	}
	if !strings.Contains(out, "hello-stdin-payload") {
		t.Fatalf("stdin did not reach command: %s", out)
	}
}

func TestChangedMemberFilesRejectsHardlinkedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix hardlink test")
	}
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("top-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	before := snapshotMemberDir(dir)
	if err := os.Link(secret, filepath.Join(dir, "linked.txt")); err != nil {
		t.Skipf("hardlink unsupported on temp filesystem: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "genuine.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, _ := changedMemberFiles(dir, before, DefaultMemberCommandLimits)
	if len(files) != 1 || files[0].Name != "genuine.txt" {
		t.Fatalf("hardlinked file was not rejected: %#v", files)
	}
}

func TestMemberSandboxBlocksNetworkEscapeAndRunsToolkitEndToEnd(t *testing.T) {
	if kind, reason := MemberCommandAvailable(); kind == "" {
		t.Skipf("member sandbox unavailable: %s", reason)
	}
	if _, err := lookupMemberProgram("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()

	// 1. 验证沙盒严格拦截联网、硬链接与宿主敏感目录读取
	escapeScript := `
import os, socket, sys

blocked = []
try:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(1.0)
    s.connect(("127.0.0.1", 80))
    s.close()
    print("NET_ALLOWED")
    sys.exit(2)
except OSError:
    blocked.append("net")

try:
    os.link("/etc/hosts", "escape_hosts")
    print("LINK_ALLOWED")
    sys.exit(3)
except OSError:
    blocked.append("link")

if os.path.exists("/private/etc/ssh"):
    try:
        os.listdir("/private/etc/ssh")
        print("SSH_ETC_ALLOWED")
        sys.exit(4)
    except OSError:
        blocked.append("etc_ssh")

print("BLOCKED:" + ",".join(blocked))
`
	securityRes, err := RunMemberCommand(context.Background(), MemberCommandRequest{
		Dir:        dir,
		Command:    "python3",
		Args:       []string{"check_security.py"},
		WriteFiles: map[string][]byte{"check_security.py": []byte(escapeScript)},
	})
	if err != nil {
		t.Fatalf("security check execution error: %v", err)
	}
	if securityRes.ExitCode != 0 || !strings.Contains(securityRes.Output, "BLOCKED:net") {
		t.Fatalf("sandbox security boundary failed: exit=%d out=%q", securityRes.ExitCode, securityRes.Output)
	}
	for _, f := range securityRes.Files {
		if f.Name == "check_security.py" {
			t.Fatalf("staged write_files script polluted produced files: %#v", securityRes.Files)
		}
	}

	// 2. 验证内置零依赖 diana_sandbox 工具箱、inputs 与 stdin 全链路工作
	toolkitScript := `
import json, sys
import diana_sandbox as ds

stdin_text = sys.stdin.read().strip()
rows = ds.read_csv("inputs/metrics.csv")
vals = [float(r["score"]) for r in rows]
stats = ds.describe_numbers(vals)

canvas = ds.Canvas(120, 80, bg=(240, 248, 255))
canvas.fill_rect(10, 10, 100, 60, (59, 130, 246))
canvas.draw_text(16, 28, "DIANA OK", color=(255, 255, 255), scale=1)
canvas.save_png("out/badge.png")

ds.svg_bar_chart("out/scores.svg", [r["name"] for r in rows], vals, title="Scores")
ds.synth_notes("out/chime.wav", [("C5", 0.25), ("E5", 0.25)], bpm=160)

midi = ds.MIDIBuilder(bpm=120)
midi.add_chord(0, ["C4", "E4", "G4"], 0, 1.0)
midi.save("out/chord.mid")

html_doc = ds.paper_html(
    title="Diana Sandbox Evaluation",
    authors="Diana Research",
    abstract="Offline deterministic rendering.",
    body_html="<h2>1. Method</h2><p>Strict isolation.</p>",
    columns=2,
    date="2026-11-14",
)
assert not ds._is_existing_file_path("x" * 5000)
ds.write_text("out/paper.html", html_doc)

print(json.dumps({"stdin": stdin_text, "count": stats["count"], "max": stats["max"]}))
`
	csvInput := "name,score\nAlice,90\nBob,95\nCarol,85\n"
	res, err := RunMemberCommand(context.Background(), MemberCommandRequest{
		Dir:        dir,
		Command:    "python3",
		Args:       []string{"build_assets.py"},
		Stdin:      "hello-sandbox\n",
		WriteFiles: map[string][]byte{"build_assets.py": []byte(toolkitScript)},
		Inputs:     map[string][]byte{"metrics.csv": []byte(csvInput)},
	})
	if err != nil {
		t.Fatalf("toolkit run error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("toolkit script failed (exit %d): %s", res.ExitCode, res.Output)
	}
	if !strings.Contains(res.Output, `"stdin": "hello-sandbox"`) || !strings.Contains(res.Output, `"max": 95.0`) {
		t.Fatalf("unexpected toolkit stdout: %s", res.Output)
	}
	byName := map[string][]byte{}
	for _, f := range res.Files {
		byName[f.Name] = f.Data
	}
	if !bytes.HasPrefix(byName["out/badge.png"], []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("badge.png missing valid PNG signature: len=%d", len(byName["out/badge.png"]))
	}
	if !strings.Contains(string(byName["out/scores.svg"]), "<svg") {
		t.Fatalf("scores.svg invalid: %q", string(byName["out/scores.svg"]))
	}
	if !bytes.HasPrefix(byName["out/chime.wav"], []byte("RIFF")) {
		t.Fatalf("chime.wav missing RIFF header: len=%d", len(byName["out/chime.wav"]))
	}
	if !bytes.HasPrefix(byName["out/chord.mid"], []byte("MThd")) {
		t.Fatalf("chord.mid missing MThd header: len=%d", len(byName["out/chord.mid"]))
	}
	paperHTML := string(byName["out/paper.html"])
	for _, want := range []string{"@page", "column-count: 2", "Diana Sandbox Evaluation", "Offline deterministic rendering."} {
		if !strings.Contains(paperHTML, want) {
			t.Fatalf("paper.html missing %q: %s", want, paperHTML)
		}
	}
	if strings.Contains(paperHTML, "http://") || strings.Contains(paperHTML, "https://") {
		t.Fatalf("paper.html must remain 100%% offline without external links: %s", paperHTML)
	}
	if _, leaked := byName["inputs/metrics.csv"]; leaked {
		t.Fatalf("staged input file leaked into produced files: %#v", res.Files)
	}
}

type staticTestTool struct {
	name   string
	output string
}

func (t *staticTestTool) Name() string        { return t.name }
func (t *staticTestTool) Description() string { return "test tool" }
func (t *staticTestTool) Run(context.Context, map[string]any) (string, error) {
	return t.output, nil
}

func TestResearchDetailReviewIncludesRecentUserContextAndLocalTools(t *testing.T) {
	searchTool := &recordingSearchTool{output: `{"status":"ok","results":[{"title":"Release Notes","url":"https://example.com/r","snippet":"v2.1.0 released"}]}`}
	localTool := &staticTestTool{
		name:   "github",
		output: `{"ok":true,"issue":{"number":582,"title":"Fix recall"}}`,
	}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search","input":{"query":"release"}}`,
		`{"action":"tool","tool":"github","input":{"number":582}}`,
		`{"action":"final","content":"你图里的报错码 4029 在 v2.1.0 和 PR #582 里修了"}`,
		`{"unsupported":[]}`,
	}}
	runner, err := NewRunner(client, Config{MaxSteps: 5, ResearchDetailReview: true}, NewToolRegistry(searchTool, localTool))
	if err != nil {
		t.Fatal(err)
	}
	history := []llm.Message{
		{Role: llm.RoleUser, Content: "[历史 10:00:01] 群友: 我的客户端弹出了报错码 4029"},
		{Role: llm.RoleUser, Content: "【当前需要回复的消息】\n这个报错修了吗"},
	}
	resp, err := runner.Run(context.Background(), Request{Messages: history})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text == "" || len(client.requests) != 4 {
		t.Fatalf("resp=%#v requests=%d", resp, len(client.requests))
	}
	reviewPrompt := requestContent(client.requests[3])
	for _, want := range []string{"报错码 4029", "Fix recall", "v2.1.0"} {
		if !strings.Contains(reviewPrompt, want) {
			t.Fatalf("review prompt missing %q:\n%s", want, reviewPrompt)
		}
	}
	var dummy map[string]any
	_ = json.Unmarshal([]byte(`{}`), &dummy)
}

func TestMemberDockerSandboxRunArgsAreOfflineAndHardened(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "never-leak-secret")
	cfg := memberDockerConfig{
		dockerBin:  "/usr/bin/docker",
		dockerHost: "unix:///run/user/1000/docker.sock",
		image:      DefaultMemberSandboxImage,
	}
	spec := memberSandboxSpec{
		dir:     "/tmp/diana-member-group1-user1",
		command: "python3",
		args:    []string{"render.py"},
		limits:  DefaultMemberCommandLimits,
	}
	args := memberDockerRunArgs(cfg, "diana-sbx-test", spec)
	joined := strings.Join(args, " ")

	for _, wantPair := range [][2]string{
		{"--host", "unix:///run/user/1000/docker.sock"},
		{"--name", "diana-sbx-test"},
		{"--network", "none"},
		{"--cap-drop", "ALL"},
		{"--security-opt", "no-new-privileges"},
		{"--ipc", "none"},
		{"--tmpfs", "/dev/shm:ro,nosuid,nodev,noexec,size=64k"},
		{"--memory", "536870912"},
		{"--memory-swap", "536870912"},
		{"--pids-limit", "64"},
		{"--env", "TMPDIR=/tmp"},
		{"--env", "PYTHONPATH=/tmp/_pylib"},
		{"--env", "DBUS_SESSION_BUS_ADDRESS=/dev/null"},
		{"--volume", filepath.Clean("/tmp/diana-member-group1-user1") + ":/work:rw"},
		{"--volume", filepath.Join(filepath.Clean("/tmp/diana-member-group1-user1"), ".tmp") + ":/tmp:rw"},
		{"--workdir", "/work"},
	} {
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == wantPair[0] && args[i+1] == wantPair[1] {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("docker run args missing %v in %v", wantPair, args)
		}
	}
	for _, singleFlag := range []string{"--rm", "--init", "--read-only"} {
		if !strings.Contains(" "+joined+" ", " "+singleFlag+" ") {
			t.Fatalf("docker run args missing %s: %v", singleFlag, args)
		}
	}
	if strings.Contains(joined, "AWS_SECRET_ACCESS_KEY") || strings.Contains(joined, "never-leak-secret") {
		t.Fatalf("host secret leaked into docker run args: %v", args)
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--tmpfs" && strings.HasPrefix(args[i+1], "/tmp") {
			t.Fatalf("docker sandbox must not mount unmonitored writable /tmp tmpfs: %v", args)
		}
	}

	if runtime.GOOS == "windows" {
		return
	}
	// 验证容器后端执行链路与退出码 137 -> LimitExceeded="memory" 映射
	fakeSandbox := memberSandbox{
		kind:        "docker",
		inContainer: true,
		view:        func(string) string { return memberDockerWorkDir },
		build: func(ctx context.Context, s memberSandboxSpec) *exec.Cmd {
			if len(s.args) > 0 && s.args[0] == "oom" {
				return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 137")
			}
			return exec.CommandContext(ctx, "/bin/sh", "-c", "printf 'container-ok'")
		},
	}
	okRes, err := runMemberCommand(context.Background(), fakeSandbox, MemberCommandRequest{
		Dir:     t.TempDir(),
		Command: "chromium",
		Args:    []string{"--version"},
	})
	if err != nil {
		t.Fatalf("container run failed: %v", err)
	}
	if okRes.ExitCode != 0 || okRes.Sandbox != "docker" || okRes.Output != "container-ok" {
		t.Fatalf("unexpected container result: %#v", okRes)
	}

	oomRes, err := runMemberCommand(context.Background(), fakeSandbox, MemberCommandRequest{
		Dir:     t.TempDir(),
		Command: "python3",
		Args:    []string{"oom"},
	})
	if err != nil {
		t.Fatalf("container oom run returned error: %v", err)
	}
	if oomRes.ExitCode != 137 || oomRes.LimitExceeded != "memory" {
		t.Fatalf("expected exit 137 to map LimitExceeded=memory, got %#v", oomRes)
	}

	// 验证 Rootless UID 兼容、容器内部署识别与可用程序过滤
	if got := memberDockerUserSpecFor(true); got != "0:0" {
		t.Fatalf("rootless docker userSpec = %q, want 0:0", got)
	}
	for _, prog := range availableMemberContainerPrograms() {
		if prog == "sips" || prog == "md5" {
			t.Fatalf("macOS-only command %q must not be advertised in Linux container mode", prog)
		}
	}
	t.Setenv("DIANA_DEPLOYMENT", "docker")
	if !isRunningInsideContainer() {
		t.Fatal("expected DIANA_DEPLOYMENT=docker to be detected as running inside container")
	}
	t.Setenv("DIANA_DEPLOYMENT", "")

	// 验证 auto 模式下当 Docker 守护进程临时停摆或挂载被拒时，自动无缝回退原生系统沙盒
	if native, _ := detectNativeMemberSandbox(); native.available() {
		memberDockerCache.mu.Lock()
		memberDockerCache.sandbox = memberSandbox{
			kind:        "docker",
			inContainer: true,
			view:        func(string) string { return memberDockerWorkDir },
			build: func(ctx context.Context, s memberSandboxSpec) *exec.Cmd {
				return exec.CommandContext(ctx, "/bin/sh", "-c", "echo 'docker: Error response from daemon: Mounts denied' >&2; exit 125")
			},
		}
		memberDockerCache.expiresAt = time.Now().Add(time.Minute)
		memberDockerCache.mu.Unlock()
		defer invalidateMemberDockerCache()

		t.Setenv(memberSandboxImageEnv, "diana-sandbox:latest")
		fallbackRes, err := RunMemberCommand(context.Background(), MemberCommandRequest{
			Dir:     t.TempDir(),
			Command: "cat",
			Stdin:   "fallback-native-ok",
		})
		if err != nil {
			t.Fatalf("auto fallback to native sandbox failed: %v", err)
		}
		if fallbackRes.ExitCode != 0 || fallbackRes.Sandbox == "docker" || !strings.Contains(fallbackRes.Output, "fallback-native-ok") {
			t.Fatalf("expected seamless fallback to native sandbox, got %#v", fallbackRes)
		}
	}
}

type parallelReadOnlyStubTool struct {
	slowReadTool
}

func (*parallelReadOnlyStubTool) CodeModeParallel() bool { return true }

func TestCodeModeParallelReadOnlyTools(t *testing.T) {
	for _, roTool := range []CodeModeParallelTool{
		&ListFilesTool{},
		&ReadFileTool{},
		&GrepTool{},
		&FindFilesTool{},
	} {
		if !roTool.CodeModeParallel() {
			t.Fatalf("expected %T to opt into CodeModeParallel", roTool)
		}
	}

	script := testCodeModeTool(t)
	var active, peak atomic.Int32
	hist := &parallelReadOnlyStubTool{
		slowReadTool: slowReadTool{
			name:   "chat_history",
			active: &active,
			peak:   &peak,
			delays: map[string]time.Duration{"sliceA": 50 * time.Millisecond, "sliceB": 50 * time.Millisecond},
		},
	}
	client := &batchClient{first: []llm.ToolCall{{
		ID:   "script",
		Name: codeModeToolName,
		Arguments: map[string]any{
			"code": `const rows = await Promise.all(['sliceA','sliceB'].map(query => tools.chat_history({query}))); return rows.map(r => r.result).join('|');`,
		},
	}}}
	runner, err := NewRunner(client, Config{MaxSteps: 4}, NewToolRegistry(script, hist))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "parallel history"}}}); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 {
		t.Fatalf("expected CodeModeParallelTool peak concurrency 2, got %d", peak.Load())
	}
}
