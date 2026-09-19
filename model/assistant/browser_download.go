// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	chromeForTestingVersionsURL = "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"
	browserDownloadHTTPTimeout  = 2 * time.Minute
	browserDownloadTotalTimeout = 10 * time.Minute
)

// chromeForTestingVersionsEndpoint 抽成变量是为了测试能指向本地假服务器。
var chromeForTestingVersionsEndpoint = chromeForTestingVersionsURL

// browserDownloadDir 是无 root 浏览器安装的落点：data 目录下的 browser/ 子目录，
// diana 运行用户一定可写。位置约定与工作区、媒体缓存一致——跟着 APP_DB_PATH 走。
func browserDownloadDir() string {
	if dir := strings.TrimSpace(os.Getenv("DIANA_BROWSER_DIR")); dir != "" {
		return dir
	}
	if dbPath := strings.TrimSpace(os.Getenv("APP_DB_PATH")); dbPath != "" {
		return filepath.Join(filepath.Dir(dbPath), "browser")
	}
	if cacheDir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cacheDir, "diana", "browser")
	}
	return ""
}

type chromeForTestingVersions struct {
	Channels struct {
		Stable struct {
			Version   string `json:"version"`
			Downloads struct {
				Chrome []struct {
					Platform string `json:"platform"`
					URL      string `json:"url"`
				} `json:"chrome"`
			} `json:"downloads"`
		} `json:"Stable"`
	} `json:"channels"`
}

// chromeForTestingPlatform 返回当前机器的 Chrome for Testing 平台名。
// CfT 只发布 Linux 构建；其他平台返回空串，调用方引导走系统包管理器。
func chromeForTestingPlatform() string {
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "linux64"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return "linux-arm64"
	}
	return ""
}

// runningOnMusl 检测当前系统是不是 musl libc（Alpine 及其衍生）。Chrome for
// Testing 只发布 glibc 构建：在 musl 上动态 loader 连二进制都起不来（静态 TLS
// 重定位类型不兼容，gcompat 也救不了），与其下完 100 MB 再失败，不如一开始就
// 引导走系统包管理器。
func runningOnMusl() bool {
	for _, pattern := range []string{"/lib/ld-musl-*.so.1", "/usr/lib/ld-musl-*.so.1"} {
		if matches, err := filepath.Glob(pattern); err == nil && len(matches) > 0 {
			return true
		}
	}
	return false
}

// downloadChromeForTesting 从 Chrome for Testing 拉取 Stable 版完整 Chrome
// （有头构建），解压到 browserDownloadDir() 下并返回可执行文件路径。统一用完整
// 二进制、调用时以 --headless=new 无头运行：渲染行为与桌面版 Chrome 完全一致，
// 不引入旧无头实现的差异。官方 zip 不带校验和，完整性以「--version 能跑通」
// 为准——和依赖探测同一把尺子，调用方随后验证。重复调用会重新下载并覆盖，
// 也用于升级到新版本。
//
// 只适用于 glibc Linux：musl 系统上 CfT 二进制无法执行，见 runningOnMusl。
func downloadChromeForTesting(ctx context.Context) (string, error) {
	platform := chromeForTestingPlatform()
	if platform == "" {
		return "", fmt.Errorf("Chrome for Testing 只提供 Linux 官方构建（当前 %s/%s），请改用系统包管理器安装 Chromium", runtime.GOOS, runtime.GOARCH)
	}
	dir := browserDownloadDir()
	if dir == "" {
		return "", errors.New("无法确定浏览器下载目录")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建浏览器下载目录失败：%w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, browserDownloadTotalTimeout)
	defer cancel()
	client := &http.Client{Timeout: browserDownloadHTTPTimeout}

	downloadURL, err := resolveChromeDownloadURL(ctx, client)
	if err != nil {
		return "", err
	}

	zipPath, err := downloadFile(ctx, client, dir, downloadURL)
	if err != nil {
		return "", err
	}
	defer os.Remove(zipPath)

	if err := extractChromeZip(zipPath, dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, "chrome-"+platform, "chrome"), nil
}

// resolveChromeDownloadURL 查 last-known-good-versions JSON，拿 Stable 通道当前
// 版本对应平台的 Chrome 下载地址。
func resolveChromeDownloadURL(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chromeForTestingVersionsEndpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("查询 Chrome for Testing 版本信息失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("查询 Chrome for Testing 版本信息失败：HTTP %d", resp.StatusCode)
	}
	var versions chromeForTestingVersions
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&versions); err != nil {
		return "", fmt.Errorf("解析 Chrome for Testing 版本信息失败：%w", err)
	}
	for _, download := range versions.Channels.Stable.Downloads.Chrome {
		if download.Platform == chromeForTestingPlatform() {
			if download.URL == "" {
				break
			}
			return download.URL, nil
		}
	}
	return "", fmt.Errorf("Chrome for Testing Stable 没有 %s 平台的 Chrome 下载", chromeForTestingPlatform())
}

func downloadFile(ctx context.Context, client *http.Client, dir, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载 %s 失败：%w", filepath.Base(url), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 %s 失败：HTTP %d", filepath.Base(url), resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, "chrome-*.zip")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("写入 %s 失败：%w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// extractChromeZip 解压 Chrome for Testing zip 到 dir。包内路径自带
// chrome-<platform>/ 顶层目录，原样保留。entry 名做 zip slip 防护：拒绝一切
// 越出 dir 的路径。
func extractChromeZip(zipPath, dir string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开 %s 失败：%w", filepath.Base(zipPath), err)
	}
	defer reader.Close()
	for _, entry := range reader.File {
		clean := filepath.Clean(entry.Name)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("压缩包里发现不安全路径 %q，已中止解压", entry.Name)
		}
		target := filepath.Join(dir, clean)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		src, err := entry.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			src.Close()
			return err
		}
		_, copyErr := io.Copy(dst, src)
		closeErr := dst.Close()
		src.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
