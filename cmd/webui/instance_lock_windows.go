// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Windows 的字节范围锁会挡住别的进程读被锁的区间，所以锁文件内容之外的一个字节，
// 被挡住的实例仍能读出持有者的 pid 和地址。
const instanceLockOffset = 1 << 30

func lockInstanceFile(file *os.File) error {
	overlapped := windows.Overlapped{Offset: instanceLockOffset}
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errInstanceLocked
	}
	return err
}

func unlockInstanceFile(file *os.File) error {
	overlapped := windows.Overlapped{Offset: instanceLockOffset}
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
