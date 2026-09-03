// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// 把一段自包含的 HTML 截成 PNG。
//
// 走命令行的 --screenshot 而不是 CDP：这里要的就是「开一次、拍一张、退出」，
// 不需要页面观测，也不需要等网络空闲——HTML 是本地生成的，没有外部请求。
// 复用同一套沙箱参数和可执行文件查找逻辑，部署里配过的浏览器路径这边直接生效。
const (
	defaultScreenshotTimeout = 25 * time.Second
	maxScreenshotBytes       = 8 << 20
)

// ScreenshotRequest 描述一次截图。
type ScreenshotRequest struct {
	// HTML 必须是自包含的：外链的样式和字体在沙箱里取不到。
	HTML          string
	Width, Height int
	// Executable 为空时按环境变量和常见路径查找，和页面渲染那条路一致。
	Executable string
	Timeout    time.Duration
	// VirtualTimeBudget 让浏览器在拍照前把脚本跑完。
	//
	// 纯 HTML/CSS 的页面（关系图就是）加载完就定型了，拍照时机不敏感。但页面里
	// 有脚本要画东西时（比如 mermaid 是异步生成 SVG 的），--screenshot 可能在
	// 脚本跑完之前就拍了，出一张空白图。虚拟时间让浏览器把这段时间快进掉，
	// 到点再拍，既不用真等，也不会拍早。为零表示不启用，保持既有行为。
	VirtualTimeBudget time.Duration
}

// CaptureHTMLScreenshot 渲染 HTML 并返回 PNG 字节。
func CaptureHTMLScreenshot(ctx context.Context, req ScreenshotRequest) ([]byte, error) {
	if strings.TrimSpace(req.HTML) == "" {
		return nil, errors.New("screenshot: empty html")
	}
	if req.Width <= 0 {
		req.Width = 1000
	}
	if req.Height <= 0 {
		req.Height = 900
	}
	if req.Timeout <= 0 {
		req.Timeout = defaultScreenshotTimeout
	}
	executable, err := findHeadlessBrowserExecutable(req.Executable)
	if err != nil {
		obscura, obscuraErr := findObscuraExecutable(req.Executable)
		if obscuraErr != nil {
			return nil, fmt.Errorf("screenshot: %w", err)
		}
		return captureHTMLScreenshotWithObscura(ctx, obscura, req)
	}
	if looksLikeObscuraExecutable(executable) {
		return captureHTMLScreenshotWithObscura(ctx, executable, req)
	}

	dirs, err := newBrowserSandboxDirs("diana-screenshot-")
	if err != nil {
		return nil, err
	}
	defer dirs.remove()
	root, profileDir, cacheDir, crashDir := dirs.root, dirs.profile, dirs.cache, dirs.crash
	pagePath := filepath.Join(root, "page.html")
	if err := os.WriteFile(pagePath, []byte(req.HTML), 0o600); err != nil {
		return nil, err
	}
	outputPath := filepath.Join(root, "shot.png")

	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	// 沙盒加固走和网页渲染同一份底座，这里只追加截图这条路自己的参数。之前这份
	// 参数是手抄的子集，抄漏了 --host-resolver-rules，等于截图那条路没挡住
	// localhost 和内网域名。
	args := append(sandboxedChromeBaseArgs(profileDir, cacheDir, crashDir),
		"--screenshot="+outputPath,
		// 窗口尺寸就是出图尺寸；不锁死缩放比例的话，跑在 HiDPI 环境里会出一张
		// 尺寸对不上的图。
		fmt.Sprintf("--window-size=%d,%d", req.Width, req.Height),
		"--force-device-scale-factor=1",
		// 这两个是截图这条路一直带着的，跟 CDP 那条不一样：容器里以 root 跑时
		// Chrome 的进程沙盒起不来，没有它直接崩。是兼容性取舍，不在这次合并的
		// 范围里——要改得单独评估哪些部署会因此跑不动。
		"--no-sandbox",
		"--disable-gpu",
	)
	if req.VirtualTimeBudget > 0 {
		args = append(args, fmt.Sprintf("--virtual-time-budget=%d", req.VirtualTimeBudget.Milliseconds()))
	}
	args = append(args, "file://"+pagePath)

	command := exec.CommandContext(runCtx, executable, args...)
	command.Env = sandboxedBrowserEnvironment(os.Environ(), root)
	diagnosticsPath := filepath.Join(root, "browser.log")
	diagnostics, err := os.Create(diagnosticsPath)
	if err != nil {
		return nil, err
	}
	defer diagnostics.Close()
	command.Stdout = diagnostics
	command.Stderr = diagnostics
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("screenshot: 启动浏览器失败：%w", err)
	}

	// 部分 macOS Chrome 版本会先写完截图，却因为后台子进程没有收尾而一直不退出。
	// 旧实现只等 Wait，最后在超时时把已经写好的 PNG 一起丢掉。这里以「文件已完整且
	// 能解码」为完成信号；拿到图片后结束这次一次性浏览器，不再依赖它自行收尾。
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-waited:
			if pngBytes, readErr := readCompletedScreenshot(outputPath); readErr == nil {
				return pngBytes, nil
			}
			if err != nil {
				return nil, fmt.Errorf("screenshot: %w: %s", err, screenshotDiagnostics(diagnosticsPath))
			}
			return nil, fmt.Errorf("screenshot: 浏览器没有产出有效图片：%s", screenshotDiagnostics(diagnosticsPath))
		case <-ticker.C:
			pngBytes, err := readCompletedScreenshot(outputPath)
			if err != nil {
				continue
			}
			_ = command.Process.Kill()
			<-waited
			return pngBytes, nil
		case <-runCtx.Done():
			_ = command.Process.Kill()
			<-waited
			if pngBytes, err := readCompletedScreenshot(outputPath); err == nil {
				return pngBytes, nil
			}
			return nil, fmt.Errorf("screenshot: 渲染超时（%s）：%s", req.Timeout, screenshotDiagnostics(diagnosticsPath))
		}
	}
}

