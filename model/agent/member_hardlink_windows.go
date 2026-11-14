// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build windows

package agent

import "io/fs"

func memberFileSingleHardlink(info fs.FileInfo) bool {
	return info != nil
}
