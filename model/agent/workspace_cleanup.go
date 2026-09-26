// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 工作目录以前没有任何清理：下载的、生成的、截图的、回收站里的，一律永久留着。
// 这里按分区定保留天数，每天由存储维护任务跑一遍：
//
//   - tmp/ 草稿和中间文件，1 天；
//   - downloads/ 和 .agent-browser/ 下载与截图，7 天；
//   - outputs/ 机器人产出的东西，30 天；
//   - .trash/ 回收站，按删除时间 7 天。
//
// 永远不碰的：运行时配置和凭据、.diana/、.agents/、skills/、coding 相关目录、keep/。
// 根下散落的文件和没人引用的编码工作区只报告不删：它们是谁放的、还要不要，程序
// 判断不了，删错了就没了。

// WorkspaceCleanupPolicy 是各分区的保留时长。
type WorkspaceCleanupPolicy struct {
	TmpMaxAge       time.Duration
	DownloadsMaxAge time.Duration
	BrowserMaxAge   time.Duration
	OutputsMaxAge   time.Duration
	TrashMaxAge     time.Duration
	// CodingIdleAge 是编码工作区多久没动、又没有机器人配置引用时报告为闲置。
	CodingIdleAge time.Duration
}

// DefaultWorkspaceCleanupPolicy 是默认保留时长。
var DefaultWorkspaceCleanupPolicy = WorkspaceCleanupPolicy{
	TmpMaxAge:       24 * time.Hour,
	DownloadsMaxAge: 7 * 24 * time.Hour,
	BrowserMaxAge:   7 * 24 * time.Hour,
	OutputsMaxAge:   30 * 24 * time.Hour,
	TrashMaxAge:     7 * 24 * time.Hour,
	CodingIdleAge:   30 * 24 * time.Hour,
}

// CodingWorkspaceDirName 是编码代理 clone 仓库的目录，下面的 .jobs 是任务记录。
const CodingWorkspaceDirName = "coding"

// WorkspaceCleanupOptions 控制一次清理。
type WorkspaceCleanupOptions struct {
	// Now 是判断过期用的当前时间，留空取 time.Now()。测试用它固定时钟。
	Now    time.Time
	Policy WorkspaceCleanupPolicy
	// CodingReferenced 报告 coding/<name> 是否还被某台机器人的编码代理配置引用。
	// 为 nil 时不做闲置编码工作区的报告。
	CodingReferenced func(name string) bool
}

// WorkspaceFileInfo 是清理报告和工作目录概览（散落文件、闲置编码工作区）里的一个条目。
type WorkspaceFileInfo struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	IsDir    bool      `json:"is_dir,omitempty"`
}

// WorkspaceCleanupAreaResult 是一个分区这次删掉了多少。
type WorkspaceCleanupAreaResult struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// WorkspaceCleanupReport 是一次清理的结果。
type WorkspaceCleanupReport struct {
	DeletedFiles int                                   `json:"deleted_files"`
	DeletedBytes int64                                 `json:"deleted_bytes"`
	ByArea       map[string]WorkspaceCleanupAreaResult `json:"by_area"`
	// LooseFiles 是工作目录根下散落的文件，只报告。
	LooseFiles []WorkspaceFileInfo `json:"loose_files"`
	// IdleCoding 是没有配置引用、又长期没动的编码工作区，只报告。
	IdleCoding []WorkspaceFileInfo `json:"idle_coding"`
}

func (p WorkspaceCleanupPolicy) withDefaults() WorkspaceCleanupPolicy {
	d := DefaultWorkspaceCleanupPolicy
	pick := func(value, fallback time.Duration) time.Duration {
		if value > 0 {
			return value
		}
		return fallback
	}
	return WorkspaceCleanupPolicy{
		TmpMaxAge:       pick(p.TmpMaxAge, d.TmpMaxAge),
		DownloadsMaxAge: pick(p.DownloadsMaxAge, d.DownloadsMaxAge),
		BrowserMaxAge:   pick(p.BrowserMaxAge, d.BrowserMaxAge),
		OutputsMaxAge:   pick(p.OutputsMaxAge, d.OutputsMaxAge),
		TrashMaxAge:     pick(p.TrashMaxAge, d.TrashMaxAge),
		CodingIdleAge:   pick(p.CodingIdleAge, d.CodingIdleAge),
	}
}

