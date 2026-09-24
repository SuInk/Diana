// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package procgroup 让一次性子进程在超时或取消时连同它派生的孙进程一起结束。
//
// exec.CommandContext 默认只杀直接子进程。yt-dlp 的独立版是 PyInstaller 打包的，
// 真正干活的是它再起的 Python 进程；yt-dlp 合并音视频又会起 ffmpeg；git fetch/clone
// 会起 git-remote-https 或 ssh；run_command 白名单里的 go、npm、make 更是一串子进程。
// 只杀父进程时，这些孙进程继续跑、继续占 CPU 和网络；它们还握着父进程的输出管道，
// 于是 Wait 要等它们全部退出才返回——超时名义上到了，调用方实际还挂着。
//
// 这里做两件事：子进程放进自己的进程组，ctx 结束时整组杀掉（Windows 用 taskkill /T
// 递归杀进程树）；WaitDelay 兜底，杀完以后管道最多再等几秒就强制关掉，Wait 一定返回。
package procgroup

import (
	"context"
	"os/exec"
	"time"
)

// DefaultWaitDelay 是杀掉进程组以后等输出管道收尾的上限。整组都被 SIGKILL 了，
// 正常几毫秒内就能读到 EOF；给几秒只是为了把最后一段诊断输出读完整。
const DefaultWaitDelay = 3 * time.Second

// CommandContext 等同 exec.CommandContext，外加 Configure。
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return Configure(exec.CommandContext(ctx, name, args...))
}

// Configure 用于 exec.CommandContext 建的命令，在 Start 之前调用：子进程独占一个
// 进程组，ctx 结束时的 Cancel 改为杀整组，没设置 WaitDelay 的补上默认值。
// 已经设置过 Setsid 的（要脱离 Diana 存活的任务）保持原样：会话首进程本身就是
// 组长，Kill 同样能收到整组。
func Configure(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return nil
	}
	Isolate(cmd)
	cmd.Cancel = func() error { return Kill(cmd) }
	return cmd
}

// Isolate 用于没有 ctx 的长驻进程（stdio MCP 服务、常驻浏览器）：只放进独立进程组、
// 补上 WaitDelay，不装 Cancel（exec 不允许无 ctx 的命令带 Cancel）。调用方在不要它
// 的时候用 Kill 收整组。
func Isolate(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return nil
	}
	setProcessGroup(cmd)
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = DefaultWaitDelay
	}
	return cmd
}

// Kill 结束 cmd 所在的整个进程组。进程还没启动时什么也不做；整组都已经退出时
// 返回 os.ErrProcessDone，exec 据此把这次取消当作「进程自己先结束了」。
func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return killProcessGroup(cmd)
}
