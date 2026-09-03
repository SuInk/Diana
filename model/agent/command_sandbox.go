// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// 命令沙盒模式。白名单挡的是「能跑哪个程序」，挡不住「这个程序能碰什么」——
// 放行了 cat，它照样读得到 ~/.ssh；放行了 curl，密钥就能被发出去。沙盒补的是
// 后半截：把文件写入限制在工作目录内，默认切断网络。
const (
	// CommandSandboxAuto 有沙盒工具就用，没有就照常执行并在启动日志里说明。
	CommandSandboxAuto = "auto"
	// CommandSandboxRequire 没有可用沙盒时直接拒绝执行命令。
	CommandSandboxRequire = "require"
	// CommandSandboxOff 完全不套沙盒。
	CommandSandboxOff = "off"
)

// commandSandbox 描述当前平台可用的沙盒实现。
type commandSandbox struct {
	// kind 是实现名，用于日志和工具输出：sandbox-exec、bubblewrap 或空串。
	kind string
	// wrap 把原始命令包装成沙盒命令。
	wrap func(ctx context.Context, root string, allowNetwork bool, name string, args []string) *exec.Cmd
}

// detectCommandSandbox 按平台挑选沙盒实现。macOS 用系统自带的 sandbox-exec，
// Linux 用 bubblewrap；两者都不需要 root，也不需要预先准备镜像。
func detectCommandSandbox() commandSandbox {
	switch runtime.GOOS {
	case "darwin":
		if path, err := exec.LookPath("sandbox-exec"); err == nil {
			return commandSandbox{kind: "sandbox-exec", wrap: wrapWithSandboxExec(path)}
		}
	case "linux":
		if path, err := exec.LookPath("bwrap"); err == nil {
			return commandSandbox{kind: "bubblewrap", wrap: wrapWithBubblewrap(path)}
		}
	}
	return commandSandbox{}
}

func (s commandSandbox) available() bool { return s.wrap != nil }

// sandboxExecProfile 生成 SBPL 策略：默认拒绝，只放开跑一个程序所必需的读取和
// 工作目录内的写入。读取保持宽松是有意的——动态链接、locale、证书散落在系统各处，
// 逐条放行会让常用命令直接跑不起来；真正的边界是「不能改」和「不能外发」。
func sandboxExecProfile(root string, allowNetwork bool) string {
	var builder strings.Builder
	builder.WriteString("(version 1)(deny default)")
	builder.WriteString("(allow process-exec)(allow process-fork)(allow signal (target self))")
	builder.WriteString("(allow sysctl-read)(allow mach-lookup)")
	builder.WriteString("(allow file-read*)")
	// 写入只开工作目录和临时目录；/dev/null 一类字符设备是命令的常规去处。
	for _, path := range sandboxWritableRoots(root) {
		builder.WriteString(fmt.Sprintf("(allow file-write* (subpath %s))", sbplString(path)))
	}
	builder.WriteString("(allow file-write* (subpath \"/private/tmp\") (subpath \"/private/var/tmp\") (subpath \"/tmp\"))")
	builder.WriteString("(allow file-write-data (literal \"/dev/null\") (literal \"/dev/stdout\") (literal \"/dev/stderr\"))")
	if allowNetwork {
		builder.WriteString("(allow network*)")
	}
	return builder.String()
}

// sandboxWritableRoots 返回工作目录需要写入放行的所有路径形态。macOS 上 /tmp 和
// /var 都是符号链接（真身在 /private 下），沙盒策略按解析后的真实路径匹配，只写
// 原始路径会让工作目录内的正常写入也被拒。两种形态都放行，取不到真实路径时至少
// 保留原始路径。
func sandboxWritableRoots(root string) []string {
	paths := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		paths = append(paths, resolved)
	}
	return paths
}

// sbplString 按 SBPL 的字面量规则转义路径。
func sbplString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(value) + `"`
}

func wrapWithSandboxExec(sandboxExecPath string) func(context.Context, string, bool, string, []string) *exec.Cmd {
	return func(ctx context.Context, root string, allowNetwork bool, name string, args []string) *exec.Cmd {
		full := append([]string{"-p", sandboxExecProfile(root, allowNetwork), name}, args...)
		return exec.CommandContext(ctx, sandboxExecPath, full...)
	}
}

// wrapWithBubblewrap 用挂载命名空间把根文件系统整体只读挂进来，只有工作目录和
// 临时目录可写；--unshare-net 直接摘掉网络协议栈，比按域名过滤更难绕过。
func wrapWithBubblewrap(bwrapPath string) func(context.Context, string, bool, string, []string) *exec.Cmd {
	return func(ctx context.Context, root string, allowNetwork bool, name string, args []string) *exec.Cmd {
		full := []string{
			"--ro-bind", "/", "/",
			"--dev", "/dev",
			"--proc", "/proc",
			"--tmpfs", "/tmp",
			"--bind", root, root,
			"--unshare-pid",
			"--unshare-ipc",
			"--unshare-uts",
			"--die-with-parent",
			"--new-session",
		}
		if !allowNetwork {
			full = append(full, "--unshare-net")
		}
		full = append(full, "--", name)
		full = append(full, args...)
		return exec.CommandContext(ctx, bwrapPath, full...)
	}
}

// normalizeCommandSandboxMode 把配置值归一到三个已知模式，未知值按最安全的
// auto 处理（有沙盒就用），而不是静默关掉。
func normalizeCommandSandboxMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CommandSandboxOff:
		return CommandSandboxOff
	case CommandSandboxRequire:
		return CommandSandboxRequire
	default:
		return CommandSandboxAuto
	}
}
