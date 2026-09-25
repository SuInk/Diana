// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// ManageFilesToolName 是整理工作目录文件的工具名。
const ManageFilesToolName = "manage_files"

const (
	// manageFilesCopyMaxBytes 是 copy 单个文件的上限。复制是在磁盘上凭空多出一份，
	// 工作目录又是几台机器人共用的，不给它一次复制出几个 G 的机会。
	manageFilesCopyMaxBytes = 256 << 20
	// manageFilesStatSniffBytes 是 stat 为了认类型、读图片尺寸读的文件开头。JPEG 的
	// 尺寸在 SOF 段里，前面可能压着几十 KB 的 EXIF，给足 1MB。
	manageFilesStatSniffBytes = 1 << 20
	// trashTimestampLayout 是回收站里每次删除的子目录名。
	trashTimestampLayout = "20060102-150405"
)

// ManageFilesTool 在工作目录内挪动、复制、删除文件，建目录，查看文件信息。
//
// 以前工作目录里只有「写一个文本文件」和「改一段文本」：存下来的东西没法改名、
// 没法归档，删也删不掉，只能越堆越多。
//
// 删除不是真删：挪进 .trash/<时间戳>/ 下，保留原来的相对路径。模型删错文件的代价
// 应该是「去回收站捞回来」，而不是「没了」。
type ManageFilesTool struct {
	root         string
	protected    protectedFiles
	writeEnabled bool
	now          func() time.Time
}

func (t *ManageFilesTool) Name() string { return ManageFilesToolName }

func (t *ManageFilesTool) Description() string {
	if !t.writeEnabled {
		return `查看 Agent 工作目录内文件或目录的信息：大小、按内容判断的真实类型、图片宽高、修改时间、是否目录。` +
			`文件写入没有打开，挪动、复制、删除、建目录都不可用。`
	}
	return `整理 Agent 工作目录内的文件：move 挪动或改名，copy 复制文件，delete 删除（挪进回收站 ` + WorkspaceTrashDir +
		`/<时间戳>/，不是真删），mkdir 建目录，stat 查看大小、按内容判断的真实类型、图片宽高、修改时间、是否目录。` +
		`目标已存在时默认拒绝，确认要覆盖才传 overwrite=true。只动工作目录内的相对路径，运行时配置和回收站内部都不能碰。`
}

func (t *ManageFilesTool) actions() []string {
	if !t.writeEnabled {
		return []string{"stat"}
	}
	return []string{"move", "copy", "delete", "mkdir", "stat"}
}

func (t *ManageFilesTool) InputSchema() map[string]any {
	properties := map[string]any{
		"action": toolEnumParam("要做的事", t.actions()...),
		"path":   toolStringParam("工作目录内的相对路径：move/copy 的来源，其他动作的对象"),
	}
	if t.writeEnabled {
		properties["to"] = toolStringParam("move/copy 的目标相对路径；指向已有目录时放进这个目录，文件名不变")
		properties["overwrite"] = toolBoolParam("目标文件已存在时是否覆盖，默认 false")
	}
	return toolObjectSchema([]string{"action", "path"}, properties)
}

func (t *ManageFilesTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	action := strings.ToLower(stringFromInput(input, "action"))
	rel := stringFromInput(input, "path")
	if rel == "" {
		return "", errors.New("path is required")
	}
	switch action {
	case "stat":
		return t.stat(rel)
	case "move", "copy", "delete", "mkdir":
		if !t.writeEnabled {
			return "", fmt.Errorf("文件写入没有打开（机器人配置里的「允许写入文件」），manage_files 现在只能 stat")
		}
	default:
		return "", fmt.Errorf("action 必须是 %s 之一", strings.Join(t.actions(), "、"))
	}
	switch action {
	case "mkdir":
		return t.mkdir(rel)
	case "delete":
		return t.delete(rel)
	default:
		to := stringFromInput(input, "to")
		if to == "" {
			return "", fmt.Errorf("%s 需要 to", action)
		}
		return t.transfer(action, rel, to, boolFromInput(input, "overwrite", false))
	}
}

// resolve 把相对路径过一遍 safePath、凭据名单和回收站检查，返回相对 root 的本地路径。
func (t *ManageFilesTool) resolve(rel string) (string, string, error) {
	target, err := safePath(t.root, rel)
	if err != nil {
		return "", "", err
	}
	clean := relPathForOutput(t.root, target)
	if t.protected.blocked(target) {
		return "", "", errProtectedFile(rel)
	}
	if isTrashPath(clean) {
		return "", "", errTrashPath(clean)
	}
	return target, clean, nil
}

