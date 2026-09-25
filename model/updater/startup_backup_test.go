// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupDatabaseOnVersionChange(t *testing.T) {
	dataDir := t.TempDir()
	database := filepath.Join(dataDir, "diana.db")
	backupsRoot := filepath.Join(dataDir, ".diana-updates", "backups")
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	// Fresh deployment: nothing to back up, but the version is recorded.
	backup, err := BackupDatabaseOnVersionChange(database, "", "v0.9.0", now)
	if err != nil || backup != "" {
		t.Fatalf("fresh deployment: backup=%q err=%v", backup, err)
	}
	assertUpdaterTestContent(t, filepath.Join(dataDir, ".diana-updates", lastRunVersionFile), "v0.9.0\n")

	writeUpdaterTestFile(t, database, "v0.9.0-database", 0o600)
	writeUpdaterTestFile(t, database+"-wal", "v0.9.0-wal", 0o600)
	backup, err = BackupDatabaseOnVersionChange(database, "", "v0.9.0", now)
	if err != nil || backup != "" {
		t.Fatalf("same version: backup=%q err=%v", backup, err)
	}
	if _, err := os.Stat(backupsRoot); !os.IsNotExist(err) {
		t.Fatalf("same version created backups: %v", err)
	}

	backup, err = BackupDatabaseOnVersionChange(database, "", "v0.9.1", now)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(backupsRoot, "20260925T120000Z-v0.9.0", "database", "diana.db")
	if backup != want {
		t.Fatalf("backup = %q, want %q", backup, want)
	}
	assertUpdaterTestContent(t, want, "v0.9.0-database")
	assertUpdaterTestContent(t, want+"-wal", "v0.9.0-wal")
	assertUpdaterTestContent(t, database, "v0.9.0-database")
	assertUpdaterTestContent(t, filepath.Join(dataDir, ".diana-updates", lastRunVersionFile), "v0.9.1\n")

	// Later upgrades share the retention limit with the release updater.
	for i, next := range []string{"v0.9.2", "v0.9.3", "v0.9.4"} {
		if _, err := BackupDatabaseOnVersionChange(database, "", next, now.Add(time.Duration(i+1)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(backupsRoot)
	if err != nil || len(entries) != releaseBackupMaxCount {
		t.Fatalf("backups = %d, err = %v", len(entries), err)
	}
	if entries[0].Name() != "20260925T130000Z-v0.9.1" {
		t.Fatalf("oldest kept backup = %s", entries[0].Name())
	}
}

func TestBackupDatabaseOnVersionChangeWithoutMarker(t *testing.T) {
	// The first start with this feature has no marker yet; an existing database
	// still gets backed up, labelled with an unknown previous version.
	dataDir := t.TempDir()
	database := filepath.Join(dataDir, "diana.db")
	writeUpdaterTestFile(t, database, "old-database", 0o600)
	updatesDir := filepath.Join(t.TempDir(), "updates")
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	backup, err := BackupDatabaseOnVersionChange(database, updatesDir, "v0.9.0", now)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(updatesDir, "backups", "20260925T120000Z-unknown", "database", "diana.db")
	if backup != want {
		t.Fatalf("backup = %q, want %q", backup, want)
	}
	assertUpdaterTestContent(t, want, "old-database")
}

func TestBackupDatabaseOnVersionChangeKeepsMarkerOnFailure(t *testing.T) {
	dataDir := t.TempDir()
	database := filepath.Join(dataDir, "diana.db")
	writeUpdaterTestFile(t, database, "old-database", 0o600)
	writeUpdaterTestFile(t, filepath.Join(dataDir, ".diana-updates", lastRunVersionFile), "v0.9.0\n", 0o600)
	// A file where the backups directory should be makes the backup fail.
	writeUpdaterTestFile(t, filepath.Join(dataDir, ".diana-updates", "backups"), "not-a-directory", 0o600)
	if _, err := BackupDatabaseOnVersionChange(database, "", "v0.9.1", time.Now()); err == nil {
		t.Fatal("backup failure was not reported")
	}
	assertUpdaterTestContent(t, filepath.Join(dataDir, ".diana-updates", lastRunVersionFile), "v0.9.0\n")
	assertUpdaterTestContent(t, database, "old-database")
}
