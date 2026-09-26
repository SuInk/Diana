// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// WorkspaceArea 是 WebUI 文件页顶部的一张分区卡片：这一块占了多少、有几个文件、
// 多久会被清掉。具体文件由目录浏览逐层列，这里只给合计，免得一次把几百个文件名全
// 塞进响应里。
type WorkspaceArea struct {
	// Key 是 keep、downloads、outputs、tmp、browser、trash、other 之一。
	Key   string `json:"key"`
	Label string `json:"label"`
	// Path 是分区在工作目录里的相对路径，点卡片时目录浏览跳到这里；「其他目录」是根目录 "."。
	Path string `json:"path"`
	// BotID 是长期区所属机器人在 keep/ 下的目录名，BotName 由 WebUI 按配置补上。
	BotID      string `json:"bot_id,omitempty"`
	BotName    string `json:"bot_name,omitempty"`
	Retention  string `json:"retention"`
	Bytes      int64  `json:"bytes"`
	Files      int    `json:"files"`
	QuotaBytes int64  `json:"quota_bytes,omitempty"`
}

// WorkspaceOverview 是整个工作目录按分区的合计。运行时配置、凭据和 .diana/ 不计在里面。
type WorkspaceOverview struct {
	Root         string              `json:"root"`
	CollectedAt  time.Time           `json:"collected_at"`
	Areas        []WorkspaceArea     `json:"areas"`
	Loose        []WorkspaceFileInfo `json:"loose"`
	OrphanCoding []WorkspaceFileInfo `json:"orphan_coding"`
}

// WorkspaceAreaHint 说明工作目录里某个路径归哪个分区、按什么规则清理。WebUI 在目录
// 浏览时拿它提示「这里的东西 7 天后会被清掉」。
type WorkspaceAreaHint struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Retention string `json:"retention"`
	// BotID 只在长期区的某台机器人目录里才有，是 keep/ 下的目录名。
	BotID   string `json:"bot_id,omitempty"`
	BotName string `json:"bot_name,omitempty"`
}

// workspaceAgedAreaSpec 是按天数清理的几个分区。概览卡片和目录浏览的分区提示用同一份，
// 标签和保留天数只在这里写一次。
type workspaceAgedAreaSpec struct {
	key, label, dir string
	maxAge          func(WorkspaceCleanupPolicy) time.Duration
}

var workspaceAgedAreaSpecs = []workspaceAgedAreaSpec{
	{"downloads", "下载", WorkspaceDownloadsDir, func(p WorkspaceCleanupPolicy) time.Duration { return p.DownloadsMaxAge }},
	{"outputs", "产出", WorkspaceOutputsDir, func(p WorkspaceCleanupPolicy) time.Duration { return p.OutputsMaxAge }},
	{"tmp", "临时文件", WorkspaceTmpDir, func(p WorkspaceCleanupPolicy) time.Duration { return p.TmpMaxAge }},
	{"browser", "浏览器截图", WorkspaceBrowserDir, func(p WorkspaceCleanupPolicy) time.Duration { return p.BrowserMaxAge }},
	{"trash", "回收站", WorkspaceTrashDir, func(p WorkspaceCleanupPolicy) time.Duration { return p.TrashMaxAge }},
}

const (
	workspaceKeepLabel      = "长期保存"
	workspaceKeepRetention  = "不自动清理"
	workspaceOtherLabel     = "其他目录"
	workspaceOtherRetention = "不自动清理"
)

func (s workspaceAgedAreaSpec) retention(policy WorkspaceCleanupPolicy) string {
	age := s.maxAge(policy)
	if s.key == "trash" {
		return "删除 " + retentionDays(age) + " 天后永久清理"
	}
	return retentionLabel(age)
}

// WorkspaceAreaOf 返回相对路径 rel 所在的分区；工作目录根和编码仓库、Skills 这类
// 有固定用途又不按分区管的目录返回 nil。
func WorkspaceAreaOf(rel string) *WorkspaceAreaHint {
	rel = path.Clean(filepath.ToSlash(strings.TrimSpace(rel)))
	if rel == "." || rel == "" || strings.HasPrefix(rel, "../") || rel == ".." {
		return nil
	}
	if botDir, inKeep := keepLocation(rel); inKeep {
		return &WorkspaceAreaHint{Key: "keep", Label: workspaceKeepLabel, Retention: workspaceKeepRetention, BotID: botDir}
	}
	top, _, _ := strings.Cut(rel, "/")
	policy := DefaultWorkspaceCleanupPolicy
	for _, spec := range workspaceAgedAreaSpecs {
		if top == spec.dir {
			return &WorkspaceAreaHint{Key: spec.key, Label: spec.label, Retention: spec.retention(policy)}
		}
	}
	if workspaceReservedTopLevel[top] {
		return nil
	}
	return &WorkspaceAreaHint{Key: "other", Label: workspaceOtherLabel, Retention: workspaceOtherRetention}
}