func captureHTMLScreenshotWithObscura(ctx context.Context, executable string, req ScreenshotRequest) ([]byte, error) {
	dirs, err := newBrowserSandboxDirs("diana-obscura-screenshot-")
	if err != nil {
		return nil, err
	}
	defer dirs.remove()
	pagePath := filepath.Join(dirs.root, "page.html")
	if err := os.WriteFile(pagePath, []byte(req.HTML), 0o600); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	args := []string{
		"serve", "--host", "127.0.0.1", "--port", fmt.Sprintf("%d", port),
		"--allow-private-network",
		"--allow-file-access",
		"--quiet",
	}
	command := exec.CommandContext(runCtx, executable, args...)
	diagnostics := &cappedBuffer{limit: 32 * 1024}
	command.Stdout = diagnostics
	command.Stderr = diagnostics
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("screenshot: 启动 Obscura 失败：%w", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		select {
		case <-waited:
		case <-time.After(2 * time.Second):
		}
	}()
	endpoint := fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser", port)
	if err := waitForObscuraCDP(runCtx, port, waited); err != nil {
		return nil, fmt.Errorf("screenshot: Obscura CDP 启动失败：%w：%s", err, compactBrowserError(diagnostics.String()))
	}
	allocatorCtx, cancelAllocator := chromedp.NewRemoteAllocator(runCtx, endpoint, chromedp.NoModifyURL)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx)
	defer cancelBrowser()
	var pngBytes []byte
	actions := []chromedp.Action{
		emulation.SetDeviceMetricsOverride(int64(req.Width), int64(req.Height), 1, false),
		chromedp.Navigate("file://" + pagePath),
	}
	if wait := min(req.VirtualTimeBudget, 2*time.Second); wait > 0 {
		actions = append(actions, chromedp.Sleep(wait))
	}
	actions = append(actions, chromedp.CaptureScreenshot(&pngBytes))
	if err := chromedp.Run(browserCtx, actions...); err != nil {
		return nil, fmt.Errorf("screenshot: Obscura 截图失败：%w：%s", err, compactBrowserError(diagnostics.String()))
	}
	if _, err := png.Decode(bytes.NewReader(pngBytes)); err != nil {
		return nil, fmt.Errorf("screenshot: Obscura 返回了无效 PNG：%w", err)
	}
	return pngBytes, nil
}

func waitForObscuraCDP(ctx context.Context, port int, waited <-chan error) error {
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(min(5*time.Second, defaultScreenshotTimeout))
	defer timer.Stop()
	address := fmt.Sprintf("127.0.0.1:%d", port)
	for {
		select {
		case err := <-waited:
			if err == nil {
				return errors.New("进程提前退出")
			}
			return fmt.Errorf("进程提前退出：%w", err)
		case <-ticker.C:
			connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if err == nil {
				_ = connection.Close()
				return nil
			}
		case <-timer.C:
			return errors.New("等待 CDP 端口超时")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func screenshotDiagnostics(path string) string {
	raw, _ := os.ReadFile(path)
	return firstNonEmptyLine(string(raw))
}

func readCompletedScreenshot(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxScreenshotBytes {
		return nil, fmt.Errorf("图片 %.1fMB 过大", float64(info.Size())/(1<<20))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if _, err := png.DecodeConfig(bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return raw, nil
}
