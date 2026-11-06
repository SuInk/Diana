// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browseruser

import (
	"io/fs"
	"os/exec"
)

// Windows 上不会以 root 运行，Lookup 永远不给账号，这几个只为编译。

// Apply 在 Windows 上什么都不做。
func (a Account) Apply(*exec.Cmd) {}

func (a Account) allowed(fs.FileInfo, fs.FileMode) bool { return true }

func owned(string, int, int) bool { return true }
