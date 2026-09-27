// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build windows

package main

// stdoutIsJournal Windows 上没有 systemd journal。
func stdoutIsJournal() bool {
	return false
}
