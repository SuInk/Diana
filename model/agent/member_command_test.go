// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// 成员策略从零放行：家目录、工作区这些不在白名单里，写入只有成员目录，没有网络。
func TestMemberSandboxExecProfileIsAllowlist(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "member")
	profile := memberSandboxExecProfile(memberSandboxSpec{dir: dir, readRoots: []string{"/opt/py/3.12"}})
	if strings.Contains(profile, "(allow file-read*)") {
		t.Fatal("member profile opens every read")
	}
	if strings.Contains(profile, "network") {
		t.Fatalf("member profile mentions network: %s", profile)
	}
	if strings.Contains(profile, "process-fork") {
		t.Fatal("member profile allows fork")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.Contains(profile, sbplString(home)+")") {
		t.Fatalf("home %s is readable", home)
	}
	for _, want := range []string{"(subpath " + sbplString(dir) + ")", `(subpath "/opt/py/3.12")`, `(deny file-read* (subpath "/usr/local/var"))`} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile lacks %s: %s", want, profile)
		}
	}
	write := profile[strings.Index(profile, "(allow file-write*"):]
	if strings.Contains(write, "/tmp") && !strings.Contains(dir, "/tmp") {
		t.Fatalf("member profile can write /tmp: %s", write)
	}
}

// bwrap 不再整根只读挂载，成员目录挂到 /work，命名空间全部摘掉。
func TestMemberBubblewrapArgsStartFromEmptyRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "member")
	args := memberBubblewrapArgs(memberSandboxSpec{dir: dir, command: "/usr/bin/python3", args: []string{"-c", "print(1)"}, limits: DefaultMemberCommandLimits})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--ro-bind / /") {
		t.Fatal("member sandbox binds the whole root")
	}
	if !slices.Contains(args, "--unshare-all") {
		t.Fatalf("args lack --unshare-all: %v", args)
	}
	if !strings.Contains(joined, "--bind "+dir+" /work") {
		t.Fatalf("member dir not bound at /work: %v", args)
	}
	sep := slices.Index(args, "--")
	if sep < 0 || args[sep+1] != "/bin/sh" || !strings.Contains(args[sep+3], "ulimit -t 10") {
		t.Fatalf("command not wrapped with limits: %v", args[sep:])
	}
	if args[len(args)-3] != "/usr/bin/python3" {
		t.Fatalf("command missing: %v", args)
	}
}

// 包住成员目录的额外读取路径要去掉，不然上一级的别人目录也看得见。
func TestMemberReadRootsOutsideDropsAncestors(t *testing.T) {
	got := memberReadRootsOutside([]string{"/srv/ws", "/srv/ws/member-runs", "/opt/py"}, "/srv/ws/member-runs/g/u")
	if !slices.Equal(got, []string{"/opt/py"}) {
		t.Fatalf("got %v", got)
	}
}

// 解释器前缀不能是家目录本身或它的上级，也不能浅到 /usr 这一级。
func TestMemberPrefixAcceptable(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	for _, prefix := range []string{"/", "/usr", home, filepath.Dir(home)} {
		if memberPrefixAcceptable(prefix) {
			t.Fatalf("%s accepted", prefix)
		}
	}
	if !memberPrefixAcceptable(filepath.Join(home, ".pyenv", "versions", "3.12.1")) {
		t.Fatal("pyenv prefix rejected")
	}
}

func TestMemberCommandRejectsUnlistedProgram(t *testing.T) {
	sandbox := memberSandbox{kind: "fake", build: nil}
	_, err := runMemberCommand(context.Background(), sandbox, MemberCommandRequest{Dir: t.TempDir(), Command: "bash"})
	if err == nil || !strings.Contains(err.Error(), "python3") {
		t.Fatalf("err = %v", err)
	}
}

