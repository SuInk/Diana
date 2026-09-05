// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build windows

package hostinfo

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// StorageUsage 返回该路径所在文件系统的容量、已用和可用字节数。
func StorageUsage(path string) (total, used, available uint64, err error) {
	directory, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("encode storage path: %w", err)
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(directory, &available, &total, &free); err != nil {
		return 0, 0, 0, fmt.Errorf("read storage usage for %s: %w", path, err)
	}
	if free > total {
		free = total
	}
	return total, total - free, available, nil
}
