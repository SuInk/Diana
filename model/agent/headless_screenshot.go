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
	args = append(args, "file://"+pagePath)

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
