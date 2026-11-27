// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// 只读工具设了 store 就只经由它读：root 上的同名文件不能被读到。
func TestReadToolsUseFileStore(t *testing.T) {
	ctx := context.Background()
	root, other := t.TempDir(), t.TempDir()
	for dir, body := range map[string]string{root: "root copy\n", other: "store copy\n"} {
		if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "notes", "a.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store := newHostFileStore(other, protectedFiles{})
	read, err := (&ReadFileTool{root: root, store: store}).Run(ctx, map[string]any{"path": "notes/a.txt"})
	if err != nil || !strings.Contains(read, "store copy") {
		t.Fatalf("read_file: %q %v", read, err)
	}
	list, err := (&ListFilesTool{root: root, store: store}).Run(ctx, map[string]any{"path": "notes"})
	if err != nil || !strings.Contains(list, `"a.txt"`) {
		t.Fatalf("list_files: %q %v", list, err)
	}
	grep, err := (&GrepTool{root: root, store: store}).Run(ctx, map[string]any{"pattern": "copy"})
	if err != nil || !strings.Contains(grep, "store copy") || strings.Contains(grep, "root copy") {
		t.Fatalf("grep: %q %v", grep, err)
	}
	find, err := (&FindFilesTool{root: root, store: store}).Run(ctx, map[string]any{"pattern": "*.txt"})
	if err != nil || !strings.Contains(find, "notes/a.txt") {
		t.Fatalf("find_files: %q %v", find, err)
	}
	if _, err := (&GrepTool{root: root, store: store}).Run(ctx, map[string]any{"pattern": "x", "path": "missing"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing search base must say so: %v", err)
	}
}

// 受保护文件经由 store 判断：列目录时藏起来、读的时候拒绝、检索时跳过。
func TestReadToolsRespectStoreBlocked(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret.json"), []byte(`{"key":"hidden"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "open.txt"), []byte("hidden word too"), 0o600); err != nil {
		t.Fatal(err)
	}
	protected := protectedFiles{files: map[string]bool{filepath.Join(root, "secret.json"): true}}
	store := newHostFileStore(root, protected)
	if _, err := (&ReadFileTool{root: root, store: store}).Run(ctx, map[string]any{"path": "secret.json"}); err == nil {
		t.Fatal("protected file was readable")
	}
	list, _ := (&ListFilesTool{root: root, store: store}).Run(ctx, map[string]any{})
	if strings.Contains(list, "secret.json") || !strings.Contains(list, `"protected_hidden": 1`) {
		t.Fatalf("list_files leaked a protected file: %s", list)
	}
	grep, _ := (&GrepTool{root: root, store: store}).Run(ctx, map[string]any{"pattern": "hidden"})
	if strings.Contains(grep, "secret.json") || !strings.Contains(grep, "open.txt") {
		t.Fatalf("grep leaked a protected file: %s", grep)
	}
}

func TestParseBoxStat(t *testing.T) {
	info, err := parseBoxStat("regular file|42|1795700000.250000000|a b.txt")
	if err != nil || info.Name() != "a b.txt" || info.Size() != 42 || info.IsDir() || !info.Mode().IsRegular() {
		t.Fatalf("file: %+v %v", info, err)
	}
	if got := info.ModTime(); !got.Equal(time.Unix(1795700000, 250000000)) {
		t.Fatalf("mtime = %v", got)
	}
	if dir, _ := parseBoxStat("directory|4096|1795700000.0|inbox"); !dir.IsDir() {
		t.Fatal("directory not recognized")
	}
	if empty, _ := parseBoxStat("regular empty file|0|1|e"); !empty.Mode().IsRegular() {
		t.Fatal("empty file not regular")
	}
	if odd, _ := parseBoxStat("fifo|0|1|p"); odd.Mode().IsRegular() || odd.IsDir() {
		t.Fatal("fifo must be irregular")
	}
	if _, err := parseBoxStat("garbage"); err == nil {
		t.Fatal("bad stat output must error")
	}
}

func TestBoxFileStoreResolveAndReadDir(t *testing.T) {
	var gotArgs []string
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		switch args[0] {
		case "inspect":
			return boxInspectReply("x", "c1", "s")
		case "exec":
			if slices.Contains(args, boxReadDirScript) {
				gotArgs = args
				return dockerResult{stdout: []byte("f\t12\t1795700000.5\tnote\nwith newline.txt\x00d\t4096\t1795700000\tinbox\x00l\t7\t1\tlink\x00")}
			}
		}
		return dockerResult{}
	}}
	b := fakeTestBox(fake)
	store := NewBoxFileStore(b)
	for raw, want := range map[string]string{"": ".", "inbox/a.png": "inbox/a.png", "/work/out.gif": "out.gif", b.Root() + "/x": "x"} {
		if got, err := store.Resolve(raw); err != nil || got != want {
			t.Fatalf("Resolve(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := store.Resolve("../escape"); err == nil {
		t.Fatal("escaping path must be rejected")
	}
	infos, err := store.ReadDir(context.Background(), "inbox")
	if err != nil || len(infos) != 3 {
		t.Fatalf("ReadDir: %v %v", infos, err)
	}
	if infos[0].Name() != "note\nwith newline.txt" || infos[0].Size() != 12 || !infos[1].IsDir() || infos[2].Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("entries: %+v", infos)
	}
	if n := len(gotArgs); gotArgs[n-2] != b.Root()+"/inbox" || gotArgs[n-1] != b.Root() || !strings.Contains(strings.Join(gotArgs, " "), "--user 10001:10001") {
		t.Fatalf("readdir must run as the scope uid against the scope root: %v", gotArgs)
	}
}

