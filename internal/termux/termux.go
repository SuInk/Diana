// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package termux 认出 Diana 是不是跑在安卓手机的 Termux 里。
//
// Termux 不是普通 Linux：没有 /tmp 和 /usr，软件都装在 $PREFIX（/data/data/com.termux/files/usr）
// 下面；安卓不给普通 App 开用户命名空间，Chromium 的进程沙盒起不来；Xvfb 虽然能装，
// 但它的 socket 目录写死在 /tmp/.X11-unix，Termux 里根本没有这个目录。浏览器相关的
// 代码靠这里的判断换一套做法。
//
// 在 Termux 里自己编译时 GOOS 是 android；用发布包的 linux/arm64 静态程序时 GOOS 是
// linux。两种都要认，所以不看 GOOS，看 Termux 给进程设的环境变量。
package termux

import (
	"os"
	"path/filepath"
	"strings"
)

// defaultPrefix 是 Termux 固定的安装前缀。环境变量被服务管理器洗掉时靠它兜底。
const defaultPrefix = "/data/data/com.termux/files/usr"

// Prefix 返回 Termux 的安装前缀；不在 Termux 里时返回空串。
func Prefix() string {
	if prefix := strings.TrimSpace(os.Getenv("PREFIX")); strings.Contains(prefix, "/com.termux/") {
		return prefix
	}
	// TERMUX_VERSION 只有 Termux App 起的会话才有；PREFIX 被改掉时还能凭它认出来。
	if strings.TrimSpace(os.Getenv("TERMUX_VERSION")) != "" {
		return defaultPrefix
	}
	if info, err := os.Stat(defaultPrefix); err == nil && info.IsDir() {
		return defaultPrefix
	}
	return ""
}

// Detected 判断是不是在 Termux 里。
func Detected() bool { return Prefix() != "" }

// ChromiumPaths 是 Termux 里 Chromium 可能装到的位置：tur-repo 的 chromium 包装的是
// chromium-browser，有的版本另带一个 chromium。
func ChromiumPaths() []string {
	prefix := Prefix()
	if prefix == "" {
		return nil
	}
	bin := filepath.Join(prefix, "bin")
	return []string{filepath.Join(bin, "chromium-browser"), filepath.Join(bin, "chromium")}
}
