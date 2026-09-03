// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	obscuraVersion          = "v0.2.1"
	obscuraDownloadMaxBytes = 120 << 20
)

type obscuraReleaseAsset struct {
	name   string
	sha256 string
}

var obscuraReleaseAssets = map[string]obscuraReleaseAsset{
	"darwin/arm64":  {"obscura-aarch64-macos.tar.gz", "5233da6426ec16667d7e4374b824189c6dfb3b325e5cf3fb5f04c7bc48b52a0f"},
	"darwin/amd64":  {"obscura-x86_64-macos.tar.gz", "e6d0f8719998fa4460bccc712b20a1e524717d5c54e943f345227bd893ec9620"},
	"linux/arm64":   {"obscura-aarch64-linux.tar.gz", "0297c26d583f598f0126a7271cc40750598a9a9cbd86d1d6f79b2b99097d5244"},
	"linux/amd64":   {"obscura-x86_64-linux.tar.gz", "6a1a66b3f1ab118fa7d31330894a868617aea68c06d75436d851356c39df1ed3"},
	"windows/amd64": {"obscura-x86_64-windows.zip", "202e7705c30b00026dcc3d493e1d5ef4ffb436767aaf84baaec11c7ff15a1a09"},
}

func obscuraInstallPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	name := "obscura"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(configDir, "diana", "tools", "obscura", obscuraVersion, name), nil
}

func installObscura(ctx context.Context) (string, error) {
	asset, ok := obscuraReleaseAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("Obscura %s 暂不提供 %s/%s 的预编译包", obscuraVersion, runtime.GOOS, runtime.GOARCH)
	}
	destination, err := obscuraInstallPath()
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	url := "https://github.com/h4ckf0r0day/obscura/releases/download/" + obscuraVersion + "/" + asset.name
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("下载 Obscura 失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("下载 Obscura 失败：HTTP %d", response.StatusCode)
	}
	archive, err := os.CreateTemp(parent, ".obscura-download-*")
	if err != nil {
		return "", err
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(archive, hasher), io.LimitReader(response.Body, obscuraDownloadMaxBytes+1))
	closeErr := archive.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written > obscuraDownloadMaxBytes {
		return "", fmt.Errorf("Obscura 下载包超过安全上限")
	}
	if actual := hex.EncodeToString(hasher.Sum(nil)); actual != asset.sha256 {
		return "", fmt.Errorf("Obscura SHA-256 校验失败：得到 %s", actual)
	}
	temporary := destination + ".new"
	defer os.Remove(temporary)
	if strings.HasSuffix(asset.name, ".zip") {
		err = extractObscuraZip(archivePath, temporary)
	} else {
		err = extractObscuraTarGZ(archivePath, temporary)
	}
	if err != nil {
		return "", err
	}
	if err := os.Chmod(temporary, 0o700); err != nil && runtime.GOOS != "windows" {
		return "", err
	}
	// 依赖探测成功时不会走到安装；能来到这里，已有文件通常是损坏或旧的下载。
	// Windows 不能用 Rename 覆盖目标，因此统一先删除这个 Diana 自己管理的精确路径。
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return "", err
	}
	return destination, nil
}

func extractObscuraTarGZ(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "obscura" {
			continue
		}
		return writeLimitedExecutable(destination, reader, header.Size)
	}
	return fmt.Errorf("Obscura 下载包内没有可执行文件")
}

func extractObscuraZip(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, entry := range reader.File {
		if filepath.Base(entry.Name) != "obscura.exe" || entry.FileInfo().IsDir() {
			continue
		}
		input, openErr := entry.Open()
		if openErr != nil {
			return openErr
		}
		err = writeLimitedExecutable(destination, input, int64(entry.UncompressedSize64))
		_ = input.Close()
		return err
	}
	return fmt.Errorf("Obscura 下载包内没有可执行文件")
}

func writeLimitedExecutable(destination string, input io.Reader, declaredSize int64) error {
	if declaredSize <= 0 || declaredSize > obscuraDownloadMaxBytes {
		return fmt.Errorf("Obscura 可执行文件大小异常：%d", declaredSize)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, obscuraDownloadMaxBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != declaredSize || written > obscuraDownloadMaxBytes {
		return fmt.Errorf("Obscura 可执行文件长度异常：%d", written)
	}
	return nil
}
