// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build linux

package hostinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSysfs 铺一棵假的 sysfs 树并把 sysfsRoot 指过去。
func fakeSysfs(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	previous := sysfsRoot
	sysfsRoot = root
	t.Cleanup(func() { sysfsRoot = previous })
	return root
}

func TestTemperaturesReadsHwmon(t *testing.T) {
	fakeSysfs(t, map[string]string{
		"class/hwmon/hwmon0/name":        "coretemp\n",
		"class/hwmon/hwmon0/temp1_input": "47500\n",
		"class/hwmon/hwmon0/temp1_label": "Package id 0\n",
		// 没有 label 的通道退回文件名。
		"class/hwmon/hwmon0/temp2_input": "39000\n",
		"class/hwmon/hwmon1/name":        "nvme\n",
		"class/hwmon/hwmon1/temp1_input": "31850\n",
	})
	readings, err := temperatures()
	if err != nil {
		t.Fatalf("temperatures() error = %v", err)
	}
	got := map[string]float64{}
	for _, reading := range readings {
		if reading.Unit != "°C" {
			t.Fatalf("unit = %q", reading.Unit)
		}
		got[reading.Label] = reading.Value
	}
	if got["coretemp Package id 0"] != 47.5 {
		t.Fatalf("readings = %#v", got)
	}
	if got["coretemp temp2"] != 39 {
		t.Fatalf("未带 label 的通道没有退回文件名: %#v", got)
	}
	if got["nvme temp1"] != 31.85 {
		t.Fatalf("readings = %#v", got)
	}
}

// 没接传感器的通道会返回 0 或极大值，那不是「机器 0 度」，要丢掉。
func TestTemperaturesDropsImpossibleReadings(t *testing.T) {
	fakeSysfs(t, map[string]string{
		"class/hwmon/hwmon0/name":        "acpi\n",
		"class/hwmon/hwmon0/temp1_input": "-60000\n",
		"class/hwmon/hwmon0/temp2_input": "250000\n",
		"class/hwmon/hwmon0/temp3_input": "不是数字\n",
	})
	if _, err := temperatures(); err == nil {
		t.Fatal("全是无效读数时仍然报告了温度")
	}
}

func TestTemperaturesReportsAbsence(t *testing.T) {
	fakeSysfs(t, map[string]string{"class/hwmon/.keep": ""})
	_, err := temperatures()
	if err == nil || !strings.Contains(err.Error(), "温度传感器") {
		t.Fatalf("err = %v", err)
	}
}

// RAPL 给的是累计能量，功率只能由两次采样的差值算出来：第一次必然没有区间。
func TestPowerDrawNeedsTwoSamples(t *testing.T) {
	root := fakeSysfs(t, map[string]string{
		"class/powercap/intel-rapl:0/name":      "package-0\n",
		"class/powercap/intel-rapl:0/energy_uj": "1000000\n",
	})
	raplState.Lock()
	raplState.byZone = map[string]raplSample{}
	raplState.Unlock()

	if _, _, err := powerDraw(); err == nil || !strings.Contains(err.Error(), "两次采样") {
		t.Fatalf("第一次采样应说明还算不出功率，err = %v", err)
	}

	// 一秒后能量涨了 5 焦，也就是 5W。
	zone := filepath.Join(root, "class/powercap/intel-rapl:0")
	raplState.Lock()
	raplState.byZone[zone] = raplSample{microJoules: 1000000, at: time.Now().Add(-time.Second)}
	raplState.Unlock()
	if err := os.WriteFile(filepath.Join(zone, "energy_uj"), []byte("6000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readings, _, err := powerDraw()
	if err != nil {
		t.Fatalf("powerDraw() error = %v", err)
	}
	if len(readings) != 1 || readings[0].Label != "package-0" || readings[0].Unit != "W" {
		t.Fatalf("readings = %#v", readings)
	}
	// 采样间隔由 time.Now 决定，不会正好是一秒，所以给一点余量。
	if readings[0].Value < 4.5 || readings[0].Value > 5.5 {
		t.Fatalf("功率 = %v W，期望 5W 上下", readings[0].Value)
	}
}

// 计数器回绕那一次的差值没有意义，跳过等下一次，而不是报一个巨大的负功率。
func TestPowerDrawSkipsCounterWraparound(t *testing.T) {
	root := fakeSysfs(t, map[string]string{
		"class/powercap/intel-rapl:0/name":      "package-0\n",
		"class/powercap/intel-rapl:0/energy_uj": "10\n",
	})
	zone := filepath.Join(root, "class/powercap/intel-rapl:0")
	raplState.Lock()
	raplState.byZone = map[string]raplSample{zone: {microJoules: 999999999, at: time.Now().Add(-time.Second)}}
	raplState.Unlock()
	if _, _, err := powerDraw(); err == nil {
		t.Fatal("回绕的那次采样被当成了有效功率")
	}
}

func TestReadBattery(t *testing.T) {
	fakeSysfs(t, map[string]string{
		"class/power_supply/AC/type":       "Mains\n",
		"class/power_supply/BAT0/type":     "Battery\n",
		"class/power_supply/BAT0/capacity": "88\n",
		"class/power_supply/BAT0/status":   "Discharging\n",
	})
	battery := readBattery()
	if battery == nil || battery.Percent != 88 || battery.Status != "Discharging" {
		t.Fatalf("battery = %#v", battery)
	}
}

// 没有 RAPL 但有电池时，电池仍然要报上来。
func TestPowerDrawReturnsBatteryWithoutRAPL(t *testing.T) {
	fakeSysfs(t, map[string]string{
		"class/power_supply/BAT0/type":     "Battery\n",
		"class/power_supply/BAT0/capacity": "42\n",
	})
	readings, battery, err := powerDraw()
	if err != nil {
		t.Fatalf("powerDraw() error = %v", err)
	}
	if len(readings) != 0 || battery == nil || battery.Percent != 42 {
		t.Fatalf("readings = %#v, battery = %#v", readings, battery)
	}
}
