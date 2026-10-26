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

// frontendFileSystem 选出要服务的前端：发布包内嵌的前端优先，这样升级后不会被
// 旧安装留下的 frontend-next/dist 盖住；没有内嵌时才按目录查找。
func frontendFileSystem(custom string) (http.FileSystem, string) {
	if sub, ok := embeddedFrontend(); ok {
		return http.FS(sub), "embedded"
	}
	dir := frontendDistDir(custom)
	return http.Dir(dir), dir
}
