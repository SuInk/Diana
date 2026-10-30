// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package scripts 把要随程序分发的脚本编进二进制。
package scripts

import _ "embed"

// UninstallPowerShell 是 Windows 卸载脚本。Windows 只发一个 exe，
// `diana uninstall` 从这里取出脚本再运行。
//
//go:embed uninstall.ps1
var UninstallPowerShell []byte
