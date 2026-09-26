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
	keep         keepScope
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
		`目标已存在时默认拒绝，确认要覆盖才传 overwrite=true。只动工作目录内的相对路径，运行时配置和回收站内部都不能碰。` +
		`主人要把东西长期留着（存下来、留着、别过期、放持久目录）时 move 或 copy 到 ` + WorkspaceKeepDir + `/，会自动归到本机器人的长期保存区，并用 description 写一句这是什么；` +
		`长期区不会自动清理、每台机器人上限 ` + formatKeepBytes(KeepQuotaBytes) + `，别的机器人的长期区只能读。`
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
		properties["description"] = toolStringParam("move/copy 进长期保存区 " + WorkspaceKeepDir + "/ 时写一句这是什么（例如「群活动海报 9 月版」），会记进长期区索引，以后靠它认出这个文件")
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
		return t.transfer(action, rel, to, boolFromInput(input, "overwrite", false), stringFromInput(input, "description"))
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
	if botDir, inKeep := keepLocation(clean); inKeep && botDir != "" {
		result["area"] = "长期保存区，不会自动清理"
		if entries, err := loadKeepIndexDir(t.root, botDir); err == nil {
			for _, entry := range entries {
				if entry.Path == clean && entry.Description != "" {
					result["description"] = entry.Description
				}
			}
		}
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
	if clean, err = t.keep.normalizeDest(clean); err != nil {
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
	if err := t.keep.checkOwned(clean); err != nil {
		return "", err
	}
	now := time.Now
	if t.now != nil {
		now = t.now
	}
	trashRel, isDir, err := moveToTrash(t.root, clean, now())
	if err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{
		"action":     "delete",
		"path":       clean,
		"is_dir":     isDir,
		"trash_path": trashRel,
		"message":    "已移到回收站，不是永久删除；误删了可以从 trash_path 恢复。",
	})
}

// moveToTrash 把工作目录内已经校验过的相对路径 clean 挪进 .trash/<时间戳>/，保留原来的
// 相对路径；落在长期区里的，索引里的条目一并去掉。
func moveToTrash(workRoot, clean string, now time.Time) (string, bool, error) {
	return moveToTrashAs(workRoot, clean, clean, now)
}

// moveToTrashAs 和 moveToTrash 一样，但回收站里的位置和要清的长期区索引按 label 算：
// 经别名或大小写不同的写法删除时，label 是它在工作目录里的规范路径。
func moveToTrashAs(workRoot, clean, label string, now time.Time) (string, bool, error) {
	root, err := os.OpenRoot(workRoot)
	if err != nil {
		return "", false, err
	}
	defer root.Close()
	local := filepath.FromSlash(clean)
	info, err := root.Lstat(local)
	if err != nil {
		return "", false, missingFileError(workRoot, clean, err)
	}
	stamp := now.Format(trashTimestampLayout)
	var trashRel string
	for attempt := 1; ; attempt++ {
		dir := stamp
		if attempt > 1 {
			dir = fmt.Sprintf("%s-%d", stamp, attempt)
		}
		trashRel = path.Join(WorkspaceTrashDir, dir, label)
		if _, err := root.Lstat(filepath.FromSlash(trashRel)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		if attempt >= 1000 {
			return "", false, errors.New("回收站里同一时刻的同名条目太多，稍后再试")
		}
	}
	if err := root.MkdirAll(filepath.Dir(filepath.FromSlash(trashRel)), 0o755); err != nil {
		return "", false, err
	}
	if err := root.Rename(local, filepath.FromSlash(trashRel)); err != nil {
		return "", false, err
	}
	_, _ = removeKeepEntries(workRoot, label)
	return trashRel, info.IsDir(), nil
}

// TrashWorkspacePath 是 WebUI 删除工作目录文件的入口：和 manage_files delete 一样挪进
// 回收站、清掉长期区索引。操作的是管理员，凭据名单和「只能动本机器人长期区」都不套：
// 那份名单防的是模型把令牌打进聊天，管理员要删 .mcp.json 或 .diana/ 里的东西是他的事。
// 仍然不许动整个工作目录、长期区根目录和回收站里面；路径必须是工作目录内的相对路径，
// 最后一段是链接时挪走的是链接本身（经 os.Root 改名），中途经链接出了工作目录会被拒。
func TrashWorkspacePath(cfg Config, rel string, now time.Time) (string, error) {
	root, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		return "", err
	}
	clean := path.Clean(filepath.ToSlash(strings.TrimSpace(rel)))
	if clean == "." || clean == "" || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("%w（%s）", ErrWorkspacePath, strings.TrimSpace(rel))
	}
	if isTrashPath(clean) {
		return "", errTrashPath(clean)
	}
	canonical, err := guardAdminTrashTarget(root, clean)
	if err != nil {
		return "", err
	}
	// 回收站里的位置和长期区索引都按规范路径算：经别名或大小写不同的写法删掉的长期区
	// 文件，索引里不会留着指向已删除文件的条目，回收站里也看得出它原本在哪。
	trashRel, _, err := moveToTrashAs(root, clean, canonical, now)
	if err != nil {
		return "", adminTrashError(root, clean, err)
	}
	return trashRel, nil
}

