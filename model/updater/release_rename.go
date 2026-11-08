// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 程序改名的迁移。latest.json 的 install_as 和本机程序名不同时，新程序按新名字
// 落地，旧名字的位置留一个指向它的软链接（.app 里和 Windows 上不留，见下），能找到
// 的启动入口（一键安装生成的 start-installed.sh、.app 的 Info.plist、Windows 的
// diana.cmd）把旧名字改成新名字。这些改动都记进本次备份，健康检查不过就和程序文件
// 一起回滚。
//
// 软链接是给认不出来的启动方式兜底的：手动部署自己写的 start.sh、PATH 里的 diana
// 命令都直接点名旧文件。.app 里不留，是因为 bundle 的 MacOS 目录多一个软链接会让
// 整包签名校验失败；Windows 普通用户建不了软链接，硬链接又会在下次替换后指向旧版本。

// maxLauncherBytes 是启动脚本的大小上限，超过就不当作启动脚本改写。
const maxLauncherBytes = 64 << 10

// validInstallName 检查 install_as 能不能当作本平台的程序文件名。
func validInstallName(goos, name string) bool {
	if !safeReleaseFileName(name) || strings.TrimSpace(name) != name || strings.HasPrefix(name, ".") {
		return false
	}
	if goos == "windows" {
		return strings.EqualFold(filepath.Ext(name), ".exe")
	}
	return !strings.EqualFold(filepath.Ext(name), ".exe")
}

// renameLauncherCandidates 列出可能点名程序文件的启动入口。只认这几个固定位置：
// 计划文件里的路径必须落在这张表里，更新助手不会去改别的文件。
func renameLauncherCandidates(executable string) []string {
	if bundle := enclosingMacOSAppBundle(executable); bundle != "" {
		return []string{
			filepath.Join(bundle, "Contents", "Info.plist"),
			filepath.Join(filepath.Dir(bundle), "start-installed.sh"),
		}
	}
	root := filepath.Dir(executable)
	candidates := []string{filepath.Join(root, "start-installed.sh"), filepath.Join(root, "run.bat")}
	if runtime.GOOS == "windows" {
		if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
			candidates = append(candidates, filepath.Join(local, "Microsoft", "WindowsApps", "diana.cmd"))
		}
	}
	return candidates
}

// planRenameLaunchers 选出确实点名了旧程序的启动入口。装在 .app 里却找不到
// start-installed.sh 时拒绝改名：launchd 会照旧去启动一个已经不存在的文件。
func planRenameLaunchers(executable string) ([]string, error) {
	oldName := filepath.Base(executable)
	var launchers []string
	for _, path := range renameLauncherCandidates(executable) {
		content, ok := readLauncher(path)
		if ok && strings.Contains(content, oldName) {
			launchers = append(launchers, path)
		}
	}
	if bundle := enclosingMacOSAppBundle(executable); bundle != "" {
		if len(launchers) != 2 {
			return nil, fmt.Errorf("updater: this release renames the program but %s was not installed by the one-click installer; rerun the one-click installer", filepath.Base(bundle))
		}
	}
	return launchers, nil
}

func readLauncher(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxLauncherBytes {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func validateRenamePlan(plan releaseApplyPlan) error {
	if plan.RenamedExecutable == "" {
		if len(plan.Launchers) > 0 {
			return errors.New("updater: launcher rewrite without a rename")
		}
		return nil
	}
	if !filepath.IsAbs(plan.RenamedExecutable) || filepath.Dir(plan.RenamedExecutable) != filepath.Dir(plan.ExecutablePath) || plan.RenamedExecutable == plan.ExecutablePath {
		return errors.New("updater: renamed executable must sit beside the current one")
	}
	if !validInstallName(runtime.GOOS, filepath.Base(plan.RenamedExecutable)) {
		return errors.New("updater: invalid renamed executable name")
	}
	allowed := map[string]bool{}
	for _, path := range renameLauncherCandidates(plan.ExecutablePath) {
		allowed[path] = true
	}
	for _, path := range plan.Launchers {
		if !allowed[path] {
			return fmt.Errorf("updater: %s is not a known launcher", path)
		}
	}
	return nil
}

// applyRename 在新程序落地之后执行：旧程序挪进备份并留下兼容软链接，启动入口里的
// 旧名字换成新名字。返回的条目和程序文件一样能被 restoreReleaseFiles 还原。
func applyRename(plan releaseApplyPlan) ([]switchedReleaseFile, error) {
	var switched []switchedReleaseFile
	oldPath, newName := plan.ExecutablePath, filepath.Base(plan.RenamedExecutable)
	retired := filepath.Join(plan.BackupRoot, "package", "renamed-from", filepath.Base(oldPath))
	if err := os.MkdirAll(filepath.Dir(retired), 0o700); err != nil {
		return switched, err
	}
	if err := os.Rename(oldPath, retired); err != nil {
		return switched, fmt.Errorf("retire %s: %w", filepath.Base(oldPath), err)
	}
	switched = append(switched, switchedReleaseFile{target: oldPath, backup: retired, hadPrevious: true})
	if runtime.GOOS != "windows" && enclosingMacOSAppBundle(oldPath) == "" {
		if err := os.Symlink(newName, oldPath); err != nil {
			return switched, fmt.Errorf("link %s to %s: %w", filepath.Base(oldPath), newName, err)
		}
	}
	for index, path := range plan.Launchers {
		entry, err := rewriteLauncher(path, filepath.Join(plan.BackupRoot, "launchers", fmt.Sprintf("%d-%s", index, filepath.Base(path))), filepath.Base(oldPath), newName)
		if entry != nil {
			switched = append(switched, *entry)
		}
		if err != nil {
			return switched, err
		}
	}
	return switched, nil
}

func rewriteLauncher(path, backup, oldName, newName string) (*switchedReleaseFile, error) {
	content, ok := readLauncher(path)
	if !ok || !strings.Contains(content, oldName) {
		return nil, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return nil, err
	}
	if err := copyRegularFile(path, backup, info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("back up %s: %w", path, err)
	}
	entry := &switchedReleaseFile{target: path, backup: backup, hadPrevious: true}
	staged := path + ".diana-rename"
	if err := os.WriteFile(staged, []byte(strings.ReplaceAll(content, oldName, newName)), info.Mode().Perm()); err != nil {
		return entry, err
	}
	if err := os.Rename(staged, path); err != nil {
		_ = os.Remove(staged)
		return entry, fmt.Errorf("rewrite %s: %w", path, err)
	}
	return entry, nil
}

// resolveRenamedEntry 只解析程序文件本身这一层软链接（改名留下的兼容入口），
// 不动上级目录：安装目录经由软链接访问时，路径要和启动脚本里写的保持一致。
func resolveRenamedEntry(executable string) string {
	info, err := os.Lstat(executable)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return executable
	}
	target, err := os.Readlink(executable)
	if err != nil {
		return executable
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(executable), target)
	}
	return filepath.Clean(target)
}
