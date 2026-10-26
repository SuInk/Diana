// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 主人的宿主机指令：「执行命令」和「清理 存储」。和 host_access 工具同一套权限：
// 只认主人、安全模式下停用；命令同样先过模型审核。

// ownerShellReplyRunes 是聊天里回显命令输出的上限，完整输出最多 64 KiB，聊天里放不下。
const ownerShellReplyRunes = 3000

func runOwnerStorageCleanup(r *Runtime, event MessageEvent, _ string) string {
	if r.effectiveConfigForEvent(event).agentSafeMode() {
		return agentSafeModeDisabledMessage
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return r.cleanupDianaStorage(ctx, time.Now()).summary()
}

func runOwnerShellCommand(r *Runtime, event MessageEvent, script string) string {
	script = strings.TrimSpace(script)
	if script == "" {
		return "用法：执行命令 <shell 命令>，例如：执行命令 df -h"
	}
	cfg := r.effectiveConfigForEvent(event)
	if cfg.agentSafeMode() {
		return agentSafeModeDisabledMessage
	}
	shell, args, err := ownerShell(script)
	if err != nil {
		return "无法执行：" + err.Error()
	}
	acfg := r.agentRegistryConfig(cfg, event, true)
	// 新部署第一次执行时工作区可能还没建出来，cwd 不存在会直接失败。
	if err := os.MkdirAll(acfg.WorkDir, 0o755); err != nil {
		return "无法准备工作目录：" + err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostCommandReviewTimeout+90*time.Second)
	defer cancel()
	// 指令原文就是授权：审核模型看到的用户原话是「执行命令 …」整句。
	out, _, err := r.runReviewedHostCommand(ctx, event, acfg, agent.OwnerCommand{Command: shell, Args: args, CWD: acfg.WorkDir}, "主人用「执行命令」指令直接输入的命令，按原文执行")
	if err != nil {
		return err.Error()
	}
	return formatOwnerShellResult(out)
}

// ownerShell 返回本机的 shell 和把一段脚本交给它的参数。
func ownerShell(script string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		shell := strings.TrimSpace(os.Getenv("ComSpec"))
		if shell == "" {
			shell = filepath.Join(firstNonEmpty(os.Getenv("SystemRoot"), `C:\Windows`), "System32", "cmd.exe")
		}
		return shell, []string{"/C", script}, nil
	}
	for _, shell := range []string{"/bin/sh", "/usr/bin/sh"} {
		if _, err := os.Stat(shell); err == nil {
			return shell, []string{"-c", script}, nil
		}
	}
	return "", nil, fmt.Errorf("找不到 /bin/sh")
}

func formatOwnerShellResult(raw string) string {
	var result struct {
		ExitCode  int    `json:"exit_code"`
		Output    string `json:"output"`
		Truncated bool   `json:"truncated"`
		TimedOut  bool   `json:"timed_out"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return truncateRunes(raw, ownerShellReplyRunes)
	}
	head := fmt.Sprintf("退出码 %d", result.ExitCode)
	if result.TimedOut {
		head += "（超过 60 秒已终止）"
	}
	output := strings.TrimRight(result.Output, "\n")
	if output == "" {
		output = "（无输出）"
	}
	if len([]rune(output)) > ownerShellReplyRunes || result.Truncated {
		output = truncateRunesPlain(output, ownerShellReplyRunes) + "\n…（输出过长已截断）"
	}
	return head + "\n" + output
}
