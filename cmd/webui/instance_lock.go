// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// errInstanceLocked 表示锁已被另一个进程持有；其它加锁失败（文件系统不支持等）不算。
var errInstanceLocked = errors.New("instance lock is held by another process")

// instanceLock 用数据库旁的锁文件保证同一份数据只有一个 Diana 在跑。
//
// 只靠端口挡不住重复启动：两份配置端口不同，或者启动后改过端口，第二个实例
// 照样能监听成功，两个进程就同时写同一个 SQLite。锁跟着数据走，和端口无关。
// 锁由操作系统随进程释放，崩溃后不会留下需要手工清理的死锁；文件本身不删，
// 删掉再建会让两个进程各锁一个 inode。
type instanceLock struct {
	file *os.File
}

func acquireInstanceLock(dbPath, address string) (*instanceLock, error) {
	path := dbPath + ".lock"
	// 加锁在打开数据库之前，首次启动时数据目录还不存在。
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	writable := err == nil
	if errors.Is(err, os.ErrPermission) {
		// 容器里用 root 手动跑过一次时锁文件归 root，服务用户只读打开也能加锁，只是写不了持有者。
		file, err = os.Open(path)
	}
	if err != nil {
		return nil, fmt.Errorf("open instance lock %s: %w", path, err)
	}
	if err := lockInstanceFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, errInstanceLocked) {
			return nil, runningInstanceError(path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	// 写下持有者，让被挡住的实例能说出对方实际在哪个地址。
	if !writable {
		return &instanceLock{file: file}, nil
	}
	if err := file.Truncate(0); err == nil {
		_, _ = file.WriteAt([]byte(fmt.Sprintf("%d\n%s\n", os.Getpid(), address)), 0)
	}
	return &instanceLock{file: file}, nil
}

func runningInstanceError(path string) error {
	pid, address := "", ""
	if file, err := os.Open(path); err == nil {
		content, _ := io.ReadAll(io.LimitReader(file, 4<<10))
		_ = file.Close()
		fields := strings.Fields(string(content))
		if len(fields) >= 2 {
			pid, address = fields[0], fields[1]
		}
	}
	if pid == "" {
		return fmt.Errorf("another Diana is already running with the data at %s; use `diana status` or `diana restart` to manage it", strings.TrimSuffix(path, ".lock"))
	}
	return fmt.Errorf("Diana is already running at %s (pid %s) with the same data; use `diana status`, `diana restart` or `diana logs` to manage it", address, pid)
}

// Release 在原地重启前调用：Windows 上新进程会在旧进程退出前启动，锁必须先放。
func (l *instanceLock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = unlockInstanceFile(l.file)
	_ = l.file.Close()
	l.file = nil
}
