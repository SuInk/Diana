// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"errors"
	"os"
	"runtime"
	"strings"
)

// ErrNoDisplay 是「这台机器没有图形界面，有头起不来」。Start 之前就判得出来，
// 不用等 Chrome 自己打印 Missing X server 再从一堆日志里认它。
var ErrNoDisplay = errors.New("有头模式需要图形界面，但当前环境没有显示器：" +
	"官方 Docker 镜像里没有 X server，也没有虚拟显示。关掉「有头」即可——" +
	"实时画面走 CDP screencast，无头一样看得见、也能接管")

// displayAvailable 判断有头模式能不能起来。只有 Linux 需要问这个问题：
// macOS 和 Windows 的窗口系统跟着登录会话走，没有对应的环境变量可看，
// 在那里一律放行，真起不来时仍然由进程输出兜底。
func displayAvailable() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	for _, key := range []string{"DISPLAY", "WAYLAND_DISPLAY"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

// checkHeadful 在启动和保存配置前挡住注定失败的有头模式。
func checkHeadful(settings Settings) error {
	if !settings.Headful || displayAvailable() {
		return nil
	}
	return ErrNoDisplay
}
