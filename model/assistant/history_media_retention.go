// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type HistoryMediaRetentionPolicy struct {
	RetentionDays int   `json:"retention_days"`
	MaxMB         int64 `json:"max_mb"`
}

func (p HistoryMediaRetentionPolicy) Validate() error {
	if p.RetentionDays < -1 || p.RetentionDays > 36500 || p.MaxMB < 0 || p.MaxMB > 1<<20 {
		return fmt.Errorf("历史媒体保留天数必须为 -1 到 36500，容量必须为 0 到 1048576 MiB")
	}
	return nil
}

func (p HistoryMediaRetentionPolicy) WithDefaults() HistoryMediaRetentionPolicy {
	if p.RetentionDays == 0 {
		p.RetentionDays = -1
	}
	return p
}

var historyMediaRetention = struct {
	sync.Mutex
	policy HistoryMediaRetentionPolicy
}{policy: HistoryMediaRetentionPolicy{RetentionDays: -1}}

func ConfigureHistoryMediaRetention(policy HistoryMediaRetentionPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	policy = policy.WithDefaults()
	historyMediaRetention.Lock()
	historyMediaRetention.policy = policy
	historyMediaRetention.Unlock()
	return nil
}

func CurrentHistoryMediaRetentionPolicy() HistoryMediaRetentionPolicy {
	historyMediaRetention.Lock()
	defer historyMediaRetention.Unlock()
	return historyMediaRetention.policy
}

type HistoryMediaCleanupResult struct {
	DeletedFiles   int   `json:"deleted_files"`
	DeletedBytes   int64 `json:"deleted_bytes"`
	RemainingBytes int64 `json:"remaining_bytes"`
}

// CleanupHistoryMedia removes persisted history originals by last-use time and
// capacity. The independently managed download-cache and source indices are excluded.
func CleanupHistoryMedia() (HistoryMediaCleanupResult, error) {
	policy := CurrentHistoryMediaRetentionPolicy()
	if policy.RetentionDays < 0 && policy.MaxMB == 0 {
		return HistoryMediaCleanupResult{}, nil
	}
	dir, err := historyMediaDir()
	if err != nil {
		return HistoryMediaCleanupResult{}, err
	}
	type item struct {
		path string
		info fs.FileInfo
	}
	var items []item
	var total int64
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != dir && (name == "download-cache" || name == ".sources") {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || strings.HasPrefix(entry.Name(), ".partial-") {
			return nil
		}
		items = append(items, item{path, info})
		total += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return HistoryMediaCleanupResult{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].info.ModTime().Before(items[j].info.ModTime()) })
	maxBytes := policy.MaxMB << 20
	cutoff := time.Time{}
	if policy.RetentionDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -policy.RetentionDays)
	}
	result := HistoryMediaCleanupResult{RemainingBytes: total}
	for _, entry := range items {
		expired := !cutoff.IsZero() && entry.info.ModTime().Before(cutoff)
		overCapacity := maxBytes > 0 && result.RemainingBytes > maxBytes
		if !expired && !overCapacity {
			continue
		}
		if err := os.Remove(entry.path); err != nil {
			continue
		}
		result.DeletedFiles++
		result.DeletedBytes += entry.info.Size()
		result.RemainingBytes -= entry.info.Size()
	}
	removeEmptyHistoryMediaDirs(dir)
	return result, nil
}

func removeEmptyHistoryMediaDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() && path != root && entry.Name() != "download-cache" && entry.Name() != ".sources" {
			dirs = append(dirs, path)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		_ = os.Remove(dir)
	}
}
