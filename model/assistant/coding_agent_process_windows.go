// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build windows

package assistant

import (
	"os/exec"
	"strconv"
	"syscall"
)

// detachedProcAttr 在 Windows 上只新建进程组：没有 setsid 这种「完全脱离」的语义，
// 任务扛不住 Diana 重启，只能按中断收尾。CREATE_NEW_PROCESS_GROUP 至少保证 Ctrl+C
// 不会顺着控制台传进去，也让整组进程能被一次杀掉。
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func codingProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	const stillActive = 259
	return code == stillActive
}

// killCodingProcess 用 taskkill 连子进程一起杀：Windows 没有进程组信号，编码 CLI
// 派生出来的编译器和测试进程只能靠 /T 递归收掉。
func killCodingProcess(pid int) {
	if pid <= 0 {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
