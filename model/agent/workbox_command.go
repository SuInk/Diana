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

// BoxCommandDescription 是 run_command 在 work box 里的说明，对所有人同一份。工具目录里
// 只放得下开头一百来字，所以「当前目录就是工作区、存的文件就在这里」写在最前面：模型
// 常在同一段脚本里先存附件再跑命令，还没看到保存结果就要写路径。
const BoxCommandDescription = `在本会话的工作区目录里用 bash 运行命令（工作区容器，Linux，可联网）；save_to_workspace、write_file 存的文件就在这里，路径写相对路径（如 downloads/a.mp4）。` +
	`git clone、curl 下载、rg/grep 检索、python3/node 跑脚本、ffmpeg/imagemagick 处理文件都在这里做；命令产出的文件直接交给 send_file、render_media 的 path。` +
	`目录持久保存但有大小上限（结果里的 cwd），别的会话看不到；pip/npm/uv 装的包落在 .home。` +
	`容器连不到本机和内网，只能访问公网；里面没有任何凭据。不用于网页搜索、计时、提醒和后台驻留，这些有专用工具。`

// BoxCommandSchema 是 run_command 的参数，照 Pi bash：command + timeout（秒）。
func BoxCommandSchema() map[string]any {
	return toolObjectSchema([]string{"command"}, map[string]any{
		"command": toolStringParam("bash 命令，在本会话的工作区目录里执行"),
		"timeout": toolIntParam(fmt.Sprintf("超时秒数，默认 %d，最多 %d", int(BoxCommandDefaultTimeout/time.Second), int(BoxCommandMaxTimeout/time.Second))),
	})
}

// RunBoxCommand 在 box 里跑一条命令，结果照 #1568 的 Pi 规则分页：装不下给尾部整行，
// 写明一共多少行、给了哪几行、怎么续读。完整输出一直留在范围目录的 .output/ 里，
// 宿主机只读回尾部窗口，命令刷出多少都不落宿主机磁盘。
func RunBoxCommand(ctx context.Context, b *WorkBox, command string, timeout time.Duration) (string, error) {
	if command == "" {
		return "", errors.New("command is required")
	}
	res, err := b.Exec(ctx, scopeLegacyWorkPaths(command, b.Root()), timeout)
	if err != nil {
		return "", err
	}
	if res.DiskFull != nil && res.DiskFull.Refused {
		body, _ := json.MarshalIndent(map[string]any{"disk_limit_exceeded": boxDiskFields(res.DiskFull), "notice": res.DiskFull.Message()}, "", "  ")
		return "", errors.New(string(body))
	}
	result := map[string]any{
		// cwd 让模型每次都看见自己的工作区目录在哪：一个容器里各会话的目录不同。
		"cwd":         b.Root(),
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

// scopeLegacyWorkPaths 把命令里的 /work、/work/<路径> 换成本会话的范围目录。以前每个
// 会话的根就是 /work，模型也习惯写 /work/downloads/a.mp4；现在 /work 下面只有各范围的
// 目录、而且列不出也进不去别人的，这种写法在命令里只会「找不到文件」。文件工具的 path
// 早就这样映射（WorkBox.ResolvePath），命令里照做。已经写成 /work/<范围>/… 的不动，
// 前面紧挨着路径字符的（比如 /home/x/work/a）不是这个 /work，也不动。
func scopeLegacyWorkPaths(command, root string) string {
	const prefix = BoxWorkDir
	var out strings.Builder
	rest := command
	for {
		index := strings.Index(rest, prefix)
		if index < 0 {
			out.WriteString(rest)
			return out.String()
		}
		end := index + len(prefix)
		before := byte(0)
		if index > 0 {
			before = rest[index-1]
		} else if out.Len() > 0 {
			before = out.String()[out.Len()-1]
		}
		after := byte(0)
		if end < len(rest) {
			after = rest[end]
		}
		standalone := !isBoxPathByte(before) && (after == 0 || after == '/' || !isBoxPathByte(after))
		scoped := strings.HasPrefix(rest[end:], "/s-")
		out.WriteString(rest[:index])
		if standalone && !scoped {
			out.WriteString(root)
		} else {
			out.WriteString(prefix)
		}
		rest = rest[end:]
	}
}

// isBoxPathByte 报告 c 能不能出现在路径名里（字母数字和 . _ - ~ /）。
func isBoxPathByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("._-~/", c) >= 0
}
