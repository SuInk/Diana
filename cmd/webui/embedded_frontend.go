// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:webdist
var embeddedFrontendFiles embed.FS

// embeddedFrontend 返回编进二进制的前端。前端只从这里来：构建时先把
// frontend/dist 复制进 cmd/webui/webdist（make build 会做），没复制就返回 false。
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

// frontendFileSystem 返回要服务的前端；没编进前端时页面请求一律 404，启动日志会提示。
func frontendFileSystem() (http.FileSystem, bool) {
	sub, ok := embeddedFrontend()
	if !ok {
		return http.FS(embed.FS{}), false
	}
	return http.FS(sub), true
}
