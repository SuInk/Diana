// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDashboardStoragePathUsesDatabaseFilesystem(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "nested", "diana.db")
	got := dashboardStoragePath(databasePath)
	want := filepath.Dir(filepath.Dir(databasePath))
	if got != want {
		t.Fatalf("dashboardStoragePath() = %q, want nearest existing directory %q", got, want)
	}
}

func TestCollectDashboardServerStatsIncludesHostStorage(t *testing.T) {
	stats := collectDashboardServerStats(time.Now(), filepath.Join(t.TempDir(), "diana.db"))
	if stats.OS == "" || stats.Arch == "" || stats.CPUCores < 1 {
		t.Fatalf("host identity is incomplete: %+v", stats)
	}
	if stats.StorageTotalBytes == 0 {
		t.Fatalf("storage total is missing: %+v", stats)
	}
	if stats.StorageUsedBytes > stats.StorageTotalBytes {
		t.Fatalf("storage used %d exceeds total %d", stats.StorageUsedBytes, stats.StorageTotalBytes)
	}
	if stats.StorageAvailableBytes > stats.StorageTotalBytes {
		t.Fatalf("storage available %d exceeds total %d", stats.StorageAvailableBytes, stats.StorageTotalBytes)
	}
}
