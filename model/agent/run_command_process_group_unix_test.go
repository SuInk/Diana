// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// run_command 超时只杀父进程时，白名单命令派生出来的子进程（go test 的测试二进制、
// npm 起的 node、make 起的编译器）继续在后台跑。超时后整组都要没了。
func TestRunCommandTimeoutKillsGrandchildren(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	for _, mode := range []string{CommandSandboxOff, CommandSandboxAuto} {
		t.Run(mode, func(t *testing.T) {
			sandbox := detectCommandSandbox()
			// bubblewrap 自带 --unshare-pid 和 --die-with-parent，写出来的是命名空间里的
			// PID，在宿主机上查不了；这里只覆盖原地 exec 的 sandbox-exec。
			if mode == CommandSandboxAuto && (!sandbox.available() || sandbox.kind != "sandbox-exec") {
				t.Skip("sandbox-exec not available on this host")
			}
			root := t.TempDir()
			tool := &RunCommandTool{
				root:        root,
				allowlist:   map[string]bool{"sh": true},
				timeout:     500 * time.Millisecond,
				maxBytes:    4096,
				sandboxMode: mode,
				sandbox:     sandbox,
			}
			start := time.Now()
			_, err := tool.Run(context.Background(), map[string]any{
				"command": "sh",
				"args":    []any{"-c", "sleep 30 & echo $! > child.pid; wait"},
			})
			if err == nil || !strings.Contains(err.Error(), `"timed_out": true`) {
				t.Fatalf("expected a timed out result, got %v", err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("run_command returned after %s", elapsed)
			}
			raw, readErr := os.ReadFile(filepath.Join(root, "child.pid"))
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
			t.Fatalf("grandchild %d survived run_command timeout", pid)
		})
	}
}