// 产物只认这次新增或改过的普通文件：符号链接、.tmp 里的东西和没动过的旧文件都不算。
func TestChangedMemberFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("old.txt", "old")
	before := snapshotMemberDir(dir)
	write("out/song.mid", "MThd")
	write(".tmp/scratch", "x")
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	files, more := changedMemberFiles(dir, before, DefaultMemberCommandLimits)
	if more != 0 || len(files) != 1 || files[0].Name != "out/song.mid" || files[0].Size != 4 || string(files[0].Data) != "MThd" {
		t.Fatalf("files = %#v more=%d", files, more)
	}
}

func TestPruneMemberDirRemovesExpired(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "a", "stale.txt")
	fresh := filepath.Join(dir, "fresh.txt")
	_ = os.MkdirAll(filepath.Dir(stale), 0o700)
	_ = os.WriteFile(stale, []byte("x"), 0o600)
	_ = os.WriteFile(fresh, []byte("x"), 0o600)
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(stale, old, old)
	pruneMemberDir(dir, 24*time.Hour, time.Now())
	if _, err := os.Stat(filepath.Dir(stale)); !os.IsNotExist(err) {
		t.Fatal("expired file and its empty dir kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh file removed")
	}
}

// 沙盒不可用时成员命令直接拒绝，不退回裸跑。
func TestRunMemberCommandRefusesWithoutSandbox(t *testing.T) {
	restoreFn, restoreProbe := memberSandboxFn, memberSandboxProbe
	t.Cleanup(func() {
		memberSandboxFn, memberSandboxProbe = restoreFn, restoreProbe
		memberSandboxOnce = sync.Once{}
	})
	memberSandboxOnce = sync.Once{}
	memberSandboxFn = func() (memberSandbox, string) {
		return memberSandbox{kind: "bubblewrap", build: memberBubblewrapCommand("bwrap"), view: func(string) string { return "/work" }}, ""
	}
	memberSandboxProbe = func(memberSandbox) error { return errors.New("No permissions to create new namespace") }
	if kind, reason := MemberCommandAvailable(); kind != "" || !strings.Contains(reason, "No permissions") {
		t.Fatalf("kind=%q reason=%q", kind, reason)
	}
	_, err := RunMemberCommand(context.Background(), MemberCommandRequest{Dir: t.TempDir(), Command: "python3"})
	if err == nil || !strings.Contains(err.Error(), "不可用") {
		t.Fatalf("err = %v", err)
	}
}

// 下面在真沙盒里跑，本机没有可用的成员沙盒或 python3 时跳过。

func realMemberSandbox(t *testing.T) memberSandbox {
	t.Helper()
	sandbox := detectMemberSandbox()
	if !sandbox.available() {
		t.Skipf("member sandbox unavailable: %s", memberSandboxReason)
	}
	return sandbox
}

func memberTestDir(t *testing.T) (root, dir string) {
	t.Helper()
	root = t.TempDir()
	dir = filepath.Join(root, "member-runs", "g1", "u1")
	return root, dir
}

