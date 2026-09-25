// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 工作目录是几台机器人共用的，以前没有任何分区：模型把文件随手丢在根下，主人说
// 「存到持久目录，别放临时目录」时根本没有这么个地方可去。现在分成几块：
//
//   - downloads/ outputs/ tmp/ 按天数自动清理（见 workspace_cleanup.go）；
//   - keep/<机器人>/ 是长期保存区，永不自动清理，删除只走 manage_files delete 进回收站。
//
// 长期区按机器人分目录、各有配额：它不会被自动清掉，就不能让一台机器人把盘写满。
// 超出配额时直接拒绝并说清楚，不悄悄挤掉旧文件——那些都是主人说过「留着」的东西。
//
// 每台机器人的长期区有一份索引（.diana/keep-index/<机器人>.json），记着每个文件是什么、
// 谁存的、从哪来。文件名说明不了「这是上周群里那张活动海报」，模型下次被问起时靠
// 这份索引认得出来；它挂在回复提示词尾部，见 assistant 包的 keepIndexPrompt。

const (
	// WorkspaceKeepDir 是长期保存区，下面按机器人分目录。
	WorkspaceKeepDir = "keep"
	// WorkspaceTmpDir 放草稿和中间文件，一天后清理。
	WorkspaceTmpDir = "tmp"
	// WorkspaceBrowserDir 是浏览器截图的默认目录。
	WorkspaceBrowserDir = ".agent-browser"
	// KeepQuotaBytes 是每台机器人长期保存区的容量上限。
	KeepQuotaBytes int64 = 2 << 30
	// keepIndexDirName 是 .diana/ 下放长期区索引的目录。
	keepIndexDirName = "keep-index"
	// keepDescriptionMaxRunes 限制说明的长度：它要进每一轮的提示词。
	keepDescriptionMaxRunes = 80
)

// KeepEntry 是长期保存区索引里的一条。
type KeepEntry struct {
	// Path 是工作目录内的相对路径，形如 keep/<机器人>/poster.png。
	Path            string    `json:"path"`
	Description     string    `json:"description,omitempty"`
	SourceMessageID string    `json:"source_message_id,omitempty"`
	SourceURL       string    `json:"source_url,omitempty"`
	SavedBy         string    `json:"saved_by,omitempty"`
	SavedAt         time.Time `json:"saved_at"`
	Size            int64     `json:"size"`
	MIME            string    `json:"mime,omitempty"`
	IsDir           bool      `json:"is_dir,omitempty"`
}

type keepIndexFile struct {
	Entries []KeepEntry `json:"entries"`
}

// KeepMeta 是写进长期区时要记进索引的信息。
type KeepMeta struct {
	// BotID 是机器人 ID（配置档 ID），决定落在哪个 keep/<机器人>/ 下。
	BotID           string
	Description     string
	SourceMessageID string
	SourceURL       string
	SavedBy         string
	Now             time.Time
}

// KeepBotDir 把机器人 ID 变成长期区下的目录名。ID 里只有字母、数字、点、下划线、
// 连字符时原样用，好认；有别的字符时替换掉，再挂一段哈希，免得两个 ID 撞成同一个目录。
func KeepBotDir(botID string) string {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return ""
	}
	var builder strings.Builder
	replaced := false
	for _, r := range botID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
			replaced = true
		}
	}
	name := strings.Trim(builder.String(), ".")
	if name == "" || replaced || len(name) > 64 {
		sum := sha256.Sum256([]byte(botID))
		if len(name) > 48 {
			name = name[:48]
		}
		name = strings.Trim(name, ".") + "-" + hex.EncodeToString(sum[:4])
		name = strings.TrimPrefix(name, "-")
	}
	return name
}

// KeepAreaPath 返回这台机器人长期区的相对路径，没有机器人 ID 时为空。
func KeepAreaPath(botID string) string {
	dir := KeepBotDir(botID)
	if dir == "" {
		return ""
	}
	return WorkspaceKeepDir + "/" + dir
}

// keepLocation 判断相对路径是否落在长期区：inKeep 为 true 时 botDir 是它所在的机器人
// 目录，路径正好是 keep/ 本身时 botDir 为空。
func keepLocation(rel string) (botDir string, inKeep bool) {
	rel = path.Clean(filepath.ToSlash(strings.TrimSpace(rel)))
	if rel == WorkspaceKeepDir {
		return "", true
	}
	rest, ok := strings.CutPrefix(rel, WorkspaceKeepDir+"/")
	if !ok {
		return "", false
	}
	botDir, _, _ = strings.Cut(rest, "/")
	return botDir, true
}

func keepIndexPath(root, botDir string) string {
	return filepath.Join(DianaStateDir(root), keepIndexDirName, botDir+".json")
}

func keepAreaLock(root, botDir string) interface {
	Lock()
	Unlock()
} {
	return extensionPathLock(filepath.Join(root, WorkspaceKeepDir, botDir))
}