// workspaceAgedAreas 是按文件修改时间清理的分区。
func (p WorkspaceCleanupPolicy) agedAreas() []struct {
	dir    string
	maxAge time.Duration
} {
	return []struct {
		dir    string
		maxAge time.Duration
	}{
		{WorkspaceTmpDir, p.TmpMaxAge},
		{WorkspaceDownloadsDir, p.DownloadsMaxAge},
		{WorkspaceBrowserDir, p.BrowserMaxAge},
		{WorkspaceOutputsDir, p.OutputsMaxAge},
	}
}

// workspaceReservedTopLevel 是工作目录下有固定用途、不算「其他」的顶层名字。
var workspaceReservedTopLevel = map[string]bool{
	WorkspaceKeepDir: true, WorkspaceDownloadsDir: true, WorkspaceOutputsDir: true, WorkspaceTmpDir: true,
	WorkspaceTrashDir: true, WorkspaceBrowserDir: true, DianaStateDirName: true, ".agents": true, "skills": true,
	CodingWorkspaceDirName: true, CodingRuntimeDirName: true,
}

// CleanupWorkspace 按保留时长清理工作目录。单个文件删不掉不让整次清理失败，
// 错误合并后一起返回，报告里是实际删掉的部分。
func CleanupWorkspace(root string, opts WorkspaceCleanupOptions) (WorkspaceCleanupReport, error) {
	report := WorkspaceCleanupReport{ByArea: map[string]WorkspaceCleanupAreaResult{}}
	root = strings.TrimSpace(root)
	if root == "" {
		return report, nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return report, err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		// 还没用过 Agent 的部署没有工作目录，不是错误。
		return report, nil
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	policy := opts.Policy.withDefaults()
	protected := agentProtectedFiles(Config{WorkDir: abs})
	var errs []error
	for _, area := range policy.agedAreas() {
		result, err := pruneAgedArea(abs, area.dir, now.Add(-area.maxAge), protected)
		if err != nil {
			errs = append(errs, err)
		}
		if result.Files > 0 {
			report.ByArea[area.dir] = result
		}
	}
	trash, err := pruneTrash(abs, now.Add(-policy.TrashMaxAge))
	if err != nil {
		errs = append(errs, err)
	}
	if trash.Files > 0 {
		report.ByArea[WorkspaceTrashDir] = trash
	}
	for _, result := range report.ByArea {
		report.DeletedFiles += result.Files
		report.DeletedBytes += result.Bytes
	}
	report.LooseFiles = looseWorkspaceFiles(abs, protected)
	if opts.CodingReferenced != nil {
		report.IdleCoding = idleCodingWorkspaces(abs, now.Add(-policy.CodingIdleAge), opts.CodingReferenced)
	}
	return report, errors.Join(errs...)
}

// pruneAgedArea 删掉一个分区里修改时间早于 cutoff 的文件，再收掉因此空出来的子目录。
// 分区目录本身留着：它是约定的一部分，删了模型下次还得重建。
func pruneAgedArea(root, dir string, cutoff time.Time, protected protectedFiles) (WorkspaceCleanupAreaResult, error) {
	var result WorkspaceCleanupAreaResult
	base := filepath.Join(root, dir)
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() {
		// 分区不存在，或者被换成了软链接：软链接指向哪里不归这里管，不跟进去删。
		return result, nil
	}
	var dirs []string
	var errs []error
	_ = filepath.WalkDir(base, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if protected.blocked(current) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if current != base {
				dirs = append(dirs, current)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			return nil
		}
		if err := os.Remove(current); err != nil {
			errs = append(errs, err)
			return nil
		}
		result.Files++
		if info.Mode().IsRegular() {
			result.Bytes += info.Size()
		}
		return nil
	})
	// 从最深的往上收，只删空的：还有新文件的目录自然删不掉。
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, current := range dirs {
		if entries, err := os.ReadDir(current); err == nil && len(entries) == 0 {
			_ = os.Remove(current)
		}
	}
	return result, errors.Join(errs...)
}