func runMemberPython(t *testing.T, sandbox memberSandbox, dir, code string, limits MemberCommandLimits) MemberCommandResult {
	t.Helper()
	if _, err := lookupMemberProgram("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	result, err := runMemberCommand(context.Background(), sandbox, MemberCommandRequest{Dir: dir, Command: "python3", Args: []string{"-c", code}, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMemberCommandPythonWritesFile(t *testing.T) {
	sandbox := realMemberSandbox(t)
	_, dir := memberTestDir(t)
	result := runMemberPython(t, sandbox, dir, `open("song.mid","wb").write(b"MThd\x00\x00\x00\x06");print("ok")`, DefaultMemberCommandLimits)
	if result.ExitCode != 0 || !strings.Contains(result.Output, "ok") {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Files) != 1 || result.Files[0].Name != "song.mid" || result.Files[0].Size != 8 {
		t.Fatalf("files = %#v", result.Files)
	}
}

func TestMemberCommandCannotReadOutsideOwnDir(t *testing.T) {
	sandbox := realMemberSandbox(t)
	root, dir := memberTestDir(t)
	sibling := filepath.Join(root, "member-runs", "g2", "u2", "secret.txt")
	shared := filepath.Join(root, "config.json")
	_ = os.MkdirAll(filepath.Dir(sibling), 0o700)
	_ = os.WriteFile(sibling, []byte("other-group"), 0o600)
	_ = os.WriteFile(shared, []byte("shared"), 0o600)
	home, _ := os.UserHomeDir()
	for _, target := range []string{sibling, shared, home} {
		code := "import os,sys\np=sys.argv[1]\ntry:\n  print(os.listdir(p) if os.path.isdir(p) else open(p).read())\n  print('LEAK')\nexcept Exception as e:\n  print('denied', type(e).__name__)"
		result, err := runMemberCommand(context.Background(), sandbox, MemberCommandRequest{Dir: dir, Command: "python3", Args: []string{"-c", code, target}})
		if err != nil {
			if strings.Contains(err.Error(), "没有 python3") {
				t.Skip("python3 not installed")
			}
			t.Fatal(err)
		}
		if strings.Contains(result.Output, "LEAK") || !strings.Contains(result.Output, "denied") {
			t.Fatalf("read %s from member sandbox: %s", target, result.Output)
		}
	}
	// 也写不出去。
	escape, _ := runMemberCommand(context.Background(), sandbox, MemberCommandRequest{Dir: dir, Command: "python3", Args: []string{"-c", "import sys\ntry:\n  open(sys.argv[1],'w').write('x');print('LEAK')\nexcept Exception as e:\n  print('denied')", filepath.Join(root, "planted.txt")}})
	if strings.Contains(escape.Output, "LEAK") {
		t.Fatal("member wrote outside its dir")
	}
}

func TestMemberCommandHasNoNetwork(t *testing.T) {
	sandbox := realMemberSandbox(t)
	_, dir := memberTestDir(t)
	result := runMemberPython(t, sandbox, dir, "import socket\ntry:\n  socket.create_connection(('1.1.1.1',80),timeout=3);print('LEAK')\nexcept Exception as e:\n  print('denied')", DefaultMemberCommandLimits)
	if !strings.Contains(result.Output, "denied") {
		t.Fatalf("network reachable: %s", result.Output)
	}
}

func TestMemberCommandLimits(t *testing.T) {
	sandbox := realMemberSandbox(t)
	_, dir := memberTestDir(t)
	limits := DefaultMemberCommandLimits
	limits.Wall = 2 * time.Second
	limits.OutputBytes = 64
	limits.MemoryBytes = 128 << 20
	limits.FileBytes = 1 << 20

	if r := runMemberPython(t, sandbox, dir, "print('x'*1000)", limits); !r.Truncated || len(r.Output) > 64 {
		t.Fatalf("output not capped: %d %v", len(r.Output), r.Truncated)
	}
	if r := runMemberPython(t, sandbox, dir, "import time\ntime.sleep(10)", limits); !r.TimedOut || r.ExitCode == 0 {
		t.Fatalf("timeout not enforced: %#v", r)
	}
	if r := runMemberPython(t, sandbox, dir, "import time\nb=bytearray(400<<20)\nfor i in range(0,len(b),4096): b[i]=1\ntime.sleep(5)", limits); r.LimitExceeded != "memory" {
		t.Fatalf("memory not enforced: %#v", r)
	}
	if r := runMemberPython(t, sandbox, dir, "open('big','wb').write(b'x'*(4<<20))", limits); r.ExitCode == 0 {
		t.Fatalf("file size not enforced: %#v", r)
	}
	forks := "import os,time\nfor i in range(64):\n  if os.fork()==0:\n    time.sleep(5); os._exit(0)\ntime.sleep(1.5)\nprint('LEAK')"
	if runtime.GOOS == "darwin" {
		forks = "import os\ntry:\n  os.fork();print('LEAK')\nexcept OSError:\n  print('denied')"
	}
	if r := runMemberPython(t, sandbox, dir, forks, limits); strings.Contains(r.Output, "LEAK") && r.LimitExceeded == "" {
		t.Fatalf("process count not enforced: %#v", r)
	}
}
