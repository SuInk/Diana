// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/hostinfo"
)

func decodeHostStats(t *testing.T, snapshot hostinfo.Snapshot) map[string]any {
	t.Helper()
	body, err := json.Marshal(hostStatsPayload(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

// 读不到的项要如实说明原因，而不是留一个 0 让模型当成真实读数转述出去。
func TestHostStatsPayloadReportsUnavailableReasons(t *testing.T) {
	payload := decodeHostStats(t, hostinfo.Snapshot{
		OS: "linux", Arch: "amd64",
		ThermalUnavailable: "这台机器没有可读的温度传感器",
		PowerUnavailable:   "没有 RAPL，也没有电池",
	})
	if _, ok := payload["temperatures"]; ok {
		t.Fatal("读不到温度时仍然给出了温度字段")
	}
	if got, _ := payload["temperature_unavailable"].(string); !strings.Contains(got, "温度传感器") {
		t.Fatalf("temperature_unavailable = %v", payload["temperature_unavailable"])
	}
	if got, _ := payload["power_unavailable"].(string); !strings.Contains(got, "RAPL") {
		t.Fatalf("power_unavailable = %v", payload["power_unavailable"])
	}
}

// 有读数时按「标签 数值 单位」给出，模型直接转述即可。
func TestHostStatsPayloadFormatsReadings(t *testing.T) {
	payload := decodeHostStats(t, hostinfo.Snapshot{
		OS: "linux", Arch: "amd64",
		MemoryUsedBytes: 4 << 30, MemoryTotalBytes: 16 << 30, MemoryUsagePercent: 25,
		LoadAverage:  []float64{0.5, 1.25, 2},
		Temperatures: []hostinfo.Reading{{Label: "coretemp Package id 0", Value: 47.5, Unit: "°C"}},
		Power:        []hostinfo.Reading{{Label: "package-0", Value: 12.3, Unit: "W"}},
		Battery:      &hostinfo.Battery{Percent: 88, Status: "Discharging"},
	})
	if payload["memory_used"] != "4.0 GiB" || payload["memory_total"] != "16.0 GiB" {
		t.Fatalf("memory = %v / %v", payload["memory_used"], payload["memory_total"])
	}
	if load, _ := payload["load_average"].(string); !strings.HasPrefix(load, "0.50 / 1.25 / 2.00") {
		t.Fatalf("load_average = %v", payload["load_average"])
	}
	temps, _ := payload["temperatures"].([]any)
	if len(temps) != 1 || temps[0] != "coretemp Package id 0 47.5°C" {
		t.Fatalf("temperatures = %v", payload["temperatures"])
	}
	power, _ := payload["power"].([]any)
	if len(power) != 1 || power[0] != "package-0 12.3W" {
		t.Fatalf("power = %v", payload["power"])
	}
	if payload["battery"] != "88%（Discharging）" {
		t.Fatalf("battery = %v", payload["battery"])
	}
}

// 磁盘读不出来时不给一块「0 B / 0 B」的假盘。
func TestHostStatsPayloadOmitsDiskWhenUnknown(t *testing.T) {
	payload := decodeHostStats(t, hostinfo.Snapshot{OS: "linux", Arch: "amd64", StorageMetricsUnavailable: "statfs failed"})
	if _, ok := payload["disk"]; ok {
		t.Fatal("磁盘读不到时仍然给出了 disk 字段")
	}
	if payload["disk_unavailable"] != "statfs failed" {
		t.Fatalf("disk_unavailable = %v", payload["disk_unavailable"])
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1536, "1.5 KiB"},
		{4 << 30, "4.0 GiB"},
		{3 << 40, "3.0 TiB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Fatalf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "刚启动"},
		{90 * time.Second, "1 分钟"},
		{3 * time.Hour, "3 小时 0 分钟"},
		{50 * time.Hour, "2 天 2 小时"},
	} {
		if got := humanDuration(tc.in); got != tc.want {
			t.Fatalf("humanDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 主机名、磁盘路径、硬件型号不该对群里所有人可见。
func TestHostStatsToolIsOwnerOnly(t *testing.T) {
	if (RelationshipPolicy{}).allowedAgentToolNames()[dianaHostStatsToolName] {
		t.Fatal("diana.host_stats is reachable by non-owners")
	}
}