// IsWorkspaceAreaDir 报告 rel 是不是某个分区的根目录（keep、downloads、outputs、tmp、
// .agent-browser、.trash）。概览里这些分区总是有卡片，目录还没建出来时点进去也该是
// 「还没有文件」，而不是找不到。
func IsWorkspaceAreaDir(rel string) bool {
	rel = path.Clean(filepath.ToSlash(strings.TrimSpace(rel)))
	if rel == WorkspaceKeepDir {
		return true
	}
	for _, spec := range workspaceAgedAreaSpecs {
		if rel == spec.dir {
			return true
		}
	}
	return false
}

// SummarizeWorkspace 按分区合计工作目录里的文件，给 WebUI 文件页顶部的概览卡片用。
func SummarizeWorkspace(root string, opts WorkspaceCleanupOptions) (WorkspaceOverview, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return WorkspaceOverview{}, err
	}
	overview := WorkspaceOverview{Root: abs, CollectedAt: now, Areas: []WorkspaceArea{}, Loose: []WorkspaceFileInfo{}, OrphanCoding: []WorkspaceFileInfo{}}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return overview, nil
	}
	policy := opts.Policy.withDefaults()
	protected := agentProtectedFiles(Config{WorkDir: abs})

	other := WorkspaceArea{Key: "other", Label: workspaceOtherLabel, Path: ".", Retention: workspaceOtherRetention}
	if entries, err := os.ReadDir(filepath.Join(abs, WorkspaceKeepDir)); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				// 直接落在 keep/ 下、不属于任何机器人的文件（命令或截图写进来的），归到「其他」。
				if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
					other.Bytes += info.Size()
					other.Files++
				}
				continue
			}
			area := WorkspaceArea{
				Key: "keep", Label: workspaceKeepLabel, Path: WorkspaceKeepDir + "/" + entry.Name(), BotID: entry.Name(),
				Retention: workspaceKeepRetention, QuotaBytes: KeepQuotaBytes,
			}
			sumWorkspaceTree(abs, area.Path, &area, protected)
			overview.Areas = append(overview.Areas, area)
		}
	}
	for _, spec := range workspaceAgedAreaSpecs {
		area := WorkspaceArea{Key: spec.key, Label: spec.label, Path: spec.dir, Retention: spec.retention(policy)}
		sumWorkspaceTree(abs, area.Path, &area, protected)
		overview.Areas = append(overview.Areas, area)
	}
	if entries, err := os.ReadDir(abs); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || workspaceReservedTopLevel[entry.Name()] || protected.blocked(filepath.Join(abs, entry.Name())) {
				continue
			}
			sumWorkspaceTree(abs, entry.Name(), &other, protected)
		}
	}
	overview.Areas = append(overview.Areas, other)
	if loose := looseWorkspaceFiles(abs, protected); loose != nil {
		overview.Loose = loose
	}
	if opts.CodingReferenced != nil {
		if idle := idleCodingWorkspaces(abs, now.Add(-policy.CodingIdleAge), opts.CodingReferenced); idle != nil {
			overview.OrphanCoding = idle
		}
	}
	return overview, nil
}

func retentionDays(age time.Duration) string {
	return strconv.Itoa(max(int(age/(24*time.Hour)), 1))
}

func retentionLabel(age time.Duration) string {
	return retentionDays(age) + " 天后自动清理"
}

// sumWorkspaceTree 把 rel 下的普通文件计进分区。软链接和凭据不算：前者可能指到
// 工作目录外面，后者本来就不该在界面上出现。
func sumWorkspaceTree(root, rel string, area *WorkspaceArea, protected protectedFiles) {
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
		area.Bytes += info.Size()
		area.Files++
		return nil
	})
}

// KeepEntriesIn 返回长期区某个目录（keep/<机器人>/ 及其子目录）下各条目在索引里的
// 记录，按相对路径索引。rel 不在某台机器人的长期区里时返回 nil。索引读不出来（还没有、
// 或者坏了）也返回 nil：说明只是锦上添花，不该挡住目录浏览。
func KeepEntriesIn(root, rel string) map[string]KeepEntry {
	botDir, inKeep := keepLocation(rel)
	if !inKeep || botDir == "" {
		return nil
	}
	entries, err := loadKeepIndexDir(root, botDir)
	if err != nil || len(entries) == 0 {
		return nil
	}
	out := make(map[string]KeepEntry, len(entries))
	for _, entry := range entries {
		out[entry.Path] = entry
	}
	return out
}
