// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package procgroup

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if !cmd.SysProcAttr.Setsid {
		cmd.SysProcAttr.Setpgid = true
		cmd.SysProcAttr.Pgid = 0
	}
}

func killProcessGroup(cmd *exec.Cmd) error {
	pid := cmd.Process.Pid
	if pid <= 0 {
		return os.ErrProcessDone
	}
	// 组号就是子进程自己的 PID（Setpgid 且 Pgid=0，或 Setsid）。负 PID 把信号发给整组。
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	// 组发不出去（EPERM 等）时至少把直接子进程杀掉，不能比 exec 的默认行为更差。
	return cmd.Process.Kill()
}