// LoadKeepIndex 读取一台机器人的长期区索引，按保存时间从新到旧排。
func LoadKeepIndex(root, botID string) ([]KeepEntry, error) {
	botDir := KeepBotDir(botID)
	if botDir == "" {
		return nil, nil
	}
	return loadKeepIndexDir(root, botDir)
}

// LoadKeepIndexDir 和 LoadKeepIndex 一样，但直接按 keep/ 下的目录名读。
func LoadKeepIndexDir(root, botDir string) ([]KeepEntry, error) {
	return loadKeepIndexDir(root, botDir)
}

func loadKeepIndexDir(root, botDir string) ([]KeepEntry, error) {
	data, err := os.ReadFile(keepIndexPath(root, botDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file keepIndexFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("长期保存区索引 %s 损坏：%w", botDir, err)
	}
	sortKeepEntries(file.Entries)
	return file.Entries, nil
}

func sortKeepEntries(entries []KeepEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].SavedAt.Equal(entries[j].SavedAt) {
			return entries[i].SavedAt.After(entries[j].SavedAt)
		}
		return entries[i].Path < entries[j].Path
	})
}

// updateKeepIndex 在锁里读出索引、交给 fn 改、原子写回。
func updateKeepIndex(root, botDir string, fn func([]KeepEntry) []KeepEntry) error {
	if botDir == "" {
		return nil
	}
	file := keepIndexPath(root, botDir)
	lock := extensionPathLock(file)
	lock.Lock()
	defer lock.Unlock()
	entries, err := loadKeepIndexDir(root, botDir)
	if err != nil {
		// 索引坏了不能挡住文件操作：从空索引重建，坏的那份留个备份。
		_ = os.Rename(file, file+".broken")
		entries = nil
	}
	entries = fn(entries)
	sortKeepEntries(entries)
	if entries == nil {
		entries = []KeepEntry{}
	}
	data, err := json.MarshalIndent(keepIndexFile{Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	return saveExtensionFile(file, data)
}

// upsertKeepEntry 登记或更新一条。更新时没带的字段保留原值：挪动、改写一个文件
// 不该把当初存它时记下的说明和来源弄丢。
func upsertKeepEntry(root string, entry KeepEntry) error {
	botDir, inKeep := keepLocation(entry.Path)
	if !inKeep || botDir == "" {
		return nil
	}
	entry.Description = trimKeepDescription(entry.Description)
	return updateKeepIndex(root, botDir, func(entries []KeepEntry) []KeepEntry {
		for i := range entries {
			if entries[i].Path != entry.Path {
				continue
			}
			merged := entries[i]
			if entry.Description != "" {
				merged.Description = entry.Description
			}
			if entry.SourceMessageID != "" {
				merged.SourceMessageID = entry.SourceMessageID
			}
			if entry.SourceURL != "" {
				merged.SourceURL = entry.SourceURL
			}
			if entry.SavedBy != "" {
				merged.SavedBy = entry.SavedBy
			}
			if entry.MIME != "" {
				merged.MIME = entry.MIME
			}
			merged.Size = entry.Size
			merged.IsDir = entry.IsDir
			if !entry.SavedAt.IsZero() {
				merged.SavedAt = entry.SavedAt
			}
			entries[i] = merged
			return entries
		}
		if entry.SavedAt.IsZero() {
			entry.SavedAt = time.Now()
		}
		return append(entries, entry)
	})
}

// removeKeepEntries 去掉 rel 本身和它下面的所有条目，返回被去掉的那些。
func removeKeepEntries(root, rel string) ([]KeepEntry, error) {
	botDir, inKeep := keepLocation(rel)
	if !inKeep || botDir == "" {
		return nil, nil
	}
	rel = path.Clean(filepath.ToSlash(rel))
	var removed []KeepEntry
	err := updateKeepIndex(root, botDir, func(entries []KeepEntry) []KeepEntry {
		kept := entries[:0]
		for _, entry := range entries {
			if entry.Path == rel || strings.HasPrefix(entry.Path, rel+"/") {
				removed = append(removed, entry)
				continue
			}
			kept = append(kept, entry)
		}
		return kept
	})
	return removed, err
}

// moveKeepEntries 让索引跟着一次挪动走：从长期区挪出去的条目去掉，在长期区里改名
// 的条目换成新路径，从外面挪进长期区的登记一条新的。
func moveKeepEntries(root, from, to string, meta KeepMeta, size int64, isDir bool, mime string) error {
	from = path.Clean(filepath.ToSlash(from))
	to = path.Clean(filepath.ToSlash(to))
	removed, err := removeKeepEntries(root, from)
	if err != nil {
		return err
	}
	if _, inKeep := keepLocation(to); !inKeep {
		return nil
	}
	now := meta.Now
	if now.IsZero() {
		now = time.Now()
	}
	if len(removed) == 0 {
		return upsertKeepEntry(root, KeepEntry{
			Path: to, Description: meta.Description, SourceMessageID: meta.SourceMessageID, SourceURL: meta.SourceURL,
			SavedBy: meta.SavedBy, SavedAt: now, Size: size, MIME: mime, IsDir: isDir,
		})
	}
	var errs []error
	for _, entry := range removed {
		entry.Path = to + strings.TrimPrefix(entry.Path, from)
		if entry.Path == to && meta.Description != "" {
			entry.Description = meta.Description
		}
		errs = append(errs, upsertKeepEntry(root, entry))
	}
	return errors.Join(errs...)
}

func trimKeepDescription(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > keepDescriptionMaxRunes {
		value = string(runes[:keepDescriptionMaxRunes]) + "…"
	}
	return value
}

// keepUsage 统计一台机器人长期区已经占了多少字节。软链接不算：它不占这里的空间。
func keepUsage(root, botDir string) int64 {
	var total int64
	_ = filepath.WalkDir(filepath.Join(root, WorkspaceKeepDir, botDir), func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// KeepUsage 返回一台机器人长期区的已用字节数。
func KeepUsage(root, botID string) int64 {
	botDir := KeepBotDir(botID)
	if botDir == "" {
		return 0
	}
	return keepUsage(root, botDir)
}

// checkKeepQuota 在往长期区放 adding 字节（其中 replacing 字节是被覆盖掉的旧文件）之前
// 检查配额。
func checkKeepQuota(root, botDir string, adding, replacing int64) error {
	used := keepUsage(root, botDir)
	if used-replacing+adding <= KeepQuotaBytes {
		return nil
	}
	return fmt.Errorf("长期保存区 %s/%s/ 已用 %s，再放 %s 会超过每台机器人 %s 的上限，这次没有保存。"+
		"先用 manage_files delete 删掉不再需要的长期文件（会进回收站），或者改存到 %s/、%s/ 这些会自动清理的目录",
		WorkspaceKeepDir, botDir, formatKeepBytes(used), formatKeepBytes(adding), formatKeepBytes(KeepQuotaBytes),
		WorkspaceDownloadsDir, WorkspaceOutputsDir)
}

func formatKeepBytes(value int64) string {
	switch {
	case value >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(value)/(1<<30))
	case value >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(value)/(1<<20))
	case value >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(value)/(1<<10))
	default:
		return fmt.Sprintf("%d B", value)
	}
}

// keepScope 是一张工具表所属机器人在长期区里的身份：只能往自己的 keep/<机器人>/ 里
// 放东西、挪动和删除，别的机器人的长期区只读。
type keepScope struct {
	botDir string
	actor  string
}

func newKeepScope(cfg Config) keepScope {
	return keepScope{botDir: KeepBotDir(cfg.WorkspaceBotID), actor: strings.TrimSpace(cfg.WorkspaceActorID)}
}

// normalizeDest 把写入目标里的 keep/ 落到本机器人的目录下：模型常写 keep/a.png，
// 这里补成 keep/<机器人>/a.png；写成别的机器人的目录也一样收进自己名下，不去动别人的。
func (s keepScope) normalizeDest(clean string) (string, error) {
	botDir, inKeep := keepLocation(clean)
	if !inKeep {
		return clean, nil
	}
	if s.botDir == "" {
		return "", errors.New("这台机器人没有 ID，用不了长期保存区 keep/；存到 downloads/ 或 outputs/")
	}
	if botDir == s.botDir {
		return path.Clean(clean), nil
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(path.Clean(clean), WorkspaceKeepDir), "/")
	if rest == "" {
		return WorkspaceKeepDir + "/" + s.botDir, nil
	}
	return WorkspaceKeepDir + "/" + s.botDir + "/" + rest, nil
}

// checkOwned 挡住挪动、改写、删除别的机器人长期区里的东西，以及 keep/ 本身。
func (s keepScope) checkOwned(clean string) error {
	botDir, inKeep := keepLocation(clean)
	if !inKeep {
		return nil
	}
	if botDir == "" || path.Clean(clean) == WorkspaceKeepDir+"/"+botDir {
		return fmt.Errorf("%s 是长期保存区的根目录，不能整个挪走或删除；要删里面的文件就指定具体路径", clean)
	}
	if botDir != s.botDir {
		return fmt.Errorf("%s 在别的机器人的长期保存区里，只能读，不能挪动、改写或删除", clean)
	}
	return nil
}

func (s keepScope) meta(now time.Time, description string) KeepMeta {
	return KeepMeta{BotID: s.botDir, SavedBy: s.actor, Description: description, Now: now}
}

// pathSize 返回文件大小，目录则是下面所有普通文件的总和。
func pathSize(target string) (int64, bool) {
	info, err := os.Lstat(target)
	if err != nil {
		return 0, false
	}
	if !info.IsDir() {
		return info.Size(), false
	}
	var total int64
	_ = filepath.WalkDir(target, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total, true
}
