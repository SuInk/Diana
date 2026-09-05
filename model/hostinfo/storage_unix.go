// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package hostinfo

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// StorageUsage 返回该路径所在文件系统的容量、已用和可用字节数。
func StorageUsage(path string) (total, used, available uint64, err error) {
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return 0, 0, 0, fmt.Errorf("read storage usage for %s: %w", path, err)
	}
	blockSize := uint64(stats.Bsize)
	total = uint64(stats.Blocks) * blockSize
	free := uint64(stats.Bfree) * blockSize
	available = uint64(stats.Bavail) * blockSize
	if free > total {
		free = total
	}
	return total, total - free, available, nil
}
