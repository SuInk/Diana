// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package assistant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// yt-dlp 的独立版是 PyInstaller 包：外面那层进程再起一个真正干活的 Python，下载时
// 又会起 ffmpeg。超时只杀外层时，里面那层握着输出管道接着跑，Output() 要等它跑完
// 才返回。假 yt-dlp 起一个睡 30 秒的子进程模拟这一层。
func TestYTDLPDumpTimeoutKillsWholeProcessTree(t *testing.T) {
	bin := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	script := "#!/bin/sh\nsleep 30 &\necho $! > '" + pidFile + "'\nwait\n"
	if err := os.WriteFile(filepath.Join(bin, "yt-dlp"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// 时限给宽一点：macOS 第一次执行新写出来的脚本要先过一遍系统扫描，几百毫秒内
	// 脚本可能还没来得及写 PID。要验证的是「到点以后很快返回、子进程不留」。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	if _, ok := ytdlpDumpInfo(ctx, "https://video.example/watch?v=1"); ok {
		t.Fatal("fake yt-dlp should not produce info")
	}
	if elapsed := time.Since(started); elapsed > 7*time.Second {
		t.Fatalf("ytdlpDumpInfo returned after %s; the inner process kept the pipe open", elapsed)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("child pid not written: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("bad child pid %q", raw)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("yt-dlp child %d survived the timeout", pid)
}
