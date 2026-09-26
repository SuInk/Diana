// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 工具参数里的时长统一用这套单位：s 秒、min 分钟、h 小时、d 天、w 周、m 月、y 年，
// 可以组合（1h30min、1y6m）。
//
// 注意 m 是月不是分钟，分钟一律写 min。以前这里直接用 time.ParseDuration，m 是分钟，
// 没法表达「每月」「每年」；现在 m 让给月，所以所有给模型看的说明、例子、报错都不能
// 再出现 Go 写法的 30m、1m0s——模型照抄回来就成了 30 个月。输出时长一律用
// formatDurationUnits，不要再用 time.Duration.String()。
//
// 月和年按日历算，不折成固定秒数：「每月」要落在每个月的同一天，1 月 31 日的下一格
// 是 2 月的最后一天，不是 3 月 3 日。
const durationUnitsHint = "s 秒、min 分钟、h 小时、d 天、w 周、m 月、y 年，可组合如 1h30min；注意 m 是月，分钟写 min"

// calendarDuration 是一段时长：Months（年已折成 12 个月）按日历加，Fixed 是固定长度。
type calendarDuration struct {
	Months int
	Fixed  time.Duration
}

func (d calendarDuration) IsZero() bool {
	return d.Months == 0 && d.Fixed == 0
}

// AddTo 从 t 起加上这段时长：先按日历加月，再加固定部分。
func (d calendarDuration) AddTo(t time.Time) time.Time {
	return addMonthsClamped(t, d.Months).Add(d.Fixed)
}

// Approximate 把整段时长折成固定长度，只给不认日历的地方用（检查间隔、兼容旧字段、
// 显示）：一年按 365 天，零头月份按 30 天。
func (d calendarDuration) Approximate() time.Duration {
	years, months := d.Months/12, d.Months%12
	return time.Duration(years)*365*24*time.Hour + time.Duration(months)*30*24*time.Hour + d.Fixed
}

func (d calendarDuration) String() string {
	var builder strings.Builder
	if years := d.Months / 12; years > 0 {
		builder.WriteString(strconv.Itoa(years) + "y")
	}
	if months := d.Months % 12; months > 0 {
		builder.WriteString(strconv.Itoa(months) + "m")
	}
	if d.Fixed > 0 || builder.Len() == 0 {
		builder.WriteString(formatDurationUnits(d.Fixed))
	}
	return builder.String()
}

var durationUnitAliases = map[string]string{
	"s": "s", "sec": "s", "secs": "s", "second": "s", "seconds": "s",
	"min": "min", "mins": "min", "minute": "min", "minutes": "min",
	"h": "h", "hr": "h", "hrs": "h", "hour": "h", "hours": "h",
	"d": "d", "day": "d", "days": "d",
	"w": "w", "wk": "w", "wks": "w", "week": "w", "weeks": "w",
	"m": "m", "mo": "m", "mon": "m", "month": "m", "months": "m",
	"y": "y", "yr": "y", "yrs": "y", "year": "y", "years": "y",
}

var fixedDurationUnits = map[string]time.Duration{
	"s":   time.Second,
	"min": time.Minute,
	"h":   time.Hour,
	"d":   24 * time.Hour,
	"w":   7 * 24 * time.Hour,
}

