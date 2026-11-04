// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// databaseSnapshotTimeout 给 VACUUM INTO 封顶。几 GB 的库正常一两分钟内写完；
// 卡住时宁可退回逐文件复制，也不要让升级一直挂着。
const databaseSnapshotTimeout = 10 * time.Minute

// snapshotReleaseDatabase 用 VACUUM INTO 把数据库（连同 WAL 里还没合并的页）
// 写成一个单文件快照。
//
// 逐文件复制会把 WAL 原样带上：有外部只读进程长期占着快照时 checkpoint 做不完，
// WAL 能涨到主库的两倍多，每份升级备份跟着翻倍（生产上 3.5 GB 的库一份备份
// 8.6 GB，三份 24 GB）。VACUUM INTO 只写当前可见的数据，备份大小约等于主库，
// 而且只读源库，不动 WAL、也不需要拿写锁。
func snapshotReleaseDatabase(databasePath, target string) error {
	uriPath := filepath.ToSlash(databasePath)
	if filepath.VolumeName(databasePath) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	temporary, err := os.CreateTemp(filepath.Dir(target), ".diana-snapshot-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	_ = temporary.Close()
	// VACUUM INTO 要求目标文件不存在。
	if err := os.Remove(temporaryPath); err != nil {
		return err
	}
	defer os.Remove(temporaryPath)

	ctx, cancel := context.WithTimeout(context.Background(), databaseSnapshotTimeout)
	defer cancel()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, temporaryPath); err != nil {
		return fmt.Errorf("vacuum into snapshot: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return err
	}
	return os.Rename(temporaryPath, target)
}