// adminTrashError 把 os.Root 报的英文错误翻成管理员看得懂的话。中途经链接走出工作目录
// 时 os.Root 只说一句 path escapes from parent，页面上原样弹出来没人看得懂。
func adminTrashError(workRoot, clean string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return missingFileError(workRoot, clean, err)
	}
	if strings.Contains(err.Error(), "path escapes from parent") {
		return fmt.Errorf("%s 经符号链接到了工作目录外面：外面的东西这里只能看和下载，不能删除", clean)
	}
	return fmt.Errorf("没能把 %s 挪进回收站：%w", clean, err)
}

// guardAdminTrashTarget 挡住不能整个删掉的目录：工作目录本身、keep/、keep/<机器人>/、
// .trash/ 以及回收站里面的东西。只比字面不够：大小写不敏感的文件系统上 Keep 就是 keep，
// 工作目录里一个 loop -> . 的链接也能让 loop/keep 指到长期区根目录。所以经 os.Root 取
// 目标和它每一层上级的真实文件，用 os.SameFile 比。要挪的这一项自己是链接时，挪走的是
// 链接本身，不用比它指向哪里。
//
// 返回 clean 在工作目录里的规范路径（见 canonicalWorkspaceRel），给回收站位置和清索引用。
func guardAdminTrashTarget(workRoot, clean string) (string, error) {
	handle, err := os.OpenRoot(workRoot)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	info, err := handle.Lstat(filepath.FromSlash(clean))
	if err != nil {
		return "", adminTrashError(workRoot, clean, err)
	}
	rootInfo, err := handle.Stat(".")
	if err != nil {
		return "", err
	}
	keepInfo, keepErr := handle.Stat(WorkspaceKeepDir)
	trashInfo, trashErr := handle.Stat(WorkspaceTrashDir)
	same := func(a os.FileInfo, b os.FileInfo, bErr error) bool {
		return a != nil && bErr == nil && os.SameFile(a, b)
	}
	if info.Mode()&fs.ModeSymlink == 0 && info.IsDir() {
		switch {
		case os.SameFile(info, rootInfo):
			return "", errors.New("不能对整个工作目录这么做")
		case same(info, keepInfo, keepErr):
			return "", fmt.Errorf("%s 是长期保存区的根目录，不能整个删除", clean)
		case same(info, trashInfo, trashErr):
			return "", errTrashPath(clean)
		}
	}
	// 逐层往上看：落在回收站里就拒绝；上一层是 keep/ 而自己是目录，就是某台机器人的
	// 长期区根目录。
	for ancestor := path.Dir(clean); ancestor != "."; ancestor = path.Dir(ancestor) {
		dirInfo, err := handle.Stat(filepath.FromSlash(ancestor))
		if err != nil {
			continue
		}
		if same(dirInfo, trashInfo, trashErr) {
			return "", errTrashPath(clean)
		}
		if ancestor == path.Dir(clean) && same(dirInfo, keepInfo, keepErr) && info.Mode()&fs.ModeSymlink == 0 && info.IsDir() {
			return "", fmt.Errorf("%s 是一台机器人的长期保存区根目录，不能整个删除；里面的文件可以单独删", clean)
		}
	}
	return canonicalWorkspaceRel(handle, workRoot, clean), nil
}

// canonicalWorkspaceRel 返回 clean 在工作目录里的规范写法：上级目录解开符号链接、换成
// 相对工作目录的真实路径，每一段再按目录里实际的名字写（大小写不敏感的文件系统上
// KEEP 写回 keep）。最后一段是链接时保留链接自己的名字：挪走的是链接本身。
// 别名可能指到任意一层（ksub -> keep/<机器人>/sub），只认 keep/<机器人>/ 本身不够。
// 算不出来（解析失败、落到工作目录外面）时原样返回 clean。
func canonicalWorkspaceRel(handle *os.Root, workRoot, clean string) string {
	resolvedRoot, err := filepath.EvalSymlinks(workRoot)
	if err != nil {
		return clean
	}
	var parts []string
	if dir := path.Dir(clean); dir != "." {
		resolved, err := filepath.EvalSymlinks(filepath.Join(workRoot, filepath.FromSlash(dir)))
		if err != nil {
			return clean
		}
		rel, err := filepath.Rel(resolvedRoot, resolved)
		if err != nil || !filepath.IsLocal(rel) {
			return clean
		}
		if rel = filepath.ToSlash(rel); rel != "." {
			parts = strings.Split(rel, "/")
		}
	}
	parts = append(parts, path.Base(clean))
	current := "."
	for i, part := range parts {
		name := actualWorkspaceEntryName(handle, current, part, i == len(parts)-1)
		if name == "" {
			return clean
		}
		current = path.Join(current, name)
	}
	return current
}