// guardTree 挡住「整个目录挪走或删掉，顺带把里面的凭据一起带走」：凭据名单只按
// 路径匹配，目录本身不在名单里，里面的 coding-runtime/auth 却在。
func (t *ManageFilesTool) guardTree(target, clean string) error {
	if clean == "." {
		return errors.New("不能对整个工作目录这么做")
	}
	if t.protected.containsWithin(target) {
		return fmt.Errorf("%s 里面有 Diana 的运行时配置或凭据，不能整个挪走或删除", clean)
	}
	return nil
}

func (t *ManageFilesTool) openRoot() (*os.Root, error) {
	return os.OpenRoot(t.root)
}

func (t *ManageFilesTool) stat(rel string) (string, error) {
	target, clean, err := t.resolve(rel)
	if err != nil {
		return "", err
	}
	root, err := t.openRoot()
	if err != nil {
		return "", err
	}
	defer root.Close()
	local := filepath.FromSlash(clean)
	info, err := root.Stat(local)
	if err != nil {
		return "", missingFileError(t.root, clean, err)
	}
	result := map[string]any{
		"path":     clean,
		"is_dir":   info.IsDir(),
		"modified": info.ModTime().Format(time.RFC3339),
	}
	if info.IsDir() {
		entries, err := os.ReadDir(target)
		if err == nil {
			result["entries"] = len(entries)
		}
		return marshalToolResult(result)
	}
	result["size"] = info.Size()
	file, err := root.Open(local)
	if err != nil {
		return "", err
	}
	defer file.Close()
	head, err := io.ReadAll(io.LimitReader(file, manageFilesStatSniffBytes))
	if err != nil {
		return "", err
	}
	mediaType := SniffMediaType(head)
	result["mime"] = mediaType
	if strings.HasPrefix(mediaType, "image/") {
		if width, height, ok := ImageDimensions(head); ok {
			result["width"] = width
			result["height"] = height
		}
	}
	if suggested := CorrectFileExtension(path.Base(clean), mediaType); suggested != path.Base(clean) {
		// 扩展名和内容对不上时说出来：发图、看图都按内容认类型，名字错了只会误导人。
		result["extension_mismatch"] = fmt.Sprintf("内容是 %s，按内容应命名为 %s", mediaType, suggested)
	}
	return marshalToolResult(result)
}

func (t *ManageFilesTool) mkdir(rel string) (string, error) {
	_, clean, err := t.resolve(rel)
	if err != nil {
		return "", err
	}
	if clean == "." {
		return "", errors.New("工作目录本身已经存在")
	}
	root, err := t.openRoot()
	if err != nil {
		return "", err
	}
	defer root.Close()
	local := filepath.FromSlash(clean)
	existed := false
	if info, err := root.Stat(local); err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("%s 已经是一个文件", clean)
		}
		existed = true
	}
	if err := root.MkdirAll(local, 0o755); err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{"action": "mkdir", "path": clean, "existed": existed})
}

func (t *ManageFilesTool) delete(rel string) (string, error) {
	target, clean, err := t.resolve(rel)
	if err != nil {
		return "", err
	}
	if err := t.guardTree(target, clean); err != nil {
		return "", err
	}
	root, err := t.openRoot()
	if err != nil {
		return "", err
	}
	defer root.Close()
	local := filepath.FromSlash(clean)
	info, err := root.Lstat(local)
	if err != nil {
		return "", missingFileError(t.root, clean, err)
	}
	now := time.Now
	if t.now != nil {
		now = t.now
	}
	stamp := now().Format(trashTimestampLayout)
	var trashRel string
	for attempt := 1; ; attempt++ {
		dir := stamp
		if attempt > 1 {
			dir = fmt.Sprintf("%s-%d", stamp, attempt)
		}
		trashRel = path.Join(WorkspaceTrashDir, dir, clean)
		if _, err := root.Lstat(filepath.FromSlash(trashRel)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		if attempt >= 1000 {
			return "", errors.New("回收站里同一时刻的同名条目太多，稍后再试")
		}
	}
	if err := root.MkdirAll(filepath.Dir(filepath.FromSlash(trashRel)), 0o755); err != nil {
		return "", err
	}
	if err := root.Rename(local, filepath.FromSlash(trashRel)); err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{
		"action":     "delete",
		"path":       clean,
		"is_dir":     info.IsDir(),
		"trash_path": trashRel,
		"message":    "已移到回收站，不是永久删除；误删了可以从 trash_path 恢复。",
	})
}

