// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package agent

import (
	"io/fs"
	"syscall"
)

// memberFileSingleHardlink 确认产物没有在沙盒外共享其他硬链接入口（防止硬链接系统只读文件冒充新产物）。
func memberFileSingleHardlink(info fs.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return true
	}
	return stat.Nlink <= 1
}
