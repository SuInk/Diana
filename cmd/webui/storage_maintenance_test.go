// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/storage"
)

func TestLogRetentionConfig(t *testing.T) {
	for _, days := range []int{-2, -1, 0, 7, 36500, 36501} {
		for _, key := range []string{"debug_log_retention_days", "log_retention_days"} {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(fmt.Sprintf("storage:\n  %s: %d\n", key, days)), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadAppConfig(path)
			invalid := days < -1 || days > 36500
			if (err != nil) != invalid {
				t.Errorf("%s=%d: %v", key, days, err)
			}
		}
	}
}

func TestLogRetentionCutoff(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ days, fallback, want int }{{0, 7, 7}, {0, 30, 30}, {2, 7, 2}} {
		if got := logRetentionCutoff(now, tc.days, tc.fallback); !got.Equal(now.AddDate(0, 0, -tc.want)) {
			t.Errorf("cutoff=%v for %+v", got, tc)
		}
	}
	if !logRetentionCutoff(now, -1, 7).IsZero() {
		t.Fatal("negative retention should disable cleanup")
	}
}

func TestStorageMaintenanceShutdown(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	s, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stop := startStorageMaintenance(context.Background(), s, storageConfig{})
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance did not stop")
	}
}

func TestStorageMaintenanceStartup(t *testing.T) {
	mediaDir := t.TempDir()
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", mediaDir)
	cacheDir := filepath.Join(mediaDir, "download-cache", "objects", "aa", "test")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cached := filepath.Join(cacheDir, "media.txt")
	if err := os.WriteFile(cached, []byte("expired"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -90)
	if err := os.Chtimes(cached, old, old); err != nil {
		t.Fatal(err)
	}
	s, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, kind := range []storage.AppLogKind{storage.LogKindDebug, storage.LogKindOperation} {
		if err := s.AppendLog(ctx, storage.AppLogEntry{Kind: kind, CreatedAt: time.Now().AddDate(0, 0, -90)}); err != nil {
			t.Fatal(err)
		}
	}
	stop := startStorageMaintenance(ctx, s, storageConfig{LogRetentionDays: -1})
	defer stop()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		logs, err := s.ListLogs(ctx, storage.AppLogFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(logs) == 1 && logs[0].Kind == storage.LogKindOperation {
			if _, err := os.Stat(cached); !os.IsNotExist(err) {
				t.Fatal("startup did not prune idle download cache")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("startup did not prune debug logs while preserving disabled operation logs")
}

func TestDownloadCacheConfig(t *testing.T) {
	for _, tc := range []struct {
		days    int
		maxMB   int64
		invalid bool
	}{
		{0, 0, false}, {7, 0, false}, {30, 1024, false}, {-1, 0, false},
		{36500, 1 << 20, false}, {-2, 0, true}, {36501, 0, true},
		{7, -1, true}, {7, (1 << 20) + 1, true},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		body := fmt.Sprintf("storage:\n  download_cache_retention_days: %d\n  download_cache_max_mb: %d\n", tc.days, tc.maxMB)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadAppConfig(path)
		if (err != nil) != tc.invalid {
			t.Errorf("%+v: %v", tc, err)
		}
		if !tc.invalid && (cfg.Storage.DownloadCacheRetentionDays != tc.days || cfg.Storage.DownloadCacheMaxMB != tc.maxMB) {
			t.Errorf("config values lost: %+v", cfg.Storage)
		}
	}
}
