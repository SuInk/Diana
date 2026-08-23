// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// macOS 按「代码签名身份」记住授权：麦克风、完全磁盘访问、App 管理这些开关都挂在
// 签名标识和它的 designated requirement 上。Diana 没有开发者账号，只能 ad-hoc 签名，
// 而 ad-hoc 的默认 DR 会把 cdhash 一起钉死——每次更新换一份新的 Mach-O，系统就认成
// 一个全新的程序：授权重新弹窗，「隐私与安全性」列表里堆出一排同名 Diana。
//
// 解决办法是每次都用同一个 identifier 重签，并把 DR 改写成只要求 identifier、不要求
// cdhash。这样新版本二进制照样满足旧授权的要求，系统里始终只有一条 Diana。
//
// 这些函数只在 macOS 上做事，其它平台直接返回 nil：Linux 和 Windows 没有这套机制。
const (
	// MacOSCodeIdentifier 必须和 packaging/macos/Info.plist 的 CFBundleIdentifier、
	// scripts/build-local-mac.sh 与 scripts/install.sh 里的签名标识完全一致。
	// 任何一处写歪，macOS 就会把它当成另一个程序重新登记。
	MacOSCodeIdentifier = "com.suink.diana"

	// macOSCodesignTimeout 给 codesign 的时间上限。它平时是毫秒级，卡住多半是
	// 环境异常；更新流程不能为它无限等待。
	macOSCodesignTimeout = 30 * time.Second
)

// macOSDesignatedRequirement 让签名只认 identifier。缺了它，ad-hoc 签名的 DR 会带上
// 当前二进制的 cdhash，下个版本就不再满足，授权随之作废。
func macOSDesignatedRequirement(identifier string) string {
	return fmt.Sprintf("=designated => identifier \"%s\"", identifier)
}

// resignMacOSPath 用固定 identifier 重新 ad-hoc 签名一个可执行文件或 .app。
// 找不到 codesign（例如没装命令行工具）时安静跳过：更新本身不该因为签不了名而失败，
// 代价只是这次授权可能要重新点一次。
func resignMacOSPath(path string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	codesign, err := exec.LookPath("codesign")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), macOSCodesignTimeout)
	defer cancel()
	args := []string{"--force", "--sign", "-", "--identifier", MacOSCodeIdentifier,
		"--requirements", macOSDesignatedRequirement(MacOSCodeIdentifier)}
	// .app 要连同内部的 helper 一起签，裸可执行文件不能带 --deep（会报错）。
	if strings.EqualFold(filepath.Ext(path), ".app") {
		args = append(args, "--deep")
	}
	args = append(args, path)
	output, err := exec.CommandContext(ctx, codesign, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign %s: %w: %s", filepath.Base(path), err, strings.TrimSpace(string(output)))
	}
	return nil
}

// enclosingMacOSAppBundle 返回可执行文件所属的 .app 路径；不在 .app 里就返回空。
// 更新完成后要重签的是整个 bundle，只签里面的可执行文件会让 bundle 签名失效。
func enclosingMacOSAppBundle(executable string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return ""
	}
	// 期望结构：<name>.app/Contents/MacOS/<executable>
	macOS := filepath.Dir(executable)
	if !strings.EqualFold(filepath.Base(macOS), "MacOS") {
		return ""
	}
	contents := filepath.Dir(macOS)
	if !strings.EqualFold(filepath.Base(contents), "Contents") {
		return ""
	}
	bundle := filepath.Dir(contents)
	if !strings.EqualFold(filepath.Ext(bundle), ".app") {
		return ""
	}
	return bundle
}

// restoreMacOSIdentity 在替换完文件之后恢复稳定身份：装在 .app 里就重签整个 bundle，
// 否则重签这一个可执行文件。返回错误只用于记录，调用方不应据此判定更新失败。
func restoreMacOSIdentity(executable string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	if bundle := enclosingMacOSAppBundle(executable); bundle != "" {
		return resignMacOSPath(bundle)
	}
	return resignMacOSPath(executable)
}
