// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectStorageUsageBreaksDownByFileType(t *testing.T) {
	dir := t.TempDir()
	writeStorageSample(t, filepath.Join(dir, "diana.db"), 2048)
	writeStorageSample(t, filepath.Join(dir, "history-media", "group_1", "media.jpg"), 1024)
	writeStorageSample(t, filepath.Join(dir, "history-media", "group_1", "media.PNG"), 512)
	writeStorageSample(t, filepath.Join(dir, "history-media", "group_1", "media.mp4"), 4096)
	writeStorageSample(t, filepath.Join(dir, "history-media", "download-cache", "blob"), 256)

	usage := waitForStorageScan(t, dir)
	if usage.Scanning {
		t.Fatalf("scan still running after waiting")
	}
	if usage.DianaBytes != 2048+1024+512+4096+256 {
		t.Fatalf("unexpected total %d", usage.DianaBytes)
	}
	if usage.DianaFiles != 5 {
		t.Fatalf("unexpected file count %d", usage.DianaFiles)
	}
	byKey := map[string]StorageUsageCategory{}
	for _, category := range usage.Categories {
		byKey[category.Key] = category
	}
	// 大小写不同的扩展名要归到同一类，没有扩展名的落到「其它文件」。
	if got := byKey["image"]; got.Bytes != 1536 || got.Files != 2 {
		t.Fatalf("unexpected image category %+v", got)
	}
	if got := byKey["video"]; got.Bytes != 4096 || got.Files != 1 {
		t.Fatalf("unexpected video category %+v", got)
	}
	if got := byKey["database"]; got.Bytes != 2048 {
		t.Fatalf("unexpected database category %+v", got)
	}
	if got := byKey["other"]; got.Bytes != 256 {
		t.Fatalf("unexpected other category %+v", got)
	}
	if _, ok := byKey["audio"]; ok {
		t.Fatalf("empty categories must be omitted: %+v", usage.Categories)
	}
	// 饼图按扇区从大到小画，图例颜色才跟得上。
	for i := 1; i < len(usage.Categories); i++ {
		if usage.Categories[i-1].Bytes < usage.Categories[i].Bytes {
			t.Fatalf("categories not sorted by size: %+v", usage.Categories)
		}
	}
	if usage.DiskUnavailable == "" && usage.DiskTotalBytes == 0 {
		t.Fatalf("disk total missing without an unavailable reason")
	}
	if usage.DiskTotalBytes > 0 && usage.DiskUsedBytes+usage.DiskFreeBytes > usage.DiskTotalBytes {
		t.Fatalf("used %d + free %d exceeds total %d", usage.DiskUsedBytes, usage.DiskFreeBytes, usage.DiskTotalBytes)
	}
}

// 首次调用只触发后台遍历、返回 scanning=true，前端据此重试；这里替它等。
func TestCollectStorageUsageFirstCallReportsScanning(t *testing.T) {
	dir := t.TempDir()
	writeStorageSample(t, filepath.Join(dir, "diana.db"), 16)
	usage := collectStorageUsage(dir, time.Time{})
	if !usage.Scanning {
		t.Fatalf("first call should report scanning")
	}
	if usage.ScannedAt != nil {
		t.Fatalf("first call should not report a scan time")
	}
	waitForStorageScan(t, dir)
}

func waitForStorageScan(t *testing.T, dir string) StorageUsageResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		usage := collectStorageUsage(dir, time.Now())
		if !usage.Scanning && usage.ScannedAt != nil {
			return usage
		}
		if time.Now().After(deadline) {
			t.Fatalf("directory scan did not finish in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func writeStorageSample(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
