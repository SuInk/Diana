// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/hostinfo"
	"github.com/gin-gonic/gin"
)

// 设置页的存储卡片要回答两件事：这块盘还剩多少，以及 Diana 自己占掉的那部分
// 是被什么吃掉的。总览页的「资源占用」只给一个总数，删不删得动、该删哪一类，
// 光看总数判断不了，所以这里按文件类型拆开。
type StorageUsageCategory struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Bytes uint64 `json:"bytes"`
	Files int    `json:"files"`
}

type StorageUsageResponse struct {
	CollectedAt      time.Time              `json:"collected_at"`
	Path             string                 `json:"path"`
	DiskTotalBytes   uint64                 `json:"disk_total_bytes,omitempty"`
	DiskUsedBytes    uint64                 `json:"disk_used_bytes,omitempty"`
	DiskFreeBytes    uint64                 `json:"disk_free_bytes,omitempty"`
	DiskUsagePercent float64                `json:"disk_usage_percent,omitempty"`
	DianaBytes       uint64                 `json:"diana_bytes"`
	DianaFiles       int                    `json:"diana_files"`
	Categories       []StorageUsageCategory `json:"categories"`
	// Directories 按数据目录的顶层目录拆，工作目录再按分区拆一层：光看文件类型分不清
	// 几个 G 的图片是历史媒体原件、下载缓存还是工作目录里攒下的。
	Directories     []StorageUsageCategory `json:"directories"`
	ScannedAt       *time.Time             `json:"scanned_at,omitempty"`
	Scanning        bool                   `json:"scanning"`
	DiskUnavailable string                 `json:"disk_unavailable,omitempty"`
}

type StorageUsageHandler struct {
	path string
}

// NewStorageUsageHandler 接收配置里的数据库路径，落到它所在的数据目录上。
func NewStorageUsageHandler(dbPath string) *StorageUsageHandler {
	return &StorageUsageHandler{path: dashboardStoragePath(dbPath)}
}

func (h *StorageUsageHandler) Register(router gin.IRouter) {
	router.GET("/api/system/storage", h.get)
}

func (h *StorageUsageHandler) get(c *gin.Context) {
	c.JSON(http.StatusOK, collectStorageUsage(h.path, time.Now()))
}

func collectStorageUsage(dir string, now time.Time) StorageUsageResponse {
	if now.IsZero() {
		now = time.Now()
	}
	response := StorageUsageResponse{CollectedAt: now, Path: dir, Categories: []StorageUsageCategory{}, Directories: []StorageUsageCategory{}}
	if total, used, free, err := hostinfo.StorageUsage(dir); err == nil {
		response.DiskTotalBytes = total
		response.DiskUsedBytes = used
		response.DiskFreeBytes = free
		if total > 0 {
			response.DiskUsagePercent = hostinfo.RoundPercent((float64(used) / float64(total)) * 100)
		}
	} else {
		response.DiskUnavailable = err.Error()
	}
	breakdown := dataDirectoryBreakdown(dir)
	response.DianaBytes = breakdown.total
	response.DianaFiles = breakdown.files
	response.Scanning = breakdown.scanning
	if !breakdown.measuredAt.IsZero() {
		scannedAt := breakdown.measuredAt
		response.ScannedAt = &scannedAt
	}
	for _, category := range storageCategories {
		bytes := breakdown.bytesByKey[category.key]
		files := breakdown.filesByKey[category.key]
		if bytes == 0 && files == 0 {
			continue
		}
		response.Categories = append(response.Categories, StorageUsageCategory{
			Key:   category.key,
			Label: category.label,
			Bytes: bytes,
			Files: files,
		})
	}
	// 饼图从大到小排，颜色顺序才和图例一致；同样大小时按固定顺序兜底，避免
	// 两次刷新之间扇区来回跳。
	sort.SliceStable(response.Categories, func(i, j int) bool {
		return response.Categories[i].Bytes > response.Categories[j].Bytes
	})
	for key, bytes := range breakdown.dirBytes {
		response.Directories = append(response.Directories, StorageUsageCategory{
			Key: key, Label: storageDirectoryLabel(key), Bytes: bytes, Files: breakdown.dirFiles[key],
		})
	}
	sort.Slice(response.Directories, func(i, j int) bool {
		if response.Directories[i].Bytes != response.Directories[j].Bytes {
			return response.Directories[i].Bytes > response.Directories[j].Bytes
		}
		return response.Directories[i].Key < response.Directories[j].Key
	})
	return response
}

