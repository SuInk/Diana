// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package hostinfo 采集这台机器的运行状态：CPU、内存、磁盘、温度、功率。
//
// 这套采集原来长在 webui 里，只服务控制台的总览页。但 webui 单向依赖 model，
// 反过来不行，于是机器人自己反而拿不到这些数字——「看一下内存占用」这种问题
// 它答不上来。移到这里之后两边共用一份，不必各写一遍再各错一遍。
package hostinfo

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var processStartedAt = time.Now()

// Snapshot 是一次采样。取不到的项留零值，并在对应的 *Unavailable 里写明原因——
// 「读不到」和「是 0」必须分得开：把读不到的温度报成 0℃ 比不报更糟。
type Snapshot struct {
	CollectedAt time.Time `json:"collected_at"`

	Hostname             string `json:"hostname,omitempty"`
	OS                   string `json:"os"`
	Arch                 string `json:"arch"`
	ProcessID            int    `json:"process_id"`
	ProcessUptimeSeconds int64  `json:"process_uptime_seconds"`

	CPUModel        string    `json:"cpu_model,omitempty"`
	CPUCores        int       `json:"cpu_cores"`
	CPUUsagePercent float64   `json:"cpu_usage_percent"`
	LoadAverage     []float64 `json:"load_average,omitempty"`

	MemoryTotalBytes   uint64  `json:"memory_total_bytes"`
	MemoryUsedBytes    uint64  `json:"memory_used_bytes"`
	MemoryUsagePercent float64 `json:"memory_usage_percent"`

	ProcessCPUPercent  float64 `json:"process_cpu_percent"`
	ProcessMemoryBytes uint64  `json:"process_memory_bytes"`

	StoragePath           string  `json:"storage_path,omitempty"`
	StorageTotalBytes     uint64  `json:"storage_total_bytes"`
	StorageUsedBytes      uint64  `json:"storage_used_bytes"`
	StorageAvailableBytes uint64  `json:"storage_available_bytes"`
	StorageUsagePercent   float64 `json:"storage_usage_percent"`

	Temperatures []Reading `json:"temperatures,omitempty"`
	Power        []Reading `json:"power,omitempty"`
	Battery      *Battery  `json:"battery,omitempty"`

	GoHeapAllocBytes  uint64 `json:"go_heap_alloc_bytes"`
	GoHeapSystemBytes uint64 `json:"go_heap_system_bytes"`
	GoRoutines        int    `json:"go_routines"`
	RuntimeVersion    string `json:"runtime_version"`

	MetricsUnavailable        string `json:"metrics_unavailable,omitempty"`
	ProcessMetricsUnavailable string `json:"process_metrics_unavailable,omitempty"`
	StorageMetricsUnavailable string `json:"storage_metrics_unavailable,omitempty"`
	ThermalUnavailable        string `json:"thermal_unavailable,omitempty"`
	PowerUnavailable          string `json:"power_unavailable,omitempty"`
}

// Reading 是一路带标签的读数，比如某个传感器的温度或某个功率域的瓦数。
type Reading struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// Battery 是电池状态，台式机和大多数服务器上没有。
type Battery struct {
	Percent float64 `json:"percent"`
	Status  string  `json:"status,omitempty"`
}

