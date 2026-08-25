// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
		return nil, fmt.Errorf("screenshot: %w", err)
	}

	root, err := os.MkdirTemp("", "diana-screenshot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	profileDir := filepath.Join(root, "profile")
	cacheDir := filepath.Join(root, "cache")
	crashDir := filepath.Join(root, "crash")
	for _, dir := range []string{profileDir, cacheDir, crashDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	pagePath := filepath.Join(root, "page.html")
	if err := os.WriteFile(pagePath, []byte(req.HTML), 0o600); err != nil {
		return nil, err
	}
	outputPath := filepath.Join(root, "shot.png")

	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	args := append([]string{
		"--headless=new",
		"--screenshot=" + outputPath,
		// 窗口尺寸就是出图尺寸；不锁死缩放比例的话，跑在 HiDPI 环境里会出一张
		// 尺寸对不上的图。
		fmt.Sprintf("--window-size=%d,%d", req.Width, req.Height),
		"--force-device-scale-factor=1",
		"--hide-scrollbars",
		"--user-data-dir=" + profileDir,
		"--disk-cache-dir=" + cacheDir,
		"--crash-dumps-dir=" + crashDir,
		"--no-sandbox",
		"--disable-gpu",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-breakpad",
		"--disable-crash-reporter",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-notifications",
		"--no-first-run",
		"--no-default-browser-check",
	}, "file://"+pagePath)

	command := exec.CommandContext(runCtx, executable, args...)
	command.Env = sandboxedBrowserEnvironment(os.Environ(), root)
	output, err := command.CombinedOutput()
	if err != nil {
		if runCtx.Err() != nil {
			return nil, fmt.Errorf("screenshot: 渲染超时（%s）", req.Timeout)
		}
		return nil, fmt.Errorf("screenshot: %w: %s", err, firstNonEmptyLine(string(output)))
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		// 浏览器退出码是 0 但没出文件：多半是参数被这个版本忽略了，把它的输出
		// 带上，否则这里只有一句「文件不存在」，查不下去。
		return nil, fmt.Errorf("screenshot: 浏览器没有产出图片：%s", firstNonEmptyLine(string(output)))
	}
	if info.Size() > maxScreenshotBytes {
		return nil, fmt.Errorf("screenshot: 图片 %.1fMB 过大", float64(info.Size())/(1<<20))
	}
	return os.ReadFile(outputPath)
}
