// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SuInk/diana/model/hostinfo"
)

const dianaHostStatsToolName = "diana.host_stats"

// 「内存占多少」「CPU 现在忙不忙」以前机器人答不上来：这些数字只在控制台的总览页
// 里，采集代码住在 webui，而 webui 单向依赖 model——机器人够不着。采集移到
// model/hostinfo 之后，这个工具把同一份快照给模型。
//
// 只读，无参数：没有自由输入就没有注入面。但仍然是主人专属——主机名、磁盘路径、
// 硬件型号不该对群里所有人可见。
type dianaHostStatsTool struct {
	runtime *Runtime
	event   MessageEvent
	// now 和 collect 是给测试注入的；生产路径上走 hostinfo 的带缓存采集。
	now     func() time.Time
	collect func(time.Time, string) hostinfo.Snapshot
}

func newDianaHostStatsTool(runtime *Runtime, event MessageEvent) *dianaHostStatsTool {
	return &dianaHostStatsTool{runtime: runtime, event: event}
}

func (t *dianaHostStatsTool) Name() string { return dianaHostStatsToolName }

func (t *dianaHostStatsTool) Description() string {
	return `读取本机运行状态：CPU 型号与占用、平均负载、内存、磁盘、Diana 自身的占用，` +
		`以及这台机器能提供的温度、功率和电池读数。用户问「内存占了多少」「CPU 忙不忙」` +
		`「现在多少度」「功耗多少」时调用它，不要用命令执行工具去拼这些数字。只有主人可以调用。`
}

func (t *dianaHostStatsTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{})
}

func (t *dianaHostStatsTool) Run(ctx context.Context, _ map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("host stats: runtime is not configured")
	}
	if !t.runtime.relationshipPolicy(ctx, t.event).Owner {
		return "", fmt.Errorf("只有主人可以查看本机运行状态")
	}
	snapshot := t.snapshot()
	body, err := json.MarshalIndent(hostStatsPayload(snapshot), "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *dianaHostStatsTool) snapshot() hostinfo.Snapshot {
	now := time.Now()
	if t.now != nil {
		now = t.now()
	}
	collect := hostinfo.Cached
	if t.collect != nil {
		collect = t.collect
	}
	return collect(now, hostStatsStoragePath())
}

// hostStatsPayload 把快照压成给模型看的形状。
//
// 字节数同时给原始值和人话：模型算 8589934592 是多少 GB 经常算错，而它转述给用户的
// 是后者。取不到的项不写 0，写明为什么读不到——把读不到的温度报成 0℃ 比不报更糟。
func hostStatsPayload(snapshot hostinfo.Snapshot) map[string]any {
	payload := map[string]any{
		"host":              snapshot.Hostname,
		"platform":          snapshot.OS + "/" + snapshot.Arch,
		"uptime":            humanDuration(time.Duration(snapshot.ProcessUptimeSeconds) * time.Second),
		"cpu_model":         snapshot.CPUModel,
		"cpu_cores":         snapshot.CPUCores,
		"cpu_percent":       snapshot.CPUUsagePercent,
		"memory_used":       humanBytes(snapshot.MemoryUsedBytes),
		"memory_total":      humanBytes(snapshot.MemoryTotalBytes),
		"memory_percent":    snapshot.MemoryUsagePercent,
		"diana_cpu_percent": snapshot.ProcessCPUPercent,
		"diana_memory":      humanBytes(snapshot.ProcessMemoryBytes),
	}
	if len(snapshot.LoadAverage) == 3 {
		payload["load_average"] = fmt.Sprintf("%.2f / %.2f / %.2f（1、5、15 分钟）",
			snapshot.LoadAverage[0], snapshot.LoadAverage[1], snapshot.LoadAverage[2])
	}
	if snapshot.StorageTotalBytes > 0 {
		payload["disk"] = map[string]any{
			"path":      snapshot.StoragePath,
			"used":      humanBytes(snapshot.StorageUsedBytes),
			"total":     humanBytes(snapshot.StorageTotalBytes),
			"available": humanBytes(snapshot.StorageAvailableBytes),
			"percent":   snapshot.StorageUsagePercent,
		}
	}
	if len(snapshot.Temperatures) > 0 {
		payload["temperatures"] = readingStrings(snapshot.Temperatures)
	}
	if len(snapshot.Power) > 0 {
		payload["power"] = readingStrings(snapshot.Power)
	}
	if snapshot.Battery != nil {
		battery := fmt.Sprintf("%.0f%%", snapshot.Battery.Percent)
		if status := strings.TrimSpace(snapshot.Battery.Status); status != "" {
			battery += "（" + status + "）"
		}
		payload["battery"] = battery
	}
	// 读不到的原因如实带上，让模型说「这台机器读不到温度」而不是随口编一个数。
	for key, reason := range map[string]string{
		"metrics_unavailable":       snapshot.MetricsUnavailable,
		"diana_metrics_unavailable": snapshot.ProcessMetricsUnavailable,
		"disk_unavailable":          snapshot.StorageMetricsUnavailable,
		"temperature_unavailable":   snapshot.ThermalUnavailable,
		"power_unavailable":         snapshot.PowerUnavailable,
	} {
		if strings.TrimSpace(reason) != "" {
			payload[key] = reason
		}
	}
	return payload
}

func readingStrings(readings []hostinfo.Reading) []string {
	out := make([]string, 0, len(readings))
	for _, reading := range readings {
		out = append(out, fmt.Sprintf("%s %.1f%s", reading.Label, reading.Value, reading.Unit))
	}
	return out
}

// humanBytes 给出人话形式的体积；0 表示读不到，交由调用方决定要不要写进结果。
func humanBytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := uint64(unit), 0
	for n := value / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTP"[exp])
}

func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "刚启动"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
	default:
		return fmt.Sprintf("%d 分钟", minutes)
	}
}

// hostStatsStoragePath 是要统计容量的目录：数据目录所在的那块盘。
// 和 AgentWorkspaceDir 同源，用户关心的「磁盘还剩多少」问的就是这一块。
func hostStatsStoragePath() string {
	if dbPath := strings.TrimSpace(os.Getenv("APP_DB_PATH")); dbPath != "" {
		return filepath.Dir(dbPath)
	}
	return "."
}
