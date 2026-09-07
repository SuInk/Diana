// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupHistoryMediaAppliesAgeAndCapacityWithoutTouchingDownloadCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", dir)
	previous := CurrentHistoryMediaRetentionPolicy()
	t.Cleanup(func() { _ = ConfigureHistoryMediaRetention(previous) })
	old := filepath.Join(dir, "group_1", "old.jpg")
	newer := filepath.Join(dir, "objects", "aa", "new.jpg")
	download := filepath.Join(dir, "download-cache", "objects", "cached.bin")
	for path, body := range map[string][]byte{old: make([]byte, 8), newer: make([]byte, 12), download: make([]byte, 20)} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureHistoryMediaRetention(HistoryMediaRetentionPolicy{RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}
	result, err := CleanupHistoryMedia()
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedFiles != 1 || result.DeletedBytes != 8 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old media still exists: %v", err)
	}
	if _, err := os.Stat(newer); err != nil {
		t.Fatalf("new media removed: %v", err)
	}
	if _, err := os.Stat(download); err != nil {
		t.Fatalf("download cache removed: %v", err)
	}
}

func TestCleanupHistoryMediaAppliesCapacityOldestFirst(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", dir)
	previous := CurrentHistoryMediaRetentionPolicy()
	t.Cleanup(func() { _ = ConfigureHistoryMediaRetention(previous) })
	old := filepath.Join(dir, "group_1", "old.mp4")
	newer := filepath.Join(dir, "group_1", "new.mp4")
	for _, path := range []string{old, newer} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 700<<10), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureHistoryMediaRetention(HistoryMediaRetentionPolicy{RetentionDays: -1, MaxMB: 1}); err != nil {
		t.Fatal(err)
	}
	result, err := CleanupHistoryMedia()
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedFiles != 1 || result.RemainingBytes > 1<<20 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old media still exists: %v", err)
	}
	if _, err := os.Stat(newer); err != nil {
		t.Fatalf("new media removed: %v", err)
	}
}
