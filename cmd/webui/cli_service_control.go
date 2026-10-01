// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// 测试里缩短等待时间，所以是变量。
var (
	serviceStopTimeout  = 30 * time.Second
	serviceStartTimeout = 30 * time.Second
)

// installedService 是一键安装注册的 Diana 服务。命令行要独占数据库时（如
// diana passwd）先停掉它，做完再启动。
type installedService struct {
	name  string
	stop  func() error
	start func() error
}

// findInstalledServiceFunc 找出运行中、由安装器管理的服务；找不到返回 nil。
// 测试替换它，免得真的去动系统服务。
var findInstalledServiceFunc = findInstalledService

// waitForInstanceLock 等服务退出、实例锁空出来后拿下它。
func waitForInstanceLock(dbPath string, timeout time.Duration) (*instanceLock, error) {
	deadline := time.Now().Add(timeout)
	for {
		lock, err := acquireInstanceLock(dbPath, "")
		if err == nil || time.Now().After(deadline) {
			return lock, err
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// waitForHealth 等服务重新起来、健康检查通过。
func waitForHealth(address string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		if _, err := fetchHealth(ctx, address); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("health check did not pass at %s within %s", address, timeout)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// instanceLockHolderPID 读出实例锁里记下的持有者进程号；读不到返回 0。
func instanceLockHolderPID(dbPath string) int {
	file, err := os.Open(dbPath + ".lock")
	if err != nil {
		return 0
	}
	defer file.Close()
	content, _ := io.ReadAll(io.LimitReader(file, 4<<10))
	fields := strings.Fields(string(content))
	if len(fields) == 0 {
		return 0
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// installerPIDMatchesLock 确认安装器记下的后台进程就是持有实例锁的那个，
// 免得按一个过期的进程号去结束别的程序。
func installerPIDMatchesLock(pidFile, dbPath string) (int, bool) {
	content, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, pid == instanceLockHolderPID(dbPath)
}
