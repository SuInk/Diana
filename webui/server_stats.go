// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/hostinfo"
	"github.com/SuInk/diana/model/storage"
)

// 采集本身住在 model/hostinfo：webui 单向依赖 model，反过来不行，而机器人也要
// 用同一批数字回答「内存占多少」。这里只负责把快照映射成总览页的载荷，外加
// 「Diana 自己占了多少磁盘」——那是控制台独有的一项，和机器所在的主机无关。
func collectDashboardServerStats(now time.Time, storagePaths ...string) storage.DashboardServerStats {
	if now.IsZero() {
		now = time.Now()
	}
	storagePath := dashboardStoragePath(storagePaths...)
	snapshot := hostinfo.Collect(now, storagePath)
	return storage.DashboardServerStats{
		CollectedAt:               snapshot.CollectedAt,
		Hostname:                  snapshot.Hostname,
		OS:                        snapshot.OS,
		Arch:                      snapshot.Arch,
		ProcessID:                 snapshot.ProcessID,
		ProcessUptimeSeconds:      snapshot.ProcessUptimeSeconds,
		CPUModel:                  snapshot.CPUModel,
		CPUCores:                  snapshot.CPUCores,
		CPUUsagePercent:           snapshot.CPUUsagePercent,
		ProcessCPUPercent:         snapshot.ProcessCPUPercent,
		MemoryTotalBytes:          snapshot.MemoryTotalBytes,
		MemoryUsedBytes:           snapshot.MemoryUsedBytes,
		MemoryUsagePercent:        snapshot.MemoryUsagePercent,
		ProcessMemoryBytes:        snapshot.ProcessMemoryBytes,
		ProcessStorageBytes:       dataDirectorySize(storagePath),
		StoragePath:               snapshot.StoragePath,
		StorageTotalBytes:         snapshot.StorageTotalBytes,
		StorageUsedBytes:          snapshot.StorageUsedBytes,
		StorageAvailableBytes:     snapshot.StorageAvailableBytes,
		StorageUsagePercent:       snapshot.StorageUsagePercent,
		GoHeapAllocBytes:          snapshot.GoHeapAllocBytes,
		GoHeapSystemBytes:         snapshot.GoHeapSystemBytes,
		GoRoutines:                snapshot.GoRoutines,
		RuntimeVersion:            snapshot.RuntimeVersion,
		MetricsUnavailableReason:  snapshot.MetricsUnavailable,
		ProcessMetricsUnavailable: snapshot.ProcessMetricsUnavailable,
		StorageMetricsUnavailable: snapshot.StorageMetricsUnavailable,
	}
}

const dashboardServerStatsCacheTTL = 5 * time.Second

type cachedServerStats struct {
	collectedAt time.Time
	stats       storage.DashboardServerStats
}

var dashboardServerStatsCache = struct {
	sync.Mutex
	byPath map[string]cachedServerStats
}{byPath: map[string]cachedServerStats{}}

func cachedDashboardServerStats(now time.Time, storagePaths ...string) storage.DashboardServerStats {
	if now.IsZero() {
		now = time.Now()
	}
	storagePath := dashboardStoragePath(storagePaths...)
	dashboardServerStatsCache.Lock()
	defer dashboardServerStatsCache.Unlock()
	if cached, ok := dashboardServerStatsCache.byPath[storagePath]; ok && now.Sub(cached.collectedAt) < dashboardServerStatsCacheTTL {
		return cached.stats
	}
	stats := collectDashboardServerStats(now, storagePath)
	dashboardServerStatsCache.byPath[storagePath] = cachedServerStats{collectedAt: now, stats: stats}
	return stats
}

// dashboardStoragePath 把配置里的数据库路径归一成一个存在的目录。
func dashboardStoragePath(storagePaths ...string) string {
	path := ""
	if len(storagePaths) > 0 {
		path = strings.TrimSpace(storagePaths[0])
	}
	if path == "" {
		path = strings.TrimSpace(os.Getenv("APP_DB_PATH"))
	}
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		path = "data/diana.db"
	}
	absPath, err := filepath.Abs(path)
	if err == nil {
		path = absPath
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	directory := filepath.Dir(path)
	for {
		if info, err := os.Stat(directory); err == nil && info.IsDir() {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return directory
		}
		directory = parent
	}
}

const dashboardDataSizeCacheTTL = time.Minute

type cachedDataSize struct {
	measuredAt time.Time
	bytes      uint64
	measuring  bool
}

var dashboardDataSizeCache = struct {
	sync.Mutex
	byPath map[string]*cachedDataSize
}{byPath: map[string]*cachedDataSize{}}

// dataDirectorySize 返回 Diana 数据目录的体积，也就是它自己占掉的磁盘。
//
// 媒体缓存可能有几十万个文件，遍历一次并不便宜，所以永远返回上一次的结果，
// 过期后在后台协程里重算。总览接口因此不会被一次慢遍历拖住，代价是刚启动的
// 第一次调用返回 0，下一次采样就有值了。
func dataDirectorySize(dir string) uint64 {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return 0
	}
	now := time.Now()
	dashboardDataSizeCache.Lock()
	entry := dashboardDataSizeCache.byPath[dir]
	if entry == nil {
		entry = &cachedDataSize{}
		dashboardDataSizeCache.byPath[dir] = entry
	}
	size := entry.bytes
	stale := now.Sub(entry.measuredAt) >= dashboardDataSizeCacheTTL
	if stale && !entry.measuring {
		entry.measuring = true
		go func() {
			measured := walkDirectorySize(dir)
			dashboardDataSizeCache.Lock()
			entry.bytes = measured
			entry.measuredAt = time.Now()
			entry.measuring = false
			dashboardDataSizeCache.Unlock()
		}()
	}
	dashboardDataSizeCache.Unlock()
	return size
}

func walkDirectorySize(dir string) uint64 {
	var total uint64
	_ = filepath.WalkDir(dir, func(_ string, entry os.DirEntry, err error) error {
		// 权限不足或文件正好被删掉都不该让整次统计失败，跳过继续走。
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		total += uint64(info.Size())
		return nil
	})
	return total
}