// pruneTrash 按删除时间清回收站：每次删除是 .trash/<时间戳>/ 一个目录，时间戳就是
// 删除的时刻。目录名认不出时退回看目录的修改时间。
func pruneTrash(root string, cutoff time.Time) (WorkspaceCleanupAreaResult, error) {
	var result WorkspaceCleanupAreaResult
	base := filepath.Join(root, WorkspaceTrashDir)
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() {
		return result, nil
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return result, err
	}
	var errs []error
	for _, entry := range entries {
		deletedAt, ok := trashEntryTime(entry.Name())
		if !ok {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			deletedAt = info.ModTime()
		}
		if !deletedAt.Before(cutoff) {
			continue
		}
		current := filepath.Join(base, entry.Name())
		files, bytes := treeUsage(current)
		if err := os.RemoveAll(current); err != nil {
			errs = append(errs, err)
			continue
		}
		result.Files += files
		result.Bytes += bytes
	}
	return result, errors.Join(errs...)
}

// trashEntryTime 从回收站子目录名（20060102-150405 或带 -2 这类序号）读出删除时间。
// 目录名是按本地时间写的，按本地时间读。
func trashEntryTime(name string) (time.Time, bool) {
	if len(name) < len(trashTimestampLayout) {
		return time.Time{}, false
	}
	stamp, err := time.ParseInLocation(trashTimestampLayout, name[:len(trashTimestampLayout)], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return stamp, true
}

// treeUsage 统计一棵目录树里的文件数和字节数（文件本身也可以）。
func treeUsage(target string) (int, int64) {
	files := 0
	var bytes int64
	_ = filepath.WalkDir(target, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		files++
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes
}

// EmptyWorkspaceTrash 永久删掉回收站里的全部内容，给 WebUI 的「清空回收站」用。
func EmptyWorkspaceTrash(root string) (int, int64, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return 0, 0, err
	}
	base := filepath.Join(abs, WorkspaceTrashDir)
	info, err := os.Lstat(base)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if !info.IsDir() {
		return 0, 0, errors.New(WorkspaceTrashDir + " 不是目录")
	}
	files, bytes := treeUsage(base)
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0, 0, err
	}
	var errs []error
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(base, entry.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		remaining, remainingBytes := treeUsage(base)
		return files - remaining, bytes - remainingBytes, errors.Join(errs...)
	}
	return files, bytes, nil
}

// looseWorkspaceFiles 列出根下散落的普通文件。点开头的是运行时自己的东西，不算。
func looseWorkspaceFiles(root string, protected protectedFiles) []WorkspaceFileInfo {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []WorkspaceFileInfo
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || protected.blocked(filepath.Join(root, name)) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, WorkspaceFileInfo{Path: name, Name: name, Size: info.Size(), Modified: info.ModTime()})
	}
	sortWorkspaceFiles(out)
	return out
}

// idleCodingWorkspaces 列出 coding/ 下没有配置引用、又在 cutoff 之前就没再动过的仓库。
// 「动过」看目录本身和 .git 里 index、FETCH_HEAD、HEAD 的修改时间：整棵仓库遍历
// 一遍太贵，而编码代理每次干活都会碰到这几个文件。
func idleCodingWorkspaces(root string, cutoff time.Time, referenced func(string) bool) []WorkspaceFileInfo {
	base := filepath.Join(root, CodingWorkspaceDirName)
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []WorkspaceFileInfo
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || referenced(name) {
			continue
		}
		dir := filepath.Join(base, name)
		latest := time.Time{}
		for _, probe := range []string{dir, filepath.Join(dir, ".git", "index"), filepath.Join(dir, ".git", "FETCH_HEAD"), filepath.Join(dir, ".git", "HEAD")} {
			if info, err := os.Stat(probe); err == nil && info.ModTime().After(latest) {
				latest = info.ModTime()
			}
		}
		if !latest.Before(cutoff) {
			continue
		}
		_, size := treeUsage(dir)
		out = append(out, WorkspaceFileInfo{Path: path.Join(CodingWorkspaceDirName, name), Name: name, Size: size, Modified: latest, IsDir: true})
	}
	sortWorkspaceFiles(out)
	return out
}

func sortWorkspaceFiles(items []WorkspaceFileInfo) {
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].Modified.Equal(items[j].Modified) {
			return items[i].Modified.After(items[j].Modified)
		}
		return items[i].Path < items[j].Path
	})
}
