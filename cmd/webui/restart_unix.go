//go:build !windows

package main

import (
	"os"
	"syscall"
)

// relaunchSelf 原地重启：exec 替换进程映像，pid 不变，systemd/launchd/Docker
// 都视为同一个服务；磁盘上的二进制被更新过时，重启后即运行新版本。
// cleanup 在 exec 前执行（exec 成功后 defer 不会再运行）。
func relaunchSelf(cleanup func()) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cleanup()
	return syscall.Exec(exe, os.Args, os.Environ())
}
