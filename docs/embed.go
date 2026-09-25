// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package docs 把中文设计文档随程序一起编译，供能力知识库检索。
// 编进去的是和这份二进制同一次提交的文档，不会拿 main 上更新的说法解释旧版本。
package docs

import "embed"

//go:embed *.md configuration.html implementation.html operations.html
var FS embed.FS