func (t *ManageFilesTool) transfer(action, rel, to string, overwrite bool) (string, error) {
	source, sourceClean, err := t.resolve(rel)
	if err != nil {
		return "", err
	}
	if action == "move" {
		if err := t.guardTree(source, sourceClean); err != nil {
			return "", err
		}
	}
	root, err := t.openRoot()
	if err != nil {
		return "", err
	}
	defer root.Close()
	sourceLocal := filepath.FromSlash(sourceClean)
	sourceInfo, err := root.Lstat(sourceLocal)
	if err != nil {
		return "", missingFileError(t.root, sourceClean, err)
	}
	if action == "copy" && !sourceInfo.Mode().IsRegular() {
		return "", fmt.Errorf("copy 只复制普通文件，%s 不是", sourceClean)
	}
	_, destClean, err := t.resolve(to)
	if err != nil {
		return "", err
	}
	// 目标是已有目录：放进去，名字不变。
	if info, err := root.Stat(filepath.FromSlash(destClean)); err == nil && info.IsDir() {
		destClean = path.Join(destClean, path.Base(sourceClean))
		if _, destClean, err = t.resolve(destClean); err != nil {
			return "", err
		}
	}
	if destClean == sourceClean {
		return "", errors.New("来源和目标是同一个路径")
	}
	if sourceInfo.IsDir() && strings.HasPrefix(destClean+"/", sourceClean+"/") {
		return "", errors.New("不能把目录挪进它自己里面")
	}
	destLocal := filepath.FromSlash(destClean)
	overwrote := false
	if info, err := root.Lstat(destLocal); err == nil {
		if !overwrite {
			return "", fmt.Errorf("%s 已存在；确认要覆盖就传 overwrite=true，否则换个目标名", destClean)
		}
		if info.IsDir() {
			return "", fmt.Errorf("%s 是已有目录，不能被覆盖", destClean)
		}
		overwrote = true
	}
	if parent := filepath.Dir(destLocal); parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return "", err
		}
	}
	if action == "move" {
		if err := root.Rename(sourceLocal, destLocal); err != nil {
			return "", err
		}
		return marshalToolResult(map[string]any{"action": "move", "from": sourceClean, "to": destClean, "overwrote": overwrote})
	}
	if sourceInfo.Size() > manageFilesCopyMaxBytes {
		return "", fmt.Errorf("%s 有 %d MB，超过复制上限 %d MB", sourceClean, sourceInfo.Size()>>20, manageFilesCopyMaxBytes>>20)
	}
	written, err := copyWithinRoot(root, sourceLocal, destLocal, overwrite)
	if err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{"action": "copy", "from": sourceClean, "to": destClean, "bytes": written, "overwrote": overwrote})
}

func copyWithinRoot(root *os.Root, sourceLocal, destLocal string, overwrite bool) (int64, error) {
	in, err := root.Open(sourceLocal)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	out, err := root.OpenFile(destLocal, flags, 0o644)
	if err != nil {
		return 0, err
	}
	written, err := io.Copy(out, io.LimitReader(in, manageFilesCopyMaxBytes+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && written > manageFilesCopyMaxBytes {
		err = fmt.Errorf("文件超过复制上限 %d MB", manageFilesCopyMaxBytes>>20)
	}
	if err != nil {
		_ = root.Remove(destLocal)
		return 0, err
	}
	return written, nil
}

// missingFileError 在找不到文件时顺手列出同名不同扩展名的文件：按内容纠正过扩展名
// 的文件（.jfif 改成 .jpg、.png 其实是 .jpg）最容易被按旧名字找。
func missingFileError(root, clean string, err error) error {
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return WorkspaceMissingFileError(root, clean)
}

// WorkspaceMissingFileError 是「工作目录里找不到这个文件」的统一报错，附上同目录下
// 主文件名相同、扩展名不同的候选。
func WorkspaceMissingFileError(root, rel string) error {
	rel = path.Clean(filepath.ToSlash(rel))
	dir, base := path.Split(rel)
	stem := strings.TrimSuffix(base, path.Ext(base))
	var candidates []string
	if entries, readErr := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir))); readErr == nil && stem != "" {
		for _, entry := range entries {
			name := entry.Name()
			if name != base && strings.TrimSuffix(name, path.Ext(name)) == stem {
				candidates = append(candidates, path.Join(dir, name))
			}
			if len(candidates) >= 5 {
				break
			}
		}
	}
	// 包上 fs.ErrNotExist：调用方靠 errors.Is 区分「找不到」和别的读取失败。
	if len(candidates) > 0 {
		return fmt.Errorf("工作目录里没有 %s（%w）；同名的有 %s", rel, fs.ErrNotExist, strings.Join(candidates, "、"))
	}
	return fmt.Errorf("工作目录里没有 %s（%w）；不确定文件名或位置就用 find_files 按文件名查", rel, fs.ErrNotExist)
}
