//go:build !windows

package webui

import "fmt"

func windowsTotalCPUUsagePercent() (float64, error) {
	return 0, fmt.Errorf("windows CPU metrics unavailable")
}

func windowsMemoryUsage() (uint64, uint64, error) {
	return 0, 0, fmt.Errorf("windows memory metrics unavailable")
}
