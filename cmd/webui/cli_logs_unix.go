// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package main

import "os"

func openFollowedLog(path string) (*os.File, error) {
	return os.Open(path)
}