// parseDurationUnits 解析 s/min/h/d/w/m/y 写法。s 到 w 可以带小数（1.5h），月和年
// 只收整数：半个月没有日历意义。
func parseDurationUnits(raw string) (calendarDuration, error) {
	text := strings.ToLower(strings.Join(strings.Fields(raw), ""))
	if text == "" {
		return calendarDuration{}, fmt.Errorf("时长不能为空")
	}
	var result calendarDuration
	for position := 0; position < len(text); {
		start := position
		for position < len(text) && (text[position] >= '0' && text[position] <= '9' || text[position] == '.') {
			position++
		}
		number := text[start:position]
		unitStart := position
		for position < len(text) && text[position] >= 'a' && text[position] <= 'z' {
			position++
		}
		unit, known := durationUnitAliases[text[unitStart:position]]
		if number == "" || !known {
			return calendarDuration{}, fmt.Errorf("时长 %q 格式不正确，单位只支持 %s", raw, durationUnitsHint)
		}
		switch unit {
		case "m", "y":
			count, err := strconv.Atoi(number)
			if err != nil || count < 0 {
				return calendarDuration{}, fmt.Errorf("时长 %q 里的月和年只能是整数", raw)
			}
			if unit == "y" {
				count *= 12
			}
			result.Months += count
		default:
			value, err := strconv.ParseFloat(number, 64)
			if err != nil || value < 0 {
				return calendarDuration{}, fmt.Errorf("时长 %q 格式不正确，单位只支持 %s", raw, durationUnitsHint)
			}
			result.Fixed += time.Duration(value * float64(fixedDurationUnits[unit]))
		}
		if result.Months > 1200 || result.Fixed > 100*365*24*time.Hour {
			return calendarDuration{}, fmt.Errorf("时长 %q 太长", raw)
		}
	}
	if result.IsZero() {
		return calendarDuration{}, fmt.Errorf("时长必须大于 0")
	}
	return result, nil
}

// parseFixedDurationUnits 给只认固定长度的参数用（RSS/仓库检查间隔、事件触发冷却）：
// 月和年按 Approximate 折算。
func parseFixedDurationUnits(raw string) (time.Duration, error) {
	parsed, err := parseDurationUnits(raw)
	if err != nil {
		return 0, err
	}
	return parsed.Approximate(), nil
}

// formatDurationUnits 用同一套单位输出固定时长，从大到小拆：168h 是 1w，90min 是
// 1h30min。不输出 m 和 y：固定时长不代表日历月。
func formatDurationUnits(value time.Duration) string {
	if value <= 0 {
		return "0s"
	}
	var builder strings.Builder
	for _, unit := range []struct {
		name string
		size time.Duration
	}{{"w", 7 * 24 * time.Hour}, {"d", 24 * time.Hour}, {"h", time.Hour}, {"min", time.Minute}, {"s", time.Second}} {
		if count := value / unit.size; count > 0 {
			builder.WriteString(strconv.FormatInt(int64(count), 10) + unit.name)
			value -= count * unit.size
		}
	}
	if builder.Len() == 0 {
		return "0s"
	}
	return builder.String()
}

// addMonthsClamped 加 n 个月，日子超过目标月份天数时取该月最后一天。time.AddDate
// 会把 1 月 31 日加一个月规范化成 3 月 3 日，「每月 31 号」就这样滑走了。
func addMonthsClamped(t time.Time, months int) time.Time {
	if months == 0 {
		return t
	}
	year, month, day := t.Date()
	hour, minute, second := t.Clock()
	first := time.Date(year, month+time.Month(months), 1, hour, minute, second, t.Nanosecond(), t.Location())
	if last := daysInMonth(first.Year(), first.Month()); day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, hour, minute, second, t.Nanosecond(), t.Location())
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// calendarSlotAfter 返回 anchor 往后按 months 个月一格排出来的、第一个晚于 now 的
// 格子。每一格都从 anchor 直接算，不在上一格上累加，所以 31 号经过 2 月之后回到
// 3 月仍是 31 号。
func calendarSlotAfter(anchor time.Time, months int, now time.Time) time.Time {
	if anchor.After(now) || months <= 0 {
		return anchor
	}
	elapsed := (now.Year()-anchor.Year())*12 + int(now.Month()-anchor.Month())
	step := elapsed/months - 1
	if step < 1 {
		step = 1
	}
	for {
		slot := addMonthsClamped(anchor, step*months)
		if slot.After(now) {
			return slot
		}
		step++
	}
}
