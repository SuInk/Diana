// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// WorkspaceArea 是 WebUI 工作目录页上的一个分区。
type WorkspaceArea struct {
	// Key 是 keep、downloads、outputs、tmp、browser、trash、other 之一。
	Key   string `json:"key"`
	Label string `json:"label"`
	Path  string `json:"path"`
	// BotID 是长期区所属机器人在 keep/ 下的目录名，BotName 由 WebUI 按配置补上。
	BotID      string              `json:"bot_id,omitempty"`
	BotName    string              `json:"bot_name,omitempty"`
	Retention  string              `json:"retention"`
	Bytes      int64               `json:"bytes"`
	Files      int                 `json:"files"`
	QuotaBytes int64               `json:"quota_bytes,omitempty"`
	Entries    []WorkspaceFileInfo `json:"entries"`
	Truncated  bool                `json:"truncated,omitempty"`
}

// WorkspaceListing 是整个工作目录按分区的列表。运行时配置、凭据和 .diana/ 不出现在里面。
type WorkspaceListing struct {
	Root         string              `json:"root"`
	CollectedAt  time.Time           `json:"collected_at"`
	Areas        []WorkspaceArea     `json:"areas"`
	Loose        []WorkspaceFileInfo `json:"loose"`
	OrphanCoding []WorkspaceFileInfo `json:"orphan_coding"`
}

// DefaultWorkspaceListLimit 是每个分区最多列出的文件数，按修改时间取最新的。
const DefaultWorkspaceListLimit = 300

// ListWorkspace 按分区列出工作目录里的文件。
func ListWorkspace(root string, opts WorkspaceCleanupOptions, limit int) (WorkspaceListing, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	if limit <= 0 {
		limit = DefaultWorkspaceListLimit
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return WorkspaceListing{}, err
	}
	listing := WorkspaceListing{Root: abs, CollectedAt: now, Areas: []WorkspaceArea{}, Loose: []WorkspaceFileInfo{}, OrphanCoding: []WorkspaceFileInfo{}}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return listing, nil
	}
	policy := opts.Policy.withDefaults()
	protected := agentProtectedFiles(Config{WorkDir: abs})

	other := WorkspaceArea{Key: "other", Label: "其他目录", Path: ".", Retention: "不自动清理"}
	if entries, err := os.ReadDir(filepath.Join(abs, WorkspaceKeepDir)); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				// 直接落在 keep/ 下、不属于任何机器人的文件（命令或截图写进来的），归到「其他」。
				if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
					other.Bytes += info.Size()
					other.Files++
					other.Entries = append(other.Entries, WorkspaceFileInfo{Path: WorkspaceKeepDir + "/" + entry.Name(), Name: entry.Name(), Size: info.Size(), Modified: info.ModTime()})
				}
				continue
			}
			botDir := entry.Name()
			area := WorkspaceArea{
				Key: "keep", Label: "长期保存", Path: WorkspaceKeepDir + "/" + botDir, BotID: botDir,
				Retention: "不自动清理", QuotaBytes: KeepQuotaBytes,
			}
			collectWorkspaceArea(abs, &area, protected, limit)
			if index, err := loadKeepIndexDir(abs, botDir); err == nil {
				byPath := make(map[string]KeepEntry, len(index))
				for _, item := range index {
					byPath[item.Path] = item
				}
				for i := range area.Entries {
					if item, ok := byPath[area.Entries[i].Path]; ok {
						area.Entries[i].Description = item.Description
						area.Entries[i].SavedBy = item.SavedBy
						area.Entries[i].SavedAt = item.SavedAt
						area.Entries[i].MIME = item.MIME
					}
				}
			}
			listing.Areas = append(listing.Areas, area)
		}
	}
	for _, spec := range []struct {
		key, label, dir string
		maxAge          time.Duration
	}{
		{"downloads", "下载", WorkspaceDownloadsDir, policy.DownloadsMaxAge},
		{"outputs", "产出", WorkspaceOutputsDir, policy.OutputsMaxAge},
		{"tmp", "临时文件", WorkspaceTmpDir, policy.TmpMaxAge},
		{"browser", "浏览器截图", WorkspaceBrowserDir, policy.BrowserMaxAge},
		{"trash", "回收站", WorkspaceTrashDir, policy.TrashMaxAge},
	} {
		area := WorkspaceArea{Key: spec.key, Label: spec.label, Path: spec.dir, Retention: retentionLabel(spec.maxAge)}
		if spec.key == "trash" {
			area.Retention = "删除 " + retentionDays(spec.maxAge) + " 天后永久清理"
		}
		collectWorkspaceArea(abs, &area, protected, limit)
		listing.Areas = append(listing.Areas, area)
	}
	if entries, err := os.ReadDir(abs); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || workspaceReservedTopLevel[entry.Name()] || protected.blocked(filepath.Join(abs, entry.Name())) {
				continue
			}
			collectWorkspaceTree(abs, entry.Name(), &other, protected)
		}
	}
	finishWorkspaceArea(&other, limit)
	listing.Areas = append(listing.Areas, other)
	listing.Loose = looseWorkspaceFiles(abs, protected)
	if listing.Loose == nil {
		listing.Loose = []WorkspaceFileInfo{}
	}
	if opts.CodingReferenced != nil {
		if idle := idleCodingWorkspaces(abs, now.Add(-policy.CodingIdleAge), opts.CodingReferenced); idle != nil {
			listing.OrphanCoding = idle
		}
	}
	return listing, nil
}

