// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"errors"

	"github.com/SuInk/diana/internal/xvfb"
)

// ErrNoDisplay 是「有头要一块屏幕，这台机器上一块都凑不出来」：既没有现成的
// X/Wayland 会话，也没有 Xvfb 可以自己拉一块。
var ErrNoDisplay = errors.New("有头模式需要图形界面：当前环境既没有 DISPLAY/WAYLAND_DISPLAY，" +
	"也没装 Xvfb。完整版镜像自带 Xvfb；slim 镜像和自建的裸机部署要自己装（Debian/Ubuntu：" +
	"apt-get install -y xvfb，容器里加 docker exec -u root <容器名>），装完重开这个开关。" +
	"另一条路是干脆关掉「有头」——实时画面走 CDP screencast，无头一样看得见、也能接管")

// Xvfb 相关的实现在 internal/xvfb，网页渲染也用它开有头 Chrome。这里留几个名字，
// 内置浏览器其余的代码照旧调用。
type virtualDisplay = xvfb.Display

func systemDisplayAvailable() bool { return xvfb.SystemDisplayAvailable() }

func xvfbAvailable() bool { return xvfb.Available() }

func startVirtualDisplay(width, height int) (*virtualDisplay, error) {
	return xvfb.Start(width, height)
}

// DisplayStatus 回答「开真窗口有没有屏幕可用」，给浏览器页的运行依赖用：现成的图形
// 会话，或者能自己拉起的 Xvfb，有一个就行。
func DisplayStatus() (available bool, detail string) {
	switch {
	case systemDisplayAvailable():
		return true, "图形会话"
	case xvfbAvailable():
		return true, "Xvfb 虚拟屏"
	}
	return false, "没有显示器也没有 Xvfb：只能无头运行，实时画面照常。要开真窗口请装 xvfb"
}

// checkHeadful 在启动和保存配置前挡住注定失败的有头模式。
func checkHeadful(settings Settings) error {
	if !settings.Headful || systemDisplayAvailable() || xvfbAvailable() {
		return nil
	}
	return ErrNoDisplay
}
