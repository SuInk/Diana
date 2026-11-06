// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package browseruser

import (
	"io/fs"
	"os"
	"os/exec"
	"syscall"
)

// Apply 让 cmd 以账号身份启动，不带 root 的附加组。保留 cmd 已有的进程组设置。
func (a Account) Apply(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(a.UID), Gid: uint32(a.GID), Groups: []uint32{}}
}

func (a Account) allowed(info fs.FileInfo, need fs.FileMode) bool {
	perm := info.Mode().Perm()
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	switch {
	case int(stat.Uid) == a.UID:
		perm >>= 6
	case int(stat.Gid) == a.GID:
		perm >>= 3
	}
	return perm&need == need
}

func owned(path string, uid, gid int) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid && int(stat.Gid) == gid
}
