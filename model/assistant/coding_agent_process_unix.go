// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package assistant

import (
	"syscall"
	"time"
)

// detachedProcAttr 让编码 CLI 自己起一个会话和进程组。脱离 Diana 的进程组是「任务
// 扛得住重启」的前提：不脱离的话 Diana 一退出，整组进程跟着收到信号就全没了。
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func codingProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// 信号 0 只做权限和存在性检查，不真的送信号。
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return err == syscall.EPERM
}

// killCodingProcess 杀掉整个进程组。编码 CLI 会派生编译器、测试进程和自己的子代理，
// 只杀父进程会留下一地孤儿，它们还握着工作区里的文件锁。
func killCodingProcess(pid int) {
	if pid <= 0 {
		return
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		pgid = pid
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	// 给一点收尾时间：编码 CLI 收到 TERM 会把当前写到一半的文件落盘。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !codingProcessAlive(pid) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
