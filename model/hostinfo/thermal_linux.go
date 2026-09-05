// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build linux

package hostinfo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// temperatures 读 hwmon 下的温度传感器。
//
// 走 sysfs 而不是调 sensors 命令：lm-sensors 不一定装，而 /sys/class/hwmon 是
// 内核直接暴露的，容器里只要挂了 /sys 就能读。
// sysfsRoot 是 /sys 的挂载点，测试用假的 sysfs 树替换它。
//
// 温度和功率只有在真的有传感器的机器上才走得到那条路径，而 CI 和多数云主机
// 都没有；不能注入的话，这两段代码就只有「读不到」那一半被测过。
var sysfsRoot = "/sys"

func temperatures() ([]Reading, error) {
	entries, err := filepath.Glob(filepath.Join(sysfsRoot, "class", "hwmon", "hwmon*"))
	if err != nil || len(entries) == 0 {
		return nil, errors.New("这台机器没有可读的温度传感器（/sys/class/hwmon 为空）")
	}
	var readings []Reading
	for _, hwmon := range entries {
		chip := readTrimmed(filepath.Join(hwmon, "name"))
		inputs, _ := filepath.Glob(filepath.Join(hwmon, "temp*_input"))
		sort.Strings(inputs)
		for _, input := range inputs {
			raw := readTrimmed(input)
			if raw == "" {
				continue
			}
			milli, convErr := strconv.ParseFloat(raw, 64)
			if convErr != nil {
				continue
			}
			// hwmon 的温度单位是毫摄氏度。
			celsius := milli / 1000
			// 明显不可能的读数丢掉：有些芯片在没接传感器的通道上返回 0 或极大值。
			if celsius <= -50 || celsius >= 200 {
				continue
			}
			label := readTrimmed(strings.TrimSuffix(input, "_input") + "_label")
			readings = append(readings, Reading{
				Label: thermalLabel(chip, label, input),
				Value: RoundPercent(celsius),
				Unit:  "°C",
			})
		}
	}
	if len(readings) == 0 {
		return nil, errors.New("hwmon 里没有可用的温度读数")
	}
	return readings, nil
}

func thermalLabel(chip, label, inputPath string) string {
	name := strings.TrimSpace(label)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(inputPath), "_input")
	}
	if chip = strings.TrimSpace(chip); chip != "" {
		return chip + " " + name
	}
	return name
}

// raplSample 是上一次的能量计数，用来算两次之间的平均功率。
type raplSample struct {
	microJoules uint64
	at          time.Time
}

var raplState = struct {
	sync.Mutex
	byZone map[string]raplSample
}{byZone: map[string]raplSample{}}

// powerDraw 读 RAPL 能量计数算功率，并顺带读电池。
//
// RAPL 给的是累计能量（微焦），不是瞬时功率，所以功率只能由两次采样的差值除以
// 间隔得到——第一次调用必然没有上一笔，只能记下计数返回「还没有区间」。这不是
// 缺陷，是这个接口的形状；报一个假的瞬时值才是缺陷。
func powerDraw() ([]Reading, *Battery, error) {
	battery := readBattery()
	zones, err := filepath.Glob(filepath.Join(sysfsRoot, "class", "powercap", "intel-rapl:*"))
	if err != nil || len(zones) == 0 {
		if battery != nil {
			return nil, battery, nil
		}
		return nil, nil, errors.New("这台机器没有可读的功率计数（没有 RAPL，也没有电池）")
	}
	now := time.Now()
	var readings []Reading
	var pending int
	raplState.Lock()
	for _, zone := range zones {
		raw := readTrimmed(filepath.Join(zone, "energy_uj"))
		if raw == "" {
			continue
		}
		current, convErr := strconv.ParseUint(raw, 10, 64)
		if convErr != nil {
			continue
		}
		name := readTrimmed(filepath.Join(zone, "name"))
		if name == "" {
			name = filepath.Base(zone)
		}
		previous, ok := raplState.byZone[zone]
		raplState.byZone[zone] = raplSample{microJoules: current, at: now}
		if !ok {
			pending++
			continue
		}
		elapsed := now.Sub(previous.at).Seconds()
		// 计数器会回绕，回绕那一次的差值没有意义，跳过等下一次。
		if elapsed <= 0 || current < previous.microJoules {
			pending++
			continue
		}
		watts := float64(current-previous.microJoules) / 1e6 / elapsed
		readings = append(readings, Reading{Label: name, Value: RoundPercent(watts), Unit: "W"})
	}
	raplState.Unlock()
	if len(readings) == 0 {
		if battery != nil {
			return nil, battery, nil
		}
		if pending > 0 {
			return nil, nil, fmt.Errorf("RAPL 能量计数刚开始采集，还算不出功率（需要两次采样，%d 个功率域已记下第一次）", pending)
		}
		return nil, nil, errors.New("RAPL 功率域读不出有效计数")
	}
	return readings, battery, nil
}

func readBattery() *Battery {
	supplies, err := filepath.Glob(filepath.Join(sysfsRoot, "class", "power_supply", "*"))
	if err != nil {
		return nil
	}
	for _, supply := range supplies {
		if readTrimmed(filepath.Join(supply, "type")) != "Battery" {
			continue
		}
		raw := readTrimmed(filepath.Join(supply, "capacity"))
		if raw == "" {
			continue
		}
		percent, convErr := strconv.ParseFloat(raw, 64)
		if convErr != nil {
			continue
		}
		return &Battery{
			Percent: ClampPercent(RoundPercent(percent)),
			Status:  readTrimmed(filepath.Join(supply, "status")),
		}
	}
	return nil
}

func readTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