// Collect 采一次样。storagePath 是要统计容量的目录，留空表示不看磁盘。
func Collect(now time.Time, storagePath string) Snapshot {
	if now.IsZero() {
		now = time.Now()
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	hostname, _ := os.Hostname()
	snapshot := Snapshot{
		CollectedAt:          now,
		Hostname:             strings.TrimSpace(hostname),
		OS:                   runtime.GOOS,
		Arch:                 runtime.GOARCH,
		ProcessID:            os.Getpid(),
		ProcessUptimeSeconds: int64(now.Sub(processStartedAt).Seconds()),
		CPUModel:             CPUModel(),
		CPUCores:             runtime.NumCPU(),
		LoadAverage:          loadAverage(),
		GoHeapAllocBytes:     mem.HeapAlloc,
		GoHeapSystemBytes:    mem.HeapSys,
		GoRoutines:           runtime.NumGoroutine(),
		RuntimeVersion:       runtime.Version(),
	}
	if usage, err := TotalCPUUsagePercent(snapshot.CPUCores); err == nil {
		snapshot.CPUUsagePercent = usage
	} else {
		snapshot.MetricsUnavailable = err.Error()
	}
	if total, used, err := MemoryUsage(); err == nil {
		snapshot.MemoryTotalBytes = total
		snapshot.MemoryUsedBytes = used
		if total > 0 {
			snapshot.MemoryUsagePercent = RoundPercent((float64(used) / float64(total)) * 100)
		}
	} else if snapshot.MetricsUnavailable == "" {
		snapshot.MetricsUnavailable = err.Error()
	}
	if procCPU, procMem, err := ProcessUsage(os.Getpid()); err == nil {
		snapshot.ProcessCPUPercent = procCPU
		snapshot.ProcessMemoryBytes = procMem
	} else {
		snapshot.ProcessMetricsUnavailable = err.Error()
	}
	if storagePath = strings.TrimSpace(storagePath); storagePath != "" {
		if total, used, available, err := StorageUsage(storagePath); err == nil {
			snapshot.StoragePath = storagePath
			snapshot.StorageTotalBytes = total
			snapshot.StorageUsedBytes = used
			snapshot.StorageAvailableBytes = available
			if total > 0 {
				snapshot.StorageUsagePercent = RoundPercent((float64(used) / float64(total)) * 100)
			}
		} else {
			snapshot.StorageMetricsUnavailable = err.Error()
		}
	}
	if readings, err := temperatures(); err == nil {
		snapshot.Temperatures = readings
	} else {
		snapshot.ThermalUnavailable = err.Error()
	}
	if readings, battery, err := powerDraw(); err == nil {
		snapshot.Power = readings
		snapshot.Battery = battery
	} else {
		snapshot.PowerUnavailable = err.Error()
	}
	return snapshot
}

// CPUModel 返回处理器型号，取不到时返回空串。
func CPUModel() string {
	switch runtime.GOOS {
	case "darwin":
		return CommandOutput("sysctl", "-n", "machdep.cpu.brand_string")
	case "linux":
		data, err := os.ReadFile("/proc/cpuinfo")
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if ok && strings.EqualFold(strings.TrimSpace(key), "model name") {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

// TotalCPUUsagePercent 返回整机 CPU 占用（按核数归一）。
func TotalCPUUsagePercent(cores int) (float64, error) {
	if runtime.GOOS == "windows" {
		return windowsTotalCPUUsagePercent()
	}
	if cores <= 0 {
		cores = 1
	}
	output := CommandOutput("ps", "-A", "-o", "%cpu=")
	if strings.TrimSpace(output) == "" {
		return 0, fmt.Errorf("cpu usage command unavailable")
	}
	var total float64
	for _, field := range strings.Fields(output) {
		value, err := strconv.ParseFloat(strings.TrimSpace(field), 64)
		if err == nil {
			total += value
		}
	}
	return ClampPercent(RoundPercent(total / float64(cores))), nil
}

// loadAverage 读 1/5/15 分钟平均负载。Windows 上没有这个概念。
func loadAverage() []float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		if runtime.GOOS == "darwin" {
			return parseDarwinLoadAverage(CommandOutput("sysctl", "-n", "vm.loadavg"))
		}
		return nil
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return nil
	}
	out := make([]float64, 0, 3)
	for _, field := range fields[:3] {
		value, err := strconv.ParseFloat(field, 64)
		if err != nil {
			return nil
		}
		out = append(out, value)
	}
	return out
}

// parseDarwinLoadAverage 解析 `{ 1.20 1.30 1.40 }` 这种花括号包着的输出。
func parseDarwinLoadAverage(raw string) []float64 {
	fields := strings.Fields(strings.Trim(strings.TrimSpace(raw), "{}"))
	if len(fields) < 3 {
		return nil
	}
	out := make([]float64, 0, 3)
	for _, field := range fields[:3] {
		value, err := strconv.ParseFloat(field, 64)
		if err != nil {
			return nil
		}
		out = append(out, value)
	}
	return out
}

// MemoryUsage 返回整机内存总量与已用量。
func MemoryUsage() (uint64, uint64, error) {
	switch runtime.GOOS {
	case "darwin":
		total, err := darwinTotalMemory()
		if err != nil {
			return 0, 0, err
		}
		used, err := darwinUsedMemory()
		if err != nil {
			return total, 0, err
		}
		return total, used, nil
	case "linux":
		return linuxMemoryUsage()
	case "windows":
		return windowsMemoryUsage()
	default:
		return 0, 0, fmt.Errorf("memory metrics unsupported on %s", runtime.GOOS)
	}
}

func darwinTotalMemory() (uint64, error) {
	output := CommandOutput("sysctl", "-n", "hw.memsize")
	total, err := strconv.ParseUint(strings.TrimSpace(output), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("read total memory: %w", err)
	}
	return total, nil
}

func darwinUsedMemory() (uint64, error) {
	output := CommandOutput("vm_stat")
	if strings.TrimSpace(output) == "" {
		return 0, fmt.Errorf("vm_stat unavailable")
	}
	pageSize := uint64(4096)
	var active, wired, compressed uint64
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "page size of") {
			for _, field := range strings.Fields(line) {
				if value, err := strconv.ParseUint(field, 10, 64); err == nil && value > 0 {
					pageSize = value
					break
				}
			}
			continue
		}
		key, rawValue, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value, err := strconv.ParseUint(strings.Trim(strings.TrimSpace(rawValue), "."), 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Pages active":
			active = value
		case "Pages wired down":
			wired = value
		case "Pages occupied by compressor":
			compressed = value
		}
	}
	return (active + wired + compressed) * pageSize, nil
}

