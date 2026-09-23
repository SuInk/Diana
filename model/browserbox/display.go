// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ErrNoDisplay 是「有头要一块屏幕，这台机器上一块都凑不出来」：既没有现成的
// X/Wayland 会话，也没有 Xvfb 可以自己拉一块。
var ErrNoDisplay = errors.New("有头模式需要图形界面：当前环境既没有 DISPLAY/WAYLAND_DISPLAY，" +
	"也没装 Xvfb。完整版镜像自带 Xvfb；slim 镜像和自建的裸机部署要自己装（Debian/Ubuntu：" +
	"apt-get install -y xvfb，容器里加 docker exec -u root <容器名>），装完重开这个开关。" +
	"另一条路是干脆关掉「有头」——实时画面走 CDP screencast，无头一样看得见、也能接管")

// x11SocketDir 是 X 服务端放 socket 的地方。
const x11SocketDir = "/tmp/.X11-unix"

const (
	// xvfbStartTimeout 是等 Xvfb 报出显示号的时间。它只是开一块内存里的屏，
	// 正常在几十毫秒内就报回来，等不到基本就是起不来了。
	xvfbStartTimeout = 10 * time.Second
	// xvfbDepth 是色深。24 位是 Chromium 在虚拟显示上的常规档，16 位会让截图
	// 出现色带，32 位在 Xvfb 上并不真的多给一个通道。
	xvfbDepth = 24
)

// systemDisplayAvailable 判断有没有现成的图形会话可以用。只有 Linux 需要问这个
// 问题：macOS 和 Windows 的窗口系统跟着登录会话走，没有对应的环境变量可看，
// 在那里一律当作有。
func systemDisplayAvailable() bool {
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

// xvfbAvailable 判断能不能自己拉一块虚拟屏。
func xvfbAvailable() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := exec.LookPath("Xvfb")
	return err == nil
}

// checkHeadful 在启动和保存配置前挡住注定失败的有头模式。
func checkHeadful(settings Settings) error {
	if !settings.Headful || systemDisplayAvailable() || xvfbAvailable() {
		return nil
	}
	return ErrNoDisplay
}

// virtualDisplay 是我们自己拉起来的那块 Xvfb 屏。容器里没有显示器，有头模式就
// 靠它：Selenium Grid、Playwright 官方镜像和 Steel 的容器都是这个形状——浏览器
// 在虚拟显示里真开窗口，看画面仍然走 CDP screencast，不需要 VNC。
type virtualDisplay struct {
	cmd     *exec.Cmd
	display string
}

// startVirtualDisplay 拉起一块 width×height 的虚拟屏，返回它的显示号。
//
// 显示号交给 Xvfb 自己挑（-displayfd），不自己从 :99 往上试：并发起两个实例、
// 或者上次的锁文件没清干净时，自己挑号就会撞车，而撞车的表现是 Chromium 连上
// 了别人的屏。
func startVirtualDisplay(width, height int) (*virtualDisplay, error) {
	// X 的 socket 目录：镜像里不一定有，没有的话 Xvfb 起来了也没人连得上。
	if err := os.MkdirAll(x11SocketDir, 0o1777); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("建 X socket 目录失败：%w", err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("准备虚拟显示失败：%w", err)
	}
	defer reader.Close()

	screen := fmt.Sprintf("%dx%dx%d", width, height, xvfbDepth)
	// -nolisten tcp：这块屏只给同一个容器里的 Chromium 用，不对外开 X 端口。
	cmd := exec.Command("Xvfb", "-displayfd", "3", "-screen", "0", screen, "-nolisten", "tcp")
	cmd.ExtraFiles = []*os.File{writer}
	// Xvfb 起不来的理由只写在它自己的输出里（缺 mesa、权限不对、显示号被占），
	// 不带出来的话用户看到的只是一句「没有报出显示号」。
	diagnostics := &diagnosticTail{limit: 1024}
	cmd.Stderr = diagnostics
	if err := cmd.Start(); err != nil {
		writer.Close()
		return nil, fmt.Errorf("启动虚拟显示失败：%w", err)
	}
	// 写端留在父进程里的话，Xvfb 退出后这里的读也不会返回 EOF。
	writer.Close()

	number := make(chan string, 1)
	go func() {
		defer recoverGoroutinePanic("readDisplayNumber")
		buf := make([]byte, 32)
		n, err := reader.Read(buf)
		if err != nil || n == 0 {
			number <- ""
			return
		}
		number <- strings.TrimSpace(string(buf[:n]))
	}()

	select {
	case value := <-number:
		if _, err := strconv.Atoi(value); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, fmt.Errorf("虚拟显示起不来：%s", xvfbFailureReason(diagnostics))
		}
		return &virtualDisplay{cmd: cmd, display: ":" + value}, nil
	case <-time.After(xvfbStartTimeout):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("虚拟显示启动超时：%s", xvfbFailureReason(diagnostics))
	}
}

// xvfbFailureReason 从 Xvfb 的输出里取一句能照着查的话。
func xvfbFailureReason(diagnostics *diagnosticTail) string {
	tail := diagnostics.String()
	if tail == "" {
		return "它没有报出显示号，也没有留下任何输出"
	}
	if strings.Contains(tail, "libGL") || strings.Contains(tail, "libgallium") {
		// Xvfb 链着 mesa 的 libGL，被精简掉的镜像里会缺。补回来就能用。
		return tail + "（Xvfb 链着 mesa 的 libGL，这个环境里缺。Debian/Ubuntu 上装 libgl1 与 " +
			"libglx-mesa0 即可）"
	}
	return tail
}

// Stop 关掉这块屏。浏览器退出后不关的话，每次重启都会多留一个 Xvfb 进程。
func (d *virtualDisplay) Stop() {
	if d == nil || d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Kill()
	_ = d.cmd.Wait()
}

// Env 返回要补给浏览器进程的环境变量。
func (d *virtualDisplay) Env() []string {
	if d == nil {
		return nil
	}
	return []string{"DISPLAY=" + d.display}
}
