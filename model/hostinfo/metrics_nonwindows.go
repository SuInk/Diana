// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package hostinfo

import "fmt"

func windowsTotalCPUUsagePercent() (float64, error) {
	return 0, fmt.Errorf("windows CPU metrics unavailable")
}

func windowsMemoryUsage() (uint64, uint64, error) {
	return 0, 0, fmt.Errorf("windows memory metrics unavailable")
}
