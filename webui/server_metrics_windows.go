//go:build windows

package webui

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

type windowsFiletime struct {
	LowDateTime  uint32
	HighDateTime uint32
}

func (value windowsFiletime) ticks() uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}

type windowsMemoryStatus struct {
	Length                   uint32
	MemoryLoad               uint32
	TotalPhysical            uint64
	AvailablePhysical        uint64
	TotalPageFile            uint64
	AvailablePageFile        uint64
	TotalVirtual             uint64
	AvailableVirtual         uint64
	AvailableExtendedVirtual uint64
}

func windowsTotalCPUUsagePercent() (float64, error) {
	idleBefore, totalBefore, err := windowsCPUTimes()
	if err != nil {
		return 0, err
	}
	time.Sleep(100 * time.Millisecond)
	idleAfter, totalAfter, err := windowsCPUTimes()
	if err != nil {
		return 0, err
	}
	if totalAfter <= totalBefore {
		return 0, fmt.Errorf("GetSystemTimes returned no CPU interval")
	}
	totalDelta := totalAfter - totalBefore
	idleDelta := idleAfter - min(idleAfter, idleBefore)
	busyDelta := totalDelta - min(idleDelta, totalDelta)
	return clampPercent(roundPercent(float64(busyDelta) / float64(totalDelta) * 100)), nil
}

func windowsCPUTimes() (idleTicks, totalTicks uint64, err error) {
	var idle, kernel, user windowsFiletime
	result, _, callErr := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if result == 0 {
		return 0, 0, fmt.Errorf("GetSystemTimes: %w", callErr)
	}
	total := kernel.ticks() + user.ticks()
	if total == 0 {
		return 0, 0, fmt.Errorf("GetSystemTimes returned no CPU time")
	}
	return idle.ticks(), total, nil
}

func windowsMemoryUsage() (uint64, uint64, error) {
	status := windowsMemoryStatus{Length: uint32(unsafe.Sizeof(windowsMemoryStatus{}))}
	result, _, callErr := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 {
		return 0, 0, fmt.Errorf("GlobalMemoryStatusEx: %w", callErr)
	}
	available := min(status.AvailablePhysical, status.TotalPhysical)
	return status.TotalPhysical, status.TotalPhysical - available, nil
}
