// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// checkDatabaseOwner 拦住以 root 改别人数据库的情况：SQLite 会以 root 新建
// -wal/-shm 等文件，服务用户之后写不进去，下次启动就起不来。docker exec 默认是
// root，最容易踩到。
func checkDatabaseOwner(dbPath string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid == 0 {
		return nil
	}
	owner := strconv.FormatUint(uint64(stat.Uid), 10)
	if account, err := user.LookupId(owner); err == nil {
		owner = account.Username
	}
	hint := fmt.Sprintf(" (for example `sudo -u %s diana passwd`)", owner)
	if dockerDeployment() {
		hint = " (Docker: on the host run `docker compose run --rm diana passwd`, which switches to that user by itself)"
	}
	return fmt.Errorf("refusing to run as root: %s belongs to %s; run `diana passwd` as that user so the service can still write the database%s", dbPath, owner, hint)
}
