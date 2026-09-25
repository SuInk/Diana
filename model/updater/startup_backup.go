// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// lastRunVersionFile 记录上一次启动的版本，放在更新工作目录里。
const lastRunVersionFile = "last-run-version"

// BackupDatabaseOnVersionChange 在版本变化后、数据库迁移前备份 SQLite。
//
// Release 自更新和一键安装器替换程序前自己会备份；Docker 拉新镜像、源码重新构建
// 没有这一步，新版本一启动就直接迁移数据库。调用方只在这两种部署里调用它。
//
// 备份和自更新共用 <更新目录>/backups 与同一套保留规则（3 天内最多 3 份），
// 目录名是「UTC 时间-旧版本」；标记文件缺失（首次启用这个功能）时旧版本记作
// unknown。数据库还不存在（全新部署）时只写标记不备份。调用时数据库必须尚未被
// 本进程打开，复制出来的主文件和 WAL/SHM 才是一致的快照。返回备份出的主文件路径，
// 没有备份时返回空字符串；备份失败时标记不更新，下次启动还会再试。
func BackupDatabaseOnVersionChange(databasePath, updatesDir, currentVersion string, now time.Time) (string, error) {
	databasePath = strings.TrimSpace(databasePath)
	currentVersion = strings.TrimSpace(currentVersion)
	if databasePath == "" || currentVersion == "" {
		return "", nil
	}
	updatesRoot, err := resolveUpdatesRoot(updatesDir, databasePath, filepath.Dir(databasePath))
	if err != nil {
		return "", err
	}
	marker := filepath.Join(updatesRoot, lastRunVersionFile)
	previous := ""
	if data, err := os.ReadFile(marker); err == nil {
		previous = strings.TrimSpace(string(data))
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read last run version: %w", err)
	}
	if previous == currentVersion {
		return "", nil
	}
	backup := ""
	if regularFileExists(databasePath) {
		backupsRoot := filepath.Join(updatesRoot, "backups")
		if err := pruneReleaseBackups(backupsRoot, releaseBackupMaxCount-1, now); err != nil {
			return "", fmt.Errorf("remove previous update backups: %w", err)
		}
		backupRoot := filepath.Join(backupsRoot, now.UTC().Format(releaseBackupTimeLayout)+"-"+safePathComponent(previous))
		if err := os.MkdirAll(backupRoot, 0o700); err != nil {
			return "", err
		}
		if backup, err = backupReleaseDatabase(databasePath, backupRoot); err != nil {
			_ = os.RemoveAll(backupRoot)
			return "", err
		}
	}
	if err := os.MkdirAll(updatesRoot, 0o700); err != nil {
		return backup, err
	}
	if err := os.WriteFile(marker, []byte(currentVersion+"\n"), 0o600); err != nil {
		return backup, fmt.Errorf("record run version: %w", err)
	}
	return backup, nil
}
