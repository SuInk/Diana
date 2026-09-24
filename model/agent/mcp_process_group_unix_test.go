// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package agent

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

// stdio MCP 服务常见的是 npx、uvx 这种启动器，真正的服务是它的子进程。握手失败时
// SDK 只杀启动器，子进程握着 stderr 管道继续跑；现在整组收掉。
func TestMCPConnectFailureKillsServerProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	cfg := mcpServerConfig{
		Command: "sh",
		Args:    []string{"-c", "sleep 30 & echo $! > '" + pidFile + "'; exec sleep 30"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	session, err := connectMCPSession(ctx, "stuck", cfg, dir, time.Second)
	if err == nil {
		_ = session.close()
		t.Fatal("a server that never answers initialize should fail to connect")
	}
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("connect failure took %s", elapsed)
	}
	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("child pid not written: %v", readErr)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if convErr != nil || pid <= 0 {
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
	t.Fatalf("MCP server child %d survived the failed connect", pid)
}
