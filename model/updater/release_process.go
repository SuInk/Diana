// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"errors"
	"fmt"
	"log"
	"os"
	"time"
)

var errProcessExitTimeout = errors.New("process exit timeout")

// 更新辅助进程独立于旧服务运行；即使旧服务的退出路径死锁，也能完成交接。
// 只在明确超时时强杀，权限或进程查询错误不能当成超时处理。
func waitForUpdateParentExit(pid int, timeout time.Duration) error {
	return waitForUpdateParentExitWith(pid, timeout, waitForProcessExit, killUpdateParent)
}

func waitForUpdateParentExitWith(pid int, timeout time.Duration, wait func(int, time.Duration) error, kill func(int) error) error {
	if pid <= 1 || pid == os.Getpid() {
		return fmt.Errorf("invalid update parent pid %d", pid)
	}
	err := wait(pid, timeout)
	if !errors.Is(err, errProcessExitTimeout) {
		return err
	}
	log.Printf("updater: old Diana process %d did not stop within %s; forcing termination", pid, timeout)
	if err := kill(pid); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("force stop old Diana process %d: %w", pid, err)
	}
	// 发出 kill 不代表进程已退出；确认释放文件和数据库后才能安装。
	if err := wait(pid, 5*time.Second); err != nil {
		return fmt.Errorf("wait after force stopping old Diana process %d: %w", pid, err)
	}
	return nil
}

func killUpdateParent(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer process.Release()
	return process.Kill()
}
