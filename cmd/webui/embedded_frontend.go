// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:webdist
var embeddedFrontendFiles embed.FS

// embeddedFrontend 返回编进二进制的前端；源码构建没放前端时返回 false。
func embeddedFrontend() (fs.FS, bool) {
	sub, err := fs.Sub(embeddedFrontendFiles, "webdist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}

// loadEmbeddedFrontend 是 selectFrontend 读内嵌前端的入口，测试替换它来模拟发布包和源码构建。
var loadEmbeddedFrontend = embeddedFrontend

// frontendChoice 是启动时选中的前端来源，服务和 diana doctor 共用同一套判断。
type frontendChoice struct {
	root   http.FileSystem
	source string // "embedded" 或目录路径
	ready  bool   // 选中的来源里有 index.html
	note   string // 没按配置走或目录不完整时给日志和 doctor 的说明
}

// selectFrontend 选出要服务的前端。server.frontend_dist 显式配置时优先，相对路径
// 按配置文件所在目录解析；目录里没有 index.html 时有内嵌前端就退回内嵌。旧版
// 安装器自动写进配置的 frontend-next/dist 不算主人的选择：升级后那份目录要么被
// 删了、要么停在旧版本，有内嵌前端时直接忽略。都没有才按常见目录查找。
func selectFrontend(configPath, setting string) frontendChoice {
	embedded, hasEmbedded := loadEmbeddedFrontend()
	setting = strings.TrimSpace(setting)
	if setting != "" && hasEmbedded && legacyPackagedFrontendSetting(setting) {
		return frontendChoice{root: http.FS(embedded), source: "embedded", ready: true,
			note: "ignoring server.frontend_dist left by an older installer: " + setting}
	}
	if setting != "" {
		dir := resolveConfigRelative(configPath, setting)
		if frontendDirReady(dir) {
			return frontendChoice{root: http.Dir(dir), source: dir, ready: true}
		}
		if hasEmbedded {
			return frontendChoice{root: http.FS(embedded), source: "embedded", ready: true,
				note: "server.frontend_dist has no index.html, using embedded frontend: " + dir}
		}
		return frontendChoice{root: http.Dir(dir), source: dir,
			note: "server.frontend_dist has no index.html: " + dir}
	}
	if hasEmbedded {
		return frontendChoice{root: http.FS(embedded), source: "embedded", ready: true}
	}
	dir := frontendDistDir()
	if frontendDirReady(dir) {
		return frontendChoice{root: http.Dir(dir), source: dir, ready: true}
	}
	return frontendChoice{root: http.Dir(dir), source: dir,
		note: "frontend assets not found; build the frontend or set server.frontend_dist: " + dir}
}

// legacyPackagedFrontendSetting 认出 v0.8.x 安装器写进配置的发布包前端目录。
func legacyPackagedFrontendSetting(setting string) bool {
	return strings.HasSuffix(filepath.ToSlash(filepath.Clean(setting)), "frontend-next/dist")
}

func frontendDirReady(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "index.html"))
	return err == nil && info.Mode().IsRegular()
}