func retentionDays(age time.Duration) string {
	return strconv.Itoa(max(int(age/(24*time.Hour)), 1))
}

func retentionLabel(age time.Duration) string {
	return retentionDays(age) + " 天后自动清理"
}

func collectWorkspaceArea(root string, area *WorkspaceArea, protected protectedFiles, limit int) {
	collectWorkspaceTree(root, area.Path, area, protected)
	finishWorkspaceArea(area, limit)
}

// collectWorkspaceTree 把 rel 下的普通文件记进分区。软链接和凭据不列：前者可能指到
// 工作目录外面，后者本来就不该在界面上出现。
func collectWorkspaceTree(root, rel string, area *WorkspaceArea, protected protectedFiles) {
	base := filepath.Join(root, filepath.FromSlash(rel))
	if info, err := os.Lstat(base); err != nil || !info.IsDir() {
		return
	}
	_ = filepath.WalkDir(base, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if protected.blocked(current) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		relPath, err := filepath.Rel(root, current)
		if err != nil {
			return nil
		}
		area.Bytes += info.Size()
		area.Files++
		area.Entries = append(area.Entries, WorkspaceFileInfo{
			Path: filepath.ToSlash(relPath), Name: entry.Name(), Size: info.Size(), Modified: info.ModTime(),
		})
		return nil
	})
}

func finishWorkspaceArea(area *WorkspaceArea, limit int) {
	sortWorkspaceFiles(area.Entries)
	if len(area.Entries) > limit {
		area.Entries = area.Entries[:limit]
		area.Truncated = true
	}
	if area.Entries == nil {
		area.Entries = []WorkspaceFileInfo{}
	}
}

// OpenWorkspaceFile 打开工作目录内的一个普通文件给 WebUI 下载。和文件工具同一套边界：
// 不许走出工作目录（软链接也不行），运行时配置、凭据和 .diana/ 一律不给。打开经
// os.OpenRoot，校验和打开之间换掉软链接也出不了工作目录。
func OpenWorkspaceFile(cfg Config, rel string) (*os.File, os.FileInfo, string, error) {
	root, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		return nil, nil, "", err
	}
	target, err := safePath(root, rel)
	if err != nil {
		return nil, nil, "", err
	}
	clean := relPathForOutput(root, target)
	if clean == "." {
		return nil, nil, "", errors.New("需要一个具体的文件路径")
	}
	if agentProtectedFiles(Config{WorkDir: root, MCPConfigPath: cfg.MCPConfigPath}).blocked(target) {
		return nil, nil, "", errProtectedFile(clean)
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, "", err
	}
	defer handle.Close()
	file, err := handle.Open(filepath.FromSlash(clean))
	if err != nil {
		return nil, nil, "", missingFileError(root, clean, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, "", err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, "", fmt.Errorf("%s 不是普通文件", clean)
	}
	return file, info, clean, nil
}
