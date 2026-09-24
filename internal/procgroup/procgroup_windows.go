// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build windows

package procgroup

import (
	"os/exec"
	"strconv"
	"syscall"
)

// Windows 没有进程组信号。taskkill /T 按父子关系递归杀进程树，所以不需要改创建
// 标志；只要求父进程在杀的那一刻还活着，这正是 Cancel 被调用时的情形。
func setProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	taskkill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	taskkill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := taskkill.Run(); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
