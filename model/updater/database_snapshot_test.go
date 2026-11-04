// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 有外部读事务占着快照、WAL 里堆着没合并的页时，备份应该是一个包含全部数据的
// 单文件，而不是把 WAL 原样带上。
func TestBackupReleaseDatabaseSnapshotsPendingWAL(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "with space", "diana.db")
	if err := os.MkdirAll(filepath.Dir(database), 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA wal_autocheckpoint=0`,
		`CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT)`,
	} {
		if _, err := writer.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	// 外部只读进程开着读事务，之后的写入都只能留在 WAL 里。
	reader, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readTx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()
	var before int
	if err := readTx.QueryRow(`SELECT count(*) FROM notes`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	body := strings.Repeat("x", 2000)
	for i := 0; i < 200; i++ {
		if _, err := writer.Exec(`INSERT INTO notes(body) VALUES (?)`, body); err != nil {
			t.Fatal(err)
		}
	}
	// 反复改同一批行，WAL 里会留下多份旧页，体积远超当前数据。
	for i := 0; i < 5; i++ {
		if _, err := writer.Exec(`UPDATE notes SET body = body || ?`, string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	walInfo, err := os.Stat(database + "-wal")
	if err != nil {
		t.Fatal(err)
	}

	backupRoot := filepath.Join(dir, "backup")
	backup, err := backupReleaseDatabase(database, backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(backupRoot, "database", "diana.db"); backup != want {
		t.Fatalf("backup = %q, want %q", backup, want)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(backup + suffix); !os.IsNotExist(err) {
			t.Fatalf("snapshot left %s next to the backup: %v", suffix, err)
		}
	}
	backupInfo, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, want 0600", backupInfo.Mode().Perm())
	}
	if backupInfo.Size() >= walInfo.Size() {
		t.Fatalf("backup %d bytes is not smaller than the pending WAL %d bytes", backupInfo.Size(), walInfo.Size())
	}

	copyDB, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var rows int
	var suffix string
	if err := copyDB.QueryRow(`SELECT count(*), substr(max(body), -5) FROM notes`).Scan(&rows, &suffix); err != nil {
		t.Fatal(err)
	}
	if rows != 200 || suffix != "abcde" {
		t.Fatalf("backup has %d rows ending %q, want 200 rows ending abcde", rows, suffix)
	}

	// 回滚用这份单文件备份恢复，旧的 WAL/SHM 必须一起删掉。
	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	_ = writer.Close()
	if err := restoreReleaseDatabase(database, backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(database + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("restore kept the old WAL: %v", err)
	}
	restored, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.QueryRow(`SELECT count(*) FROM notes`).Scan(&rows); err != nil || rows != 200 {
		t.Fatalf("restored rows = %d, err = %v", rows, err)
	}
}

// 不是 SQLite 文件（或快照因别的原因失败）时退回逐文件复制。
func TestBackupReleaseDatabaseFallsBackToFileCopy(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "diana.db")
	writeUpdaterTestFile(t, database, "not-sqlite", 0o600)
	writeUpdaterTestFile(t, database+"-wal", "raw-wal", 0o600)
	backup, err := backupReleaseDatabase(database, filepath.Join(dir, "backup"))
	if err != nil {
		t.Fatal(err)
	}
	assertUpdaterTestContent(t, backup, "not-sqlite")
	assertUpdaterTestContent(t, backup+"-wal", "raw-wal")
}

func TestRunUpdateBackupPrunerRemovesExpiredBackupsAtStartup(t *testing.T) {
	dataDir := t.TempDir()
	database := filepath.Join(dataDir, "diana.db")
	backupsRoot := filepath.Join(dataDir, ".diana-updates", "backups")
	now := time.Now().UTC()
	expired := now.Add(-releaseBackupRetention-time.Hour).Format(releaseBackupTimeLayout) + "-v0.9.4"
	recent := now.Add(-time.Hour).Format(releaseBackupTimeLayout) + "-v0.9.5"
	for _, name := range []string{expired, recent} {
		writeUpdaterTestFile(t, filepath.Join(backupsRoot, name, "database", "diana.db"), name, 0o600)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunUpdateBackupPruner(ctx, database, "")
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(backupsRoot, expired)); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired backup was not removed at startup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pruner did not stop after cancel")
	}
	assertUpdaterTestContent(t, filepath.Join(backupsRoot, recent, "database", "diana.db"), recent)
}