// 容器脚本的退出码换成和 os.* 一样的错误：不存在的能 errors.Is(fs.ErrNotExist)，工具给同样的提示。
func TestBoxFileStoreErrors(t *testing.T) {
	code := 3
	fake := &fakeDocker{reply: func(args []string, _ []byte) dockerResult {
		if args[0] == "inspect" {
			return boxInspectReply("x", "c1", "s")
		}
		if args[0] == "exec" && slices.Contains(args, boxStatScript) {
			return dockerResult{code: code}
		}
		return dockerResult{}
	}}
	store := NewBoxFileStore(fakeTestBox(fake))
	if _, err := store.Stat(context.Background(), "nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	code = 4
	if _, err := store.Stat(context.Background(), "link"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("outside scope: %v", err)
	}
}

// 拷出来的子树只解目录和普通文件：软链接、硬链接、设备文件丢掉，跑出目录的名字丢掉。
func TestExtractBoxTreeIsConfined(t *testing.T) {
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	add := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			_, _ = w.Write([]byte(body))
		}
	}
	mtime := time.Unix(1700000000, 0)
	add(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	add(&tar.Header{Name: "./sub/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	add(&tar.Header{Name: "./sub/ok.txt", Typeflag: tar.TypeReg, ModTime: mtime}, "fine")
	add(&tar.Header{Name: "./link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, "")
	add(&tar.Header{Name: "./hard", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}, "")
	add(&tar.Header{Name: "../escape.txt", Typeflag: tar.TypeReg}, "bad")
	add(&tar.Header{Name: "/abs.txt", Typeflag: tar.TypeReg}, "bad")
	_ = w.Close()
	parent := t.TempDir()
	dir := filepath.Join(parent, "tree")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := extractBoxTree(&archive, dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "sub", "ok.txt"))
	if err != nil || string(data) != "fine" {
		t.Fatalf("regular file: %q %v", data, err)
	}
	if info, _ := os.Stat(filepath.Join(dir, "sub", "ok.txt")); !info.ModTime().Equal(mtime) {
		t.Fatalf("mtime not kept: %v", info.ModTime())
	}
	for _, name := range []string{"link", "hard", "abs.txt"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			t.Fatalf("%s must not be extracted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "escape.txt")); err == nil {
		t.Fatal("../ entry escaped the tree")
	}
}

// TestBoxFileStoreRealDocker 在真容器里让四个只读工具经由范围目录读文件：内容、列目录、
// 检索、按名字找，指向 /etc 或别的范围的软链接都读不到。
// DIANA_BOX_DOCKER_TEST=1 scripts/test-serial.sh ./model/agent
func TestBoxFileStoreRealDocker(t *testing.T) {
	if os.Getenv("DIANA_BOX_DOCKER_TEST") != "1" {
		t.Skip("set DIANA_BOX_DOCKER_TEST=1 to run against a real Docker daemon")
	}
	docker, reason := probeBoxRuntime(BoxConfig{})
	if reason != "" {
		t.Fatal(reason)
	}
	host := newBoxContainer(testBoxConfig(), docker)
	host.container, host.volume = "diana-test-box", "diana-test-work"
	b := &WorkBox{host: host, key: "fs-a", scope: boxScopeName("fs-a"), uid: 10911}
	other := &WorkBox{host: host, key: "fs-b", scope: boxScopeName("fs-b"), uid: 10912}
	ctx := context.Background()
	t.Cleanup(func() {
		_ = docker(context.Background(), dockerCall{args: []string{"rm", "--force", host.container}, timeout: time.Minute})
		_ = docker(context.Background(), dockerCall{args: []string{"volume", "rm", "--force", host.volume}, timeout: time.Minute})
	})
	mustRun := func(w *WorkBox, command string) {
		t.Helper()
		if res, err := w.Exec(ctx, command, 0); err != nil || res.ExitCode != 0 {
			t.Fatalf("%s: %+v %v", command, res, err)
		}
	}
	mustRun(other, `echo other-secret > mine.txt`)
	mustRun(b, `mkdir -p notes/deep .git && printf 'alpha\nneedle here\nomega\n' > notes/a.txt && echo 'needle deep' > notes/deep/b.md && echo 'needle in git' > .git/c && printf '\000\001bin' > notes/blob.bin && ln -s /etc/passwd notes/etc && ln -s `+other.Root()+`/mine.txt notes/peek`)
	store := NewBoxFileStore(b)
	read, err := (&ReadFileTool{store: store}).Run(ctx, map[string]any{"path": "notes/a.txt", "offset": 2})
	if err != nil || !strings.Contains(read, "needle here") || strings.Contains(read, "alpha") {
		t.Fatalf("read_file: %q %v", read, err)
	}
	for _, link := range []string{"notes/etc", "notes/peek"} {
		if out, err := (&ReadFileTool{store: store}).Run(ctx, map[string]any{"path": link}); err == nil {
			t.Fatalf("read_file followed %s out of the scope: %q", link, out)
		}
	}
	if _, err := (&ReadFileTool{store: store}).Run(ctx, map[string]any{"path": "notes/blob.bin"}); err == nil || !strings.Contains(err.Error(), "二进制") {
		t.Fatalf("binary file: %v", err)
	}
	list, err := (&ListFilesTool{store: store}).Run(ctx, map[string]any{"path": "notes"})
	if err != nil || !strings.Contains(list, `"a.txt"`) || !strings.Contains(list, `"deep"`) {
		t.Fatalf("list_files: %q %v", list, err)
	}
	grep, err := (&GrepTool{store: store}).Run(ctx, map[string]any{"pattern": "needle"})
	if err != nil || !strings.Contains(grep, "notes/a.txt:2") || !strings.Contains(grep, "notes/deep/b.md:1") || strings.Contains(grep, "git") || strings.Contains(grep, "other-secret") {
		t.Fatalf("grep: %q %v", grep, err)
	}
	find, err := (&FindFilesTool{store: store}).Run(ctx, map[string]any{"pattern": "*.md"})
	if err != nil || !strings.Contains(find, "notes/deep/b.md") {
		t.Fatalf("find_files: %q %v", find, err)
	}
	if _, err := (&GrepTool{store: store}).Run(ctx, map[string]any{"pattern": "x", "path": "missing"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing base: %v", err)
	}
}
