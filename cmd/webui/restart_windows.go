//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// relaunchSelf Windows 没有 exec 语义：监听端口已释放后启动新进程，
// 当前进程随后正常退出（cleanup 由 main 的 defer 执行）。
func relaunchSelf(func()) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
