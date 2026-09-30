// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"os"
	"strings"
	"sync"
	"time"
)

// defaultBotTimezone 是机器人没填时区、也没设 TZ、本机又是裸 UTC 时的兜底。
// Docker 镜像不带 TZ，进程本地时区就是 UTC；这时候把 UTC 当机器人的「本地时间」，
// 模型会报错钟点、回复时段整体偏八小时，而 UTC 服务器上的用户多半不在 UTC。
const defaultBotTimezone = "Asia/Shanghai"

var botLocationCache sync.Map // IANA 名 → *time.Location；LoadLocation 每次都要解压时区表

// systemLocation 是本机时区，测试里换掉它来模拟容器里的裸 UTC。
var systemLocation = func() *time.Location { return time.Local }

// loadBotLocation 解析 IANA 时区名，解析不了返回 nil。结果按名字缓存。
func loadBotLocation(name string) *time.Location {
	name = strings.TrimPrefix(strings.TrimSpace(name), ":")
	if name == "" {
		return nil
	}
	if cached, ok := botLocationCache.Load(name); ok {
		return cached.(*time.Location)
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	botLocationCache.Store(name, location)
	return location
}

// DefaultBotLocation 是机器人没填时区时的「本地时间」：先读 TZ 环境变量，再用本机
// 时区；本机时区是裸 UTC（容器默认值）时视为没配，按北京时间。
func DefaultBotLocation() *time.Location {
	if location := loadBotLocation(os.Getenv("TZ")); location != nil {
		return location
	}
	system := systemLocation()
	if name, _ := time.Now().In(system).Zone(); name != "UTC" {
		return system
	}
	if location := loadBotLocation(defaultBotTimezone); location != nil {
		return location
	}
	return system
}

// Location 是这台机器人的「本地时间」：注入的当前时间、回复时段、每日次数的日界线、
// 「明天八点」的换算和提示词里的时间戳都按它来。填错的时区名当没填，不让一个拼写
// 错误把时钟弄乱。
func (cfg BotConfig) Location() *time.Location {
	if location := loadBotLocation(cfg.Timezone); location != nil {
		return location
	}
	return DefaultBotLocation()
}

// formatZonedTime 按 layout 格式化并补上 UTC 偏移，如「2026-10-01 14:05:00（UTC+08:00）」。
// 只写缩写不够：CST 既是中国标准时间也是美国中部时间。
func formatZonedTime(t time.Time, layout string) string {
	_, offset := t.Zone()
	return t.Format(layout) + "（UTC" + formatUTCOffset(offset) + "）"
}

// profileTimezones 记着每台机器人填的时区（机器人 ID → IANA 名）。历史行、消息时间
// 这些时间戳由不持有 Runtime 的纯函数渲染，只拿得到事件上的机器人 ID，靠它查。
var profileTimezones sync.Map

func registerProfileTimezone(cfg BotConfig) {
	profileTimezones.Store(strings.TrimSpace(cfg.ID), strings.TrimSpace(cfg.Timezone))
}

// profileLocation 是某台机器人的时区；没登记过的按默认时区。
func profileLocation(profileID string) *time.Location {
	if name, ok := profileTimezones.Load(strings.TrimSpace(profileID)); ok {
		return BotConfig{Timezone: name.(string)}.Location()
	}
	return DefaultBotLocation()
}
