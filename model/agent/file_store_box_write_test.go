// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// 写入工具设了 store 就只经由它写：root 上什么都不该出现。
func TestWriteToolsUseFileStore(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	store := newHostFileStore(other, protectedFiles{})
	ctx := context.Background()
	keep := keepScope{botDir: "bot-a", actor: "u1"}
	if _, err := (&WriteFileTool{root: root, store: store, keep: keep}).Run(ctx, map[string]any{"path": "notes/a.txt", "content": "hello world"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&EditFileTool{root: root, store: store, keep: keep}).Run(ctx, map[string]any{"path": "notes/a.txt", "edits": []any{map[string]any{"old_text": "world", "new_text": "store"}}}); err != nil {
		t.Fatal(err)
	}
	manage := &ManageFilesTool{root: root, store: store, keep: keep, writeEnabled: true}
	if _, err := manage.Run(ctx, map[string]any{"action": "copy", "path": "notes/a.txt", "to": "keep/a.txt", "description": "问候"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manage.Run(ctx, map[string]any{"action": "delete", "path": "notes"}); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	w, _ := zw.Create("x/y.txt")
	_, _ = w.Write([]byte("zipped"))
	_ = zw.Close()
	if err := store.WriteFile(ctx, "in.zip", archive.Bytes(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ExtractArchiveTool{root: root, store: store}).Run(ctx, map[string]any{"archive_path": "in.zip", "destination": "out"}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("root was written: %v", entries)
	}
	if data, _ := os.ReadFile(other + "/keep/bot-a/a.txt"); string(data) != "hello store" {
		t.Fatalf("keep copy = %q", data)
	}
	if data, _ := os.ReadFile(other + "/out/x/y.txt"); string(data) != "zipped" {
		t.Fatalf("extracted = %q", data)
	}
	entries, err := loadKeepIndexDir(store.keepStateDir(), "bot-a")
	if err != nil || len(entries) != 1 || entries[0].Description != "问候" {
		t.Fatalf("keep index = %+v %v", entries, err)
	}
}

func TestBoxFileStoreWriteRealDocker(t *testing.T) {
	if os.Getenv("DIANA_BOX_DOCKER_TEST") != "1" {
		t.Skip("set DIANA_BOX_DOCKER_TEST=1 to run against a real Docker daemon")
	}
	docker, reason := probeBoxRuntime(BoxConfig{})
	if reason != "" {
		t.Fatal(reason)
	}
	SetBoxStateDir(t.TempDir())
	t.Cleanup(func() { SetBoxStateDir("") })
	host := newBoxContainer(testBoxConfig(), docker)
	host.container, host.volume = "diana-test-box", "diana-test-work"
	b := &WorkBox{host: host, key: "fsw-a", scope: boxScopeName("fsw-a"), uid: 10921}
	other := &WorkBox{host: host, key: "fsw-b", scope: boxScopeName("fsw-b"), uid: 10922}
	ctx := context.Background()
	t.Cleanup(func() {
		_ = docker(context.Background(), dockerCall{args: []string{"rm", "--force", host.container}, timeout: time.Minute})
		_ = docker(context.Background(), dockerCall{args: []string{"volume", "rm", "--force", host.volume}, timeout: time.Minute})
	})
	run := func(w *WorkBox, command string) string {
		t.Helper()
		res, err := w.Exec(ctx, command, 0)
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("%s: %+v %v", command, res, err)
		}
		tail, _, _, _, err := w.OutputTail(ctx, res.OutputPath, 1<<16)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(tail))
	}
	run(other, `echo other-secret > mine.txt`)
	run(b, `ln -s /tmp esc && ln -s `+other.Root()+` peer && ln -s /etc/passwd pw`)
	store := NewBoxFileStore(b)
	keep := keepScope{botDir: "bot-a", actor: "u1"}
	write := &WriteFileTool{store: store, keep: keep}
	edit := &EditFileTool{store: store, keep: keep}
	manage := &ManageFilesTool{store: store, keep: keep, writeEnabled: true}

	// write_file / edit_file：落在范围目录里、属于范围 uid，覆盖时权限位不变。
	if _, err := write.Run(ctx, map[string]any{"path": "notes/x.txt", "content": "hello world"}); err != nil {
		t.Fatal(err)
	}
	run(b, `chmod 600 notes/x.txt`)
	if _, err := edit.Run(ctx, map[string]any{"path": "notes/x.txt", "edits": []any{map[string]any{"old_text": "world", "new_text": "box"}}}); err != nil {
		t.Fatal(err)
	}
	if got := run(b, `cat notes/x.txt; echo; stat -c '%u %a' notes/x.txt`); got != "hello box\n10921 600" {
		t.Fatalf("after edit: %q", got)
	}
	// 经软链接写到范围外（容器 /tmp、别的范围）一律拒绝，外面不能多出文件。
	for _, target := range []string{"esc/pwn.txt", "peer/pwn.txt"} {
		if out, err := write.Run(ctx, map[string]any{"path": target, "content": "x"}); err == nil {
			t.Fatalf("write_file escaped via %s: %s", target, out)
		}
	}
	if out, err := manage.Run(ctx, map[string]any{"action": "copy", "path": "notes/x.txt", "to": "esc/pwn2.txt"}); err == nil {
		t.Fatalf("copy escaped: %s", out)
	}
	run(b, `test ! -e /tmp/pwn.txt && test ! -e /tmp/pwn2.txt`)
	run(other, `test ! -e pwn.txt`)

	// 长期区：keep/ 落到本机器人目录，索引记在这个范围自己的状态目录里。
	if _, err := write.Run(ctx, map[string]any{"path": "keep/k.txt", "content": "keep me"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manage.Run(ctx, map[string]any{"action": "copy", "path": "notes/x.txt", "to": "keep/", "description": "问候语"}); err != nil {
		t.Fatal(err)
	}
	entries, err := loadKeepIndexDir(store.keepStateDir(), "bot-a")
	if err != nil || len(entries) != 2 {
		t.Fatalf("keep index = %+v %v", entries, err)
	}
	if usage := store.Usage(ctx, "keep/bot-a"); usage != int64(len("keep me")+len("hello box")) {
		t.Fatalf("keep usage = %d", usage)
	}

	// manage_files：mkdir、copy（不覆盖）、move、stat、delete 进回收站。
	if _, err := manage.Run(ctx, map[string]any{"action": "mkdir", "path": "arch/sub"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manage.Run(ctx, map[string]any{"action": "copy", "path": "notes/x.txt", "to": "arch"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manage.Run(ctx, map[string]any{"action": "copy", "path": "notes/x.txt", "to": "arch"}); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("copy without overwrite: %v", err)
	}
	if _, err := manage.Run(ctx, map[string]any{"action": "move", "path": "arch/x.txt", "to": "arch/sub/y.txt"}); err != nil {
		t.Fatal(err)
	}
	stat, err := manage.Run(ctx, map[string]any{"action": "stat", "path": "arch/sub/y.txt"})
	var statResult map[string]any
	_ = json.Unmarshal([]byte(stat), &statResult)
	if err != nil || statResult["size"] != float64(len("hello box")) || !strings.HasPrefix(statResult["mime"].(string), "text/plain") {
		t.Fatalf("stat: %s %v", stat, err)
	}
	if _, err := manage.Run(ctx, map[string]any{"action": "stat", "path": "notes/x.md"}); err == nil || !strings.Contains(err.Error(), "notes/x.txt") {
		t.Fatalf("missing file hint: %v", err)
	}
	deleted, err := manage.Run(ctx, map[string]any{"action": "delete", "path": "arch"})
	if err != nil || !strings.Contains(deleted, WorkspaceTrashDir+"/") {
		t.Fatalf("delete: %s %v", deleted, err)
	}
	// 删软链接挪的是链接本身，指向的 /etc/passwd 不受影响。
	if _, err := manage.Run(ctx, map[string]any{"action": "delete", "path": "pw"}); err != nil {
		t.Fatal(err)
	}
	run(b, `test ! -e arch && test ! -L pw && test -s /etc/passwd && ls .trash/*/arch/sub/y.txt && test -L .trash/*/pw`)

	// extract_archive：解进新目录；目标不空时拒绝。
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for name, body := range map[string]string{"pkg/a.txt": "alpha", "pkg/deep/b.txt": "beta"} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	if err := store.WriteFile(ctx, "in.zip", archive.Bytes(), true); err != nil {
		t.Fatal(err)
	}
	extract := &ExtractArchiveTool{store: store}
	if _, err := extract.Run(ctx, map[string]any{"archive_path": "in.zip", "destination": "out"}); err != nil {
		t.Fatal(err)
	}
	if got := run(b, `cat out/pkg/a.txt out/pkg/deep/b.txt; stat -c %u out/pkg/deep/b.txt`); got != "alphabeta10921" {
		t.Fatalf("extracted: %q", got)
	}
	if _, err := extract.Run(ctx, map[string]any{"archive_path": "in.zip", "destination": "out"}); err == nil {
		t.Fatal("extract into a non-empty directory succeeded")
	}
	if _, err := extract.Run(ctx, map[string]any{"archive_path": "in.zip", "destination": "esc/out"}); err == nil {
		t.Fatal("extract escaped via a symlink")
	}

	// 当初发不出 GIF 的那条命令原样重跑：存进工作区的视频，命令里写 /work/... 也能读到，产物落回同一处。
	run(b, `mkdir -p downloads && ffmpeg -loglevel error -f lavfi -i testsrc=duration=1:size=64x64:rate=10 -pix_fmt yuv420p downloads/laowu_video.mp4`)
	out, err := RunBoxCommand(ctx, b, `ffmpeg -y -i /work/downloads/laowu_video.mp4 -vf "fps=15,scale=480:-1:flags=lanczos" -loop 0 /work/downloads/laowu.gif && ls -lh /work/downloads/laowu.gif`, 0)
	if err != nil || !strings.Contains(out, `"exit_code": 0`) {
		t.Fatalf("legacy /work command: %s %v", out, err)
	}
	if info, err := store.Stat(ctx, "downloads/laowu.gif"); err != nil || info.Size() == 0 {
		t.Fatalf("gif not in the scope workspace: %v %v", info, err)
	}

	// 范围目录超限：这次写入整个拒绝，给出超限说明。
	b.SetDiskLimit(store.Usage(ctx, ".") + 64<<10)
	_, err = write.Run(ctx, map[string]any{"path": "big.txt", "content": strings.Repeat("x", 256<<10)})
	b.SetDiskLimit(0)
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("over the disk limit: %v", err)
	}
	run(b, `test ! -e big.txt`)
}
