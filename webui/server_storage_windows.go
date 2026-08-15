//go:build windows

package webui

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func storageUsage(path string) (total, used, available uint64, err error) {
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