func linuxMemoryUsage() (uint64, uint64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		key, rawValue, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rawValue)
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseUint(fields[0], 10, 64)
		if err == nil {
			values[key] = value * 1024
		}
	}
	total := values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 {
		return 0, 0, fmt.Errorf("MemTotal unavailable")
	}
	if available > total {
		available = 0
	}
	return total, total - available, nil
}

// ProcessUsage 返回指定进程的 CPU 占用和常驻内存。
func ProcessUsage(pid int) (float64, uint64, error) {
	output := CommandOutput("ps", "-p", strconv.Itoa(pid), "-o", "%cpu=,rss=")
	fields := strings.Fields(output)
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("process usage unavailable")
	}
	cpu, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, 0, err
	}
	rssKB, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return RoundPercent(cpu), rssKB * 1024, nil
}

// CommandOutput 跑一条命令并返回它的标准输出，失败时返回空串。
func CommandOutput(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}

// RoundPercent 保留两位小数，并把 NaN/Inf 归零。
func RoundPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Round(value*100) / 100
}

// ClampPercent 把百分比夹在 0~100。
func ClampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

const snapshotCacheTTL = 5 * time.Second

type cachedSnapshot struct {
	collectedAt time.Time
	snapshot    Snapshot
}

var snapshotCache = struct {
	sync.Mutex
	byPath map[string]cachedSnapshot
}{byPath: map[string]cachedSnapshot{}}

// Cached 是 Collect 的带缓存版本。采样要跑 ps、读 sysfs，几秒内重复问是常态
// （总览页在推送，机器人也可能连问两句），没必要每次都真采。
func Cached(now time.Time, storagePath string) Snapshot {
	if now.IsZero() {
		now = time.Now()
	}
	snapshotCache.Lock()
	defer snapshotCache.Unlock()
	if cached, ok := snapshotCache.byPath[storagePath]; ok && now.Sub(cached.collectedAt) < snapshotCacheTTL {
		return cached.snapshot
	}
	snapshot := Collect(now, storagePath)
	snapshotCache.byPath[storagePath] = cachedSnapshot{collectedAt: now, snapshot: snapshot}
	return snapshot
}