type storageCategory struct {
	key        string
	label      string
	extensions []string
}

// 分类按「用户看得懂的东西」划，不按目录结构：同一批历史媒体原件里图片和视频
// 混在一个会话目录下，按目录分只会得到一串群号。
var storageCategories = []storageCategory{
	{key: "database", label: "数据库", extensions: []string{".db", ".db-wal", ".db-shm", ".db-journal", ".sqlite", ".sqlite3"}},
	{key: "image", label: "图片", extensions: []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".heic", ".heif", ".avif", ".tif", ".tiff", ".svg", ".ico"}},
	{key: "video", label: "视频", extensions: []string{".mp4", ".mov", ".mkv", ".avi", ".webm", ".flv", ".m4v", ".wmv", ".ts", ".3gp"}},
	{key: "audio", label: "音频", extensions: []string{".mp3", ".wav", ".ogg", ".oga", ".opus", ".amr", ".silk", ".m4a", ".aac", ".flac", ".wma"}},
	{key: "document", label: "文档与压缩包", extensions: []string{".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".md", ".csv", ".zip", ".rar", ".7z", ".gz", ".tar", ".apk"}},
	{key: "other", label: "其它文件", extensions: nil},
}

var storageCategoryByExtension = func() map[string]string {
	index := map[string]string{}
	for _, category := range storageCategories {
		for _, extension := range category.extensions {
			index[extension] = category.key
		}
	}
	return index
}()

func storageCategoryKey(name string) string {
	extension := strings.ToLower(filepath.Ext(name))
	if key, ok := storageCategoryByExtension[extension]; ok {
		return key
	}
	return "other"
}

const storageBreakdownCacheTTL = time.Minute

type storageBreakdown struct {
	total      uint64
	files      int
	bytesByKey map[string]uint64
	filesByKey map[string]int
	dirBytes   map[string]uint64
	dirFiles   map[string]int
	measuredAt time.Time
	scanning   bool
}

var storageBreakdownCache = struct {
	sync.Mutex
	byPath map[string]*storageBreakdown
}{byPath: map[string]*storageBreakdown{}}

// dataDirectoryBreakdown 和总览页的 dataDirectorySize 同一个权衡：媒体缓存可能
// 有几十万个文件，遍历一次不便宜，所以永远返回上一次的结果，过期后在后台重算。
// 首次打开设置页会拿到 scanning=true 和空结果，前端据此显示「统计中」并重试。
func dataDirectoryBreakdown(dir string) storageBreakdown {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return storageBreakdown{}
	}
	now := time.Now()
	storageBreakdownCache.Lock()
	defer storageBreakdownCache.Unlock()
	entry := storageBreakdownCache.byPath[dir]
	if entry == nil {
		entry = &storageBreakdown{bytesByKey: map[string]uint64{}, filesByKey: map[string]int{}}
		storageBreakdownCache.byPath[dir] = entry
	}
	stale := now.Sub(entry.measuredAt) >= storageBreakdownCacheTTL
	if stale && !entry.scanning {
		entry.scanning = true
		go func() {
			defer recoverGoroutinePanic("storage_usage.go:dataDirectoryBreakdown")
			measured := walkDirectoryBreakdown(dir)
			storageBreakdownCache.Lock()
			entry.total = measured.total
			entry.files = measured.files
			entry.bytesByKey = measured.bytesByKey
			entry.filesByKey = measured.filesByKey
			entry.dirBytes = measured.dirBytes
			entry.dirFiles = measured.dirFiles
			entry.measuredAt = time.Now()
			entry.scanning = false
			storageBreakdownCache.Unlock()
		}()
	}
	snapshot := storageBreakdown{
		total:      entry.total,
		files:      entry.files,
		bytesByKey: map[string]uint64{},
		filesByKey: map[string]int{},
		dirBytes:   map[string]uint64{},
		dirFiles:   map[string]int{},
		measuredAt: entry.measuredAt,
		scanning:   entry.scanning,
	}
	for key, bytes := range entry.bytesByKey {
		snapshot.bytesByKey[key] = bytes
	}
	for key, files := range entry.filesByKey {
		snapshot.filesByKey[key] = files
	}
	for key, bytes := range entry.dirBytes {
		snapshot.dirBytes[key] = bytes
	}
	for key, files := range entry.dirFiles {
		snapshot.dirFiles[key] = files
	}
	return snapshot
}