// actualWorkspaceEntryName 在 dir 里找 name 对应的那一项实际叫什么：字面有就用字面，
// 否则找忽略大小写相同、而且确实是同一个文件的那一项。last 为 true 时不跟最后一段链接。
func actualWorkspaceEntryName(handle *os.Root, dir, name string, last bool) string {
	stat := handle.Stat
	if last {
		stat = handle.Lstat
	}
	want, err := stat(filepath.FromSlash(path.Join(dir, name)))
	if err != nil {
		return ""
	}
	file, err := handle.Open(filepath.FromSlash(dir))
	if err != nil {
		return ""
	}
	names, err := file.Readdirnames(-1)
	file.Close()
	if err != nil {
		return ""
	}
	for _, candidate := range names {
		if candidate == name {
			return candidate
		}
	}
	for _, candidate := range names {
		if !strings.EqualFold(candidate, name) {
			continue
		}
		if got, err := stat(filepath.FromSlash(path.Join(dir, candidate))); err == nil && os.SameFile(got, want) {
			return candidate
		}
	}
	return ""
}

func (t *ManageFilesTool) transfer(action, rel, to string, overwrite bool, description string) (string, error) {
	source, sourceClean, err := t.resolve(rel)
	if err != nil {
		return "", err
	}
	if action == "move" {
		if err := t.guardTree(source, sourceClean); err != nil {
			return "", err
		}
		if err := t.keep.checkOwned(sourceClean); err != nil {
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
	// keep/a.png 这种写法落到本机器人的长期区 keep/<机器人>/a.png。
	if destClean, err = t.keep.normalizeDest(destClean); err != nil {
		return "", err
	}
	if _, destClean, err = t.resolve(destClean); err != nil {
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
	var replacing int64
	if info, err := root.Lstat(destLocal); err == nil {
		if !overwrite {
			return "", fmt.Errorf("%s 已存在；确认要覆盖就传 overwrite=true，否则换个目标名", destClean)
		}
		if info.IsDir() {
			return "", fmt.Errorf("%s 是已有目录，不能被覆盖", destClean)
		}
		overwrote = true
		replacing = info.Size()
	}
	size, _ := pathSize(source)
	sourceKeep, sourceInKeep := keepLocation(sourceClean)
	destKeep, destInKeep := keepLocation(destClean)
	if destInKeep {
		// 在自己的长期区里挪来挪去不多占地方；从外面放进来（或复制一份）才算配额。
		lock := keepAreaLock(t.root, destKeep)
		lock.Lock()
		defer lock.Unlock()
		if action == "copy" || !sourceInKeep || sourceKeep != destKeep {
			if err := checkKeepQuota(t.root, destKeep, size, replacing); err != nil {
				return "", err
			}
		}
	}
	if parent := filepath.Dir(destLocal); parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return "", err
		}
	}
	now := time.Now
	if t.now != nil {
		now = t.now
	}
	meta := t.keep.meta(now(), description)
	result := map[string]any{"action": action, "from": sourceClean, "to": destClean, "overwrote": overwrote}
	if action == "move" {
		if err := root.Rename(sourceLocal, destLocal); err != nil {
			return "", err
		}
		if sourceInKeep || destInKeep {
			if err := moveKeepEntries(t.root, sourceClean, destClean, meta, size, sourceInfo.IsDir(), t.sniffMIME(destClean, sourceInfo)); err != nil {
				result["index_warning"] = "文件已挪好，但长期保存区索引没更新：" + err.Error()
			}
		}
		if destInKeep {
			result["area"] = "长期保存区，不会自动清理"
		}
		return marshalToolResult(result)
	}
	if sourceInfo.Size() > manageFilesCopyMaxBytes {
		return "", fmt.Errorf("%s 有 %d MB，超过复制上限 %d MB", sourceClean, sourceInfo.Size()>>20, manageFilesCopyMaxBytes>>20)
	}
	written, err := copyWithinRoot(root, sourceLocal, destLocal, overwrite)
	if err != nil {
		return "", err
	}
	result["bytes"] = written
	if destInKeep {
		entry := KeepEntry{Path: destClean, Description: description, SavedBy: meta.SavedBy, SavedAt: meta.Now, Size: written, MIME: t.sniffMIME(destClean, sourceInfo)}
		// 从长期区复制出来的副本沿用原件的说明和来源。
		if sourceInKeep && sourceKeep != "" {
			if entries, err := loadKeepIndexDir(t.root, sourceKeep); err == nil {
				for _, existing := range entries {
					if existing.Path == sourceClean {
						entry.SourceMessageID, entry.SourceURL = existing.SourceMessageID, existing.SourceURL
						if entry.Description == "" {
							entry.Description = existing.Description
						}
					}
				}
			}
		}
		if err := upsertKeepEntry(t.root, entry); err != nil {
			result["index_warning"] = "文件已复制，但长期保存区索引没更新：" + err.Error()
		}
		result["area"] = "长期保存区，不会自动清理"
	}
	return marshalToolResult(result)
}

// sniffMIME 读挪好的文件开头认类型，给长期区索引用；目录没有类型。
func (t *ManageFilesTool) sniffMIME(clean string, info os.FileInfo) string {
	if info.IsDir() {
		return ""
	}
	file, err := os.Open(filepath.Join(t.root, filepath.FromSlash(clean)))
	if err != nil {
		return ""
	}
	defer file.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	return SniffMediaType(head[:n])
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
