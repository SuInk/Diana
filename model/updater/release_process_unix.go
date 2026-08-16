// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package updater

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

func configureDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func waitForProcessExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not exit within %s", pid, timeout)
}
