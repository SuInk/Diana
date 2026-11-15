// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package box 内嵌工作区容器的 Dockerfile，Diana 首次使用时据此在本地构建镜像。
package box

import _ "embed"

// Dockerfile 是 work box 镜像的构建文件，不依赖构建上下文（docker build -）。
//
//go:embed Dockerfile
var Dockerfile string
