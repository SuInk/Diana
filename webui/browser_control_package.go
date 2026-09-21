// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"archive/zip"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// browserControlExtensionDirCandidates 是扩展源码目录的查找顺序：
// 源码目录直接跑、二进制旁边跑、以及容器镜像里的固定位置。
//
// 这条下载链路是给容器用户准备的：用 compose 拉镜像的人手上没有仓库检出，
// 文档里那句「选 packaging/browser-control-extension 目录」对他们无从下手。
func browserControlExtensionDirCandidates() []string {
	candidates := []string{
		"packaging/browser-control-extension",
		"../../packaging/browser-control-extension",
		"/app/browser-control-extension",
	}
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		candidates = append([]string{
			filepath.Join(dir, "browser-control-extension"),
			filepath.Clean(filepath.Join(dir, "..", "Resources", "browser-control-extension")),
		}, candidates...)
	}
	if custom := strings.TrimSpace(os.Getenv("DIANA_BROWSER_EXTENSION_DIR")); custom != "" {
		candidates = append([]string{custom}, candidates...)
	}
	return candidates
}

// browserControlExtensionDir 返回扩展源码目录，找不到时返回空串。
func browserControlExtensionDir() string {
	for _, candidate := range browserControlExtensionDirCandidates() {
		if stat, err := os.Stat(candidate); err == nil && stat.IsDir() {
			if _, err := os.Stat(filepath.Join(candidate, "manifest.json")); err == nil {
				return candidate
			}
		}
	}
	return ""
}

// downloadExtension 把扩展目录打成 zip 直接吐给浏览器。解压后就是
// 「加载已解压的扩展程序」要选的那个目录，不需要仓库检出。
func (h *BrowserControlHandler) downloadExtension(c *gin.Context) {
	dir := browserControlExtensionDir()
	if dir == "" {
		writeError(c, http.StatusNotFound, errors.New("这个部署里没有带扩展源码，请从仓库的 packaging/browser-control-extension 取"))
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", `attachment; filename="diana-browser-control-extension.zip"`)
	writer := zip.NewWriter(c.Writer)
	defer func() { _ = writer.Close() }()
	if err := writeDirToZip(writer, dir); err != nil {
		// 响应头已经发出去了，这里只能记一笔：客户端会拿到一个不完整的 zip，
		// 解压时自己会报错，比装作成功要好。
		recordError(c.Request.Context(), h.logs, "browser_control_extension_download", err, dir, nil)
	}
}

func writeDirToZip(writer *zip.Writer, root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		out, err := writer.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = io.Copy(out, file)
		return err
	})
}
