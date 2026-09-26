// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// DianaStateDirName 是工作目录里放运行时自己状态的目录：扩展开关、对象名单、扩展
// 位置、长期保存区的索引。
//
// 以前这几份文件直接躺在工作目录根下（.extension-overrides.json 之类），和模型写的
// 文件混在一起。它们在凭据名单里，文件工具碰不到，但 macOS 的命令沙盒只对「目录」
// 挡写入，单个文件只挡了读——白名单里有 rm、mv 时，一条命令就能把开关文件删掉或
// 换成别的内容。收进一个目录后按目录整个挡：读、写都不放行，Linux 上整个换成空 tmpfs。
const DianaStateDirName = ".diana"

// workspaceStateFile 是一份搬进 .diana/ 的运行时状态：新名字在 .diana/ 下面，
// legacy 是老版本在工作目录根下用的名字。
type workspaceStateFile struct {
	name   string
	legacy string
}

var (
	extensionOverridesState = workspaceStateFile{name: "extension-overrides.json", legacy: ".extension-overrides.json"}
	extensionAudienceState  = workspaceStateFile{name: "extension-audience.json", legacy: ".extension-audience.json"}
	extensionPathsState     = workspaceStateFile{name: "extension-paths.json", legacy: ".extension-paths.json"}
	mcpPresetsHiddenState   = workspaceStateFile{name: "mcp-presets-hidden.json", legacy: ".mcp-presets-hidden.json"}
)

// legacyWorkspaceStateFiles 是老位置上的状态文件，搬走之前仍然要挡住。
var legacyWorkspaceStateFiles = []workspaceStateFile{extensionOverridesState, extensionAudienceState, extensionPathsState, mcpPresetsHiddenState}

// DianaStateDir 返回工作目录下的运行时状态目录。
func DianaStateDir(root string) string {
	return filepath.Join(root, DianaStateDirName)
}

func (f workspaceStateFile) path(root string) string {
	return filepath.Join(DianaStateDir(root), f.name)
}

func (f workspaceStateFile) legacyPath(root string) string {
	return filepath.Join(root, f.legacy)
}

// read 先读新位置，没有再读老位置。老位置的文件不在读的时候搬：读是高频路径，
// 几台机器人并发读时各自去挪文件只会互相踩；等下一次保存时写到新位置、再删掉旧的。
// 两处都没有时返回的错误满足 os.IsNotExist。
func (f workspaceStateFile) read(root string) ([]byte, error) {
	data, err := os.ReadFile(f.path(root))
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return data, err
	}
	return os.ReadFile(f.legacyPath(root))
}

// save 原子写到新位置，然后删掉老位置那份。删除失败不算保存失败：新位置优先读，
// 老文件留着也不会再生效。
func (f workspaceStateFile) save(root string, data []byte) error {
	if err := saveExtensionFile(f.path(root), data); err != nil {
		return err
	}
	_ = os.Remove(f.legacyPath(root))
	return nil
}

// MigrateWorkspaceState 把老版本留在工作目录根下的状态文件搬进 .diana/。启动时调
// 一次：不搬的话它们要等到下一次在 WebUI 里改开关才会挪走，这段时间里命令沙盒对
// 它们仍然只挡读不挡写。新位置已经有文件时以新的为准，只删掉老的。
func MigrateWorkspaceState(root string) error {
	if root == "" {
		return nil
	}
	var errs []error
	for _, file := range legacyWorkspaceStateFiles {
		lock := extensionPathLock(file.path(root))
		lock.Lock()
		err := migrateWorkspaceStateFile(root, file)
		lock.Unlock()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func migrateWorkspaceStateFile(root string, file workspaceStateFile) error {
	legacy := file.legacyPath(root)
	data, err := os.ReadFile(legacy)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(file.path(root)); err == nil {
		return os.Remove(legacy)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return file.save(root, data)
}
