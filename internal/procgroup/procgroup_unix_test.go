// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package procgroup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 父进程派生一个握着输出管道的孙进程后一直等它。只杀父进程时孙进程继续握着管道，
// CombinedOutput 要等它睡满 30 秒才返回；整组杀掉之后应当在超时附近就返回，孙进程
// 也不能留下。
func TestCommandContextKillsGrandchildrenOnTimeout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	cmd := CommandContext(ctx, "sh", "-c", "sleep 30 & echo $!; wait")
	start := time.Now()
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected the command to be killed, got success: %q", output)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("CombinedOutput returned after %s; the grandchild kept the pipe open", elapsed)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(output)))
	if convErr != nil || pid <= 0 {
		t.Fatalf("grandchild pid not printed: %q", output)
	}
	waitGone(t, pid)
}

// 没有 ctx 的长驻进程（stdio MCP 服务、常驻浏览器）由调用方显式 Kill。
func TestKillStopsWholeGroup(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	cmd := Isolate(exec.Command("sh", "-c", "sleep 30 & echo $!; wait"))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	n, _ := stdout.Read(buf)
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if convErr != nil || pid <= 0 {
		t.Fatalf("grandchild pid not printed: %q", buf[:n])
	}
	if err := Kill(cmd); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after Kill")
	}
	waitGone(t, pid)
	if err := Kill(cmd); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("second Kill should report the group is gone, got %v", err)
	}
}

func TestConfigureKeepsSetsid(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	Configure(cmd)
	if cmd.SysProcAttr.Setpgid {
		t.Fatal("Setpgid must not be combined with Setsid")
	}
	if cmd.WaitDelay != DefaultWaitDelay || cmd.Cancel == nil {
		t.Fatalf("Configure did not install Cancel/WaitDelay: %v", cmd.WaitDelay)
	}
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("grandchild %d survived the group kill", pid)
}