func walkDirectoryBreakdown(dir string) storageBreakdown {
	result := storageBreakdown{bytesByKey: map[string]uint64{}, filesByKey: map[string]int{}, dirBytes: map[string]uint64{}, dirFiles: map[string]int{}}
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		// 权限不足或文件正好被删掉都不该让整次统计失败，跳过继续走。
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		size := uint64(info.Size())
		key := storageCategoryKey(path)
		result.total += size
		result.files++
		result.bytesByKey[key] += size
		result.filesByKey[key]++
		if rel, err := filepath.Rel(dir, path); err == nil {
			dirKey := storageDirectoryKey(filepath.ToSlash(rel), key)
			result.dirBytes[dirKey] += size
			result.dirFiles[dirKey]++
		}
		return nil
	})
	return result
}

// storageWorkspaceAreas 是工作目录下单独列出来的分区，其余的归到 workspace/other。
var storageWorkspaceAreas = map[string]string{
	"keep":           "工作目录 · 长期保存",
	"downloads":      "工作目录 · 下载",
	"outputs":        "工作目录 · 产出",
	"tmp":            "工作目录 · 临时文件",
	".trash":         "工作目录 · 回收站",
	".agent-browser": "工作目录 · 浏览器截图",
	"coding":         "工作目录 · 编码工作区",
	"coding-runtime": "工作目录 · 编码代理运行时",
	"skills":         "工作目录 · Skills",
	".agents":        "工作目录 · Skills",
	".diana":         "工作目录 · 运行时状态",
}

var storageDirectoryLabels = map[string]string{
	"workspace/other": "工作目录 · 其他",
	"history-media":   "历史媒体原件",
	"download-cache":  "下载缓存",
	"media":           "媒体文件",
	"manual-backups":  "手动备份",
	"browser-box":     "内置浏览器",
	"browser":         "浏览器数据",
	"plugin-sources":  "插件源码",
	".diana-updates":  "更新包",
	"database":        "数据库",
	"files":           "数据目录根下的其他文件",
}

// storageDirectoryKey 把数据目录内的相对路径归到一个目录键：顶层目录名，工作目录
// 再细分到分区；顶层散落的文件按是不是数据库分两类。
func storageDirectoryKey(rel, category string) string {
	top, rest, nested := strings.Cut(rel, "/")
	if !nested {
		if category == "database" {
			return "database"
		}
		return "files"
	}
	if top != "workspace" {
		return top
	}
	area, _, nested := strings.Cut(rest, "/")
	if !nested {
		return "workspace/other"
	}
	if _, ok := storageWorkspaceAreas[area]; ok {
		if area == ".agents" {
			area = "skills"
		}
		return "workspace/" + area
	}
	return "workspace/other"
}

func storageDirectoryLabel(key string) string {
	if area, ok := strings.CutPrefix(key, "workspace/"); ok {
		if label, ok := storageWorkspaceAreas[area]; ok {
			return label
		}
	}
	if label, ok := storageDirectoryLabels[key]; ok {
		return label
	}
	return key
}
