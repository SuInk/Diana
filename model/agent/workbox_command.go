// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// BoxCommandDescription 是 run_command 在 work box 里的说明，对所有人同一份。
const BoxCommandDescription = `在工作区容器（Linux，/work 持久保存但有大小上限，可联网）里用 bash 运行命令：git clone、curl 下载、rg/grep 检索、python3/node 跑脚本、ffmpeg/imagemagick 处理文件都在这里做。` +
	`pip/npm/uv 装的包落在 /work/.home；聊天附件用 inputs 放进 /work/inbox；要发出去的文件交给 send_file 的 path（写 /work/... 路径）。` +
	`容器连不到本机和内网，只能访问公网；里面没有任何凭据。不用于网页搜索、计时、提醒和后台驻留，这些有专用工具。`

// BoxCommandSchema 是 run_command 的参数，照 Pi bash：command + timeout（秒）。
func BoxCommandSchema() map[string]any {
	return toolObjectSchema([]string{"command"}, map[string]any{
		"command": toolStringParam("bash 命令，在 /work 下执行"),
		"timeout": toolIntParam(fmt.Sprintf("超时秒数，默认 %d，最多 %d", int(BoxCommandDefaultTimeout/time.Second), int(BoxCommandMaxTimeout/time.Second))),
	})
}

// RunBoxCommand 在 box 里跑一条命令，结果照 #1568 的 Pi 规则分页：装不下给尾部整行，
// 写明一共多少行、给了哪几行、怎么续读。完整输出一直留在容器的 /work/.output/ 里，
// 宿主机只读回尾部窗口，命令刷出多少都不落宿主机磁盘。
func RunBoxCommand(ctx context.Context, b *WorkBox, command string, timeout time.Duration) (string, error) {
	if command == "" {
		return "", errors.New("command is required")
	}
	res, err := b.Exec(ctx, command, timeout)
	if err != nil {
		return "", err
	}
	if res.DiskFull != nil && res.DiskFull.Refused {
		body, _ := json.MarshalIndent(map[string]any{"disk_limit_exceeded": boxDiskFields(res.DiskFull), "notice": res.DiskFull.Message()}, "", "  ")
		return "", errors.New(string(body))
	}
	result := map[string]any{
		"exit_code":   res.ExitCode,
		"timed_out":   res.TimedOut,
		"duration_ms": res.Duration.Milliseconds(),
		"truncated":   false,
		"output":      "",
	}
	skeleton, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	budget, overhead := toolBudget(ctx), utf8.RuneCount(skeleton)+64
	tail, start, size, lines, err := b.OutputTail(ctx, res.OutputPath, outputTailWindow(budget, overhead))
	if err != nil {
		return "", err
	}
	avail := max(budget-overhead, 0)
	text := string(tail)
	page := commandOutputPage{Output: strings.ToValidUTF8(text, "\uFFFD")}
	if start > 0 || TextCost(text, true) > avail {
		howToRead := fmt.Sprintf("用 run_command 跑 sed -n '1,200p' %s 这类命令按行读，或用 rg 在里面查", res.OutputPath)
		page = pageOutputTail(text, start, size, lines, avail, true, res.OutputPath, howToRead)
	}
	result["output"], result["truncated"] = page.Output, page.Truncated
	if page.Truncated {
		result["notice"], result["full_output_path"] = page.Notice, page.FullOutputPath
	}
	if res.DiskFull != nil {
		// 运行中超限被停掉：输出照给，另外写明用了多少、上限多少、怎么清理。
		result["disk_limit_exceeded"] = boxDiskFields(res.DiskFull)
		result["disk_notice"] = res.DiskFull.Message()
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 || res.DiskFull != nil {
		return "", errors.New(string(body))
	}
	return string(body), nil
}

func boxDiskFields(u *BoxDiskUsage) map[string]any {
	return map[string]any{"used": FormatBoxBytes(u.UsedBytes), "limit": FormatBoxBytes(u.LimitBytes), "used_bytes": u.UsedBytes, "limit_bytes": u.LimitBytes}
}
