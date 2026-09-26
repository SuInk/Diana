// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package xvfb 给有头 Chrome 找一块屏幕：现成的图形会话，或者自己拉起的 Xvfb 虚拟屏。
//
// 内置浏览器（model/browserbox）和网页渲染（model/agent）都要开有头 Chrome：无头模式
// 在渲染、GPU、屏幕尺寸这些细节上会被网站认出来，搜索引擎和不少站点因此拦截或返回
// 空结果。容器里没有显示器，有头就靠这块虚拟屏：Selenium Grid、Playwright 官方镜像
// 和 Steel 的容器都是这个形状——浏览器在虚拟显示里真开窗口，看不见，也不需要 VNC。
package xvfb

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// x11SocketDir 是 X 服务端放 socket 的地方。
const x11SocketDir = "/tmp/.X11-unix"

const (
	// startTimeout 是等 Xvfb 报出显示号的时间。它只是开一块内存里的屏，正常在几十
	// 毫秒内就报回来，等不到基本就是起不来了。
	startTimeout = 10 * time.Second
	// depth 是色深。24 位是 Chromium 在虚拟显示上的常规档，16 位会让截图出现色带，
	// 32 位在 Xvfb 上并不真的多给一个通道。
	depth = 24
)

// SystemDisplayAvailable 判断有没有现成的图形会话可以用。只有 Linux 需要问这个
// 问题：macOS 和 Windows 的窗口系统跟着登录会话走，没有对应的环境变量可看，
// 在那里一律当作有。
func SystemDisplayAvailable() bool {
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

// Available 判断能不能自己拉一块虚拟屏。
func Available() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := exec.LookPath("Xvfb")
	return err == nil
}

// Display 是自己拉起来的一块 Xvfb 屏。方法对 nil 安全：用现成图形会话时没有它。
type Display struct {
	cmd    *exec.Cmd
	name   string
	exited chan struct{}
}

// Start 拉起一块 width×height 的虚拟屏。
//
// 显示号交给 Xvfb 自己挑（-displayfd），不自己从 :99 往上试：并发起两个实例、
// 或者上次的锁文件没清干净时，自己挑号就会撞车，而撞车的表现是 Chromium 连上
// 了别人的屏。
func Start(width, height int) (*Display, error) {
	// X 的 socket 目录：镜像里不一定有，没有的话 Xvfb 起来了也没人连得上。
	if err := os.MkdirAll(x11SocketDir, 0o1777); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("建 X socket 目录失败：%w", err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("准备虚拟显示失败：%w", err)
	}
	defer reader.Close()
	screen := fmt.Sprintf("%dx%dx%d", width, height, depth)
	// -nolisten tcp：这块屏只给同一台机器上的 Chromium 用，不对外开 X 端口。
	cmd := exec.Command("Xvfb", "-displayfd", "3", "-screen", "0", screen, "-nolisten", "tcp")
	cmd.ExtraFiles = []*os.File{writer}
	// Xvfb 起不来的理由只写在它自己的输出里（缺 mesa、权限不对、显示号被占），
	// 不带出来的话用户看到的只是一句「没有报出显示号」。
	diagnostics := &tail{limit: 1024}
	cmd.Stderr = diagnostics
	if err := cmd.Start(); err != nil {
		writer.Close()
		return nil, fmt.Errorf("启动虚拟显示失败：%w", err)
	}
	// 写端留在父进程里的话，Xvfb 退出后这里的读也不会返回 EOF。
	writer.Close()
	exited := make(chan struct{})
	go func() {
		defer recoverGoroutinePanic("wait")
		defer close(exited)
		_ = cmd.Wait()
	}()

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
			<-exited
			return nil, fmt.Errorf("虚拟显示起不来：%s", failureReason(diagnostics))
		}
		return &Display{cmd: cmd, name: ":" + value, exited: exited}, nil
	case <-time.After(startTimeout):
		_ = cmd.Process.Kill()
		<-exited
		return nil, fmt.Errorf("虚拟显示启动超时：%s", failureReason(diagnostics))
	}
}

// failureReason 从 Xvfb 的输出里取一句能照着查的话。
func failureReason(diagnostics *tail) string {
	text := diagnostics.String()
	if text == "" {
		return "它没有报出显示号，也没有留下任何输出"
	}
	if strings.Contains(text, "libGL") || strings.Contains(text, "libgallium") {
		// Xvfb 链着 mesa 的 libGL，被精简掉的镜像里会缺。补回来就能用。
		return text + "（Xvfb 链着 mesa 的 libGL，这个环境里缺。Debian/Ubuntu 上装 libgl1 与 " +
			"libglx-mesa0 即可）"
	}
	return text
}

// Name 是显示号，形如 ":1"。
func (d *Display) Name() string {
	if d == nil {
		return ""
	}
	return d.name
}

// Alive 表示这块屏还在：Xvfb 被杀或崩了之后，用它的浏览器会一起起不来。
func (d *Display) Alive() bool {
	if d == nil || d.exited == nil {
		return false
	}
	select {
	case <-d.exited:
		return false
	default:
		return true
	}
}

// Stop 关掉这块屏。浏览器退出后不关的话，每次重启都会多留一个 Xvfb 进程。
func (d *Display) Stop() {
	if d == nil || d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Kill()
	if d.exited != nil {
		<-d.exited
	}
}

// Env 返回要补给浏览器进程的环境变量。
func (d *Display) Env() []string {
	if d == nil {
		return nil
	}
	return []string{"DISPLAY=" + d.name}
}

// Shared 是一块按需拉起、之后一直留着的虚拟屏，给一次性的有头浏览器共用：每次渲染
// 都起一块新屏太浪费，一块屏上同时开几个 Chrome 窗口也互不干扰。屏没了（被杀、崩了）
// 下一次要用时重新拉一块。
type Shared struct {
	Width, Height int

	mu      sync.Mutex
	display *Display
}

// Env 返回有头浏览器该补的环境变量，ok 为假表示这台机器凑不出屏幕、只能无头。
// 有现成图形会话时不拉 Xvfb，返回空环境。
func (s *Shared) Env() (env []string, ok bool) {
	if SystemDisplayAvailable() {
		return nil, true
	}
	if !Available() {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.display.Alive() {
		display, err := Start(s.Width, s.Height)
		if err != nil {
			return nil, false
		}
		s.display = display
	}
	return s.display.Env(), true
}

// tail 留着 Xvfb 最后几行输出。
type tail struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}
