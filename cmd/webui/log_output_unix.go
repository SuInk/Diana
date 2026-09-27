// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// stdoutIsJournal 报告标准输出是否直连 systemd journal。systemd 把连到 journal 的
// 那个流写进 JOURNAL_STREAM（"设备号:inode"），只看变量在不在不够：从 systemd 会话
// 里开的终端也会继承它，再被重定向到文件时就误判了，所以要和标准输出本身比对。
func stdoutIsJournal() bool {
	device, inode, ok := parseJournalStream(os.Getenv("JOURNAL_STREAM"))
	if !ok {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return uint64(stat.Dev) == device && uint64(stat.Ino) == inode
}

func parseJournalStream(value string) (device, inode uint64, ok bool) {
	deviceText, inodeText, found := strings.Cut(strings.TrimSpace(value), ":")
	if !found {
		return 0, 0, false
	}
	device, deviceErr := strconv.ParseUint(deviceText, 10, 64)
	inode, inodeErr := strconv.ParseUint(inodeText, 10, 64)
	return device, inode, deviceErr == nil && inodeErr == nil
}
