// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 提醒和订阅的「哪天几点」可以不写 RFC3339，改传 date + time，由这里按自然日换算：
// 「明天」就是消息发出那天的下一个日历日，凌晨一点说的明天也是日历上的明天，不做
// 「凌晨算前一天」这类猜测。换算结果写进工具返回，模型据此向用户复述确认。
//
// 以前全靠模型自己把「后天」「26 号」算成 RFC3339：跨月、跨年、日子已过、发言者在
// 别的时区时都容易算错，而算错了代码里看不出来。
const (
	taskDateDescription = "哪天，由系统按自然日换算，不要自己算日期：today（今天）、tomorrow（明天）、day_after_tomorrow（后天）、" +
		"2026-09-28（完整日期）、09-28（今年这天，已过去取明年）、28（本月 28 号，已过去取下个月）。配合 time 使用，和 at、delay 二选一。"
	taskTimeDescription = "几点，24 小时制 HH:MM，例如 22:00、08:30。只给 time 不给 date 时取最近的那个时刻（今天已过就是明天）。和 at、delay 二选一。"
)

// taskClock 是换算「今天」「明天」用的基准：消息发出的时刻，和发言者所在时区。
type taskClock struct {
	Reference time.Time
	Location  *time.Location
}

// taskClockForEvent 取这条消息的换算基准。发言者画像里记过时区就用他的时区——他说的
// 「明天八点」是他那边的八点；否则用机器人这台机器的时区。基准时刻取消息发出时间，
// 回补的旧消息也按它当时的「今天」算。
func (r *Runtime) taskClockForEvent(event MessageEvent) taskClock {
	now := time.Now()
	if r != nil {
		now = r.clock()
	}
	location := now.Location()
	if event.userProfileLoaded {
		if speaker, _ := PortraitTimezoneWithRecordedAt(event.userProfile.Portrait); speaker != nil {
			location = speaker
		}
	}
	reference := now
	if event.Time > 0 {
		if sent := time.Unix(event.Time, 0); sent.Before(now) {
			reference = sent
		}
	}
	return taskClock{Reference: reference.In(location), Location: location}
}

// resolvedTaskTime 是 date/time 换算的结果。Note 是给模型复述用的一句话，例如
// 「明天 = 2026-09-27（周日）22:00，Asia/Shanghai」。
type resolvedTaskTime struct {
	At   time.Time
	Note string
}

// hasTaskDateTime 报告参数里有没有给 date 或 time。
func hasTaskDateTime(input map[string]any) bool {
	return strings.TrimSpace(configToolString(input, "date")) != "" || strings.TrimSpace(configToolString(input, "time")) != ""
}

// resolveTaskDateTime 把 date + time 换算成具体时刻。strictFuture 为真时，给了日期
// 却落在过去（今天 08:00 但已经十点了）直接报错，让模型回头问用户，而不是悄悄改日子；
// 周期订阅会自己顺延到下一个周期，传 false。
func resolveTaskDateTime(input map[string]any, clock taskClock, strictFuture bool) (resolvedTaskTime, error) {
	rawDate := strings.ToLower(strings.TrimSpace(configToolString(input, "date")))
	rawTime := strings.TrimSpace(configToolString(input, "time"))
	if rawTime == "" {
		return resolvedTaskTime{}, fmt.Errorf("给了 date 时必须同时给 time（HH:MM），用户没说几点就先问清楚")
	}
	hour, minute, err := parseTaskClockTime(rawTime)
	if err != nil {
		return resolvedTaskTime{}, err
	}
	reference := clock.Reference.In(clock.Location)
	year, month, day := reference.Date()
	at := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, hour, minute, 0, 0, clock.Location)
	}
	var result time.Time
	var label string
	switch rawDate {
	case "":
		// 只给时刻：最近的那一次，今天已过就是明天。
		result = at(year, month, day)
		label = "今天"
		if !result.After(reference) {
			result = at(year, month, day+1)
			label = "今天这个时刻已过，取明天"
		}
		return resolvedTaskTime{At: result, Note: taskTimeNote(label, result, clock.Location)}, nil
	case "today", "今天":
		result, label = at(year, month, day), "今天"
	case "tomorrow", "明天":
		result, label = at(year, month, day+1), "明天"
	case "day_after_tomorrow", "后天":
		result, label = at(year, month, day+2), "后天"
	default:
		result, label, err = resolveTaskCalendarDate(rawDate, reference, hour, minute, clock.Location)
		if err != nil {
			return resolvedTaskTime{}, err
		}
	}
	if strictFuture && !result.After(reference) {
		return resolvedTaskTime{}, fmt.Errorf("%s 已经过去了（现在是 %s），请和用户确认时间",
			result.Format("2006-01-02 15:04"), reference.Format("2006-01-02 15:04"))
	}
	return resolvedTaskTime{At: result, Note: taskTimeNote(label, result, clock.Location)}, nil
}

// resolveTaskCalendarDate 处理具体日期：2026-09-28、09-28、28。只写月日或只写几号
// 时，今年/本月这天已过就取明年/下个月；目标月份没有这一天（9 月 31 号）直接报错，
// 不去猜是月末还是下个月。
func resolveTaskCalendarDate(raw string, reference time.Time, hour, minute int, location *time.Location) (time.Time, string, error) {
	raw = strings.TrimSuffix(strings.TrimSuffix(raw, "号"), "日")
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == '-' || r == '/' || r == '.' })
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil {
			return time.Time{}, "", fmt.Errorf("date %q 格式不正确，%s", raw, taskDateFormatsHint)
		}
		numbers = append(numbers, number)
	}
	year, month, _ := reference.Date()
	build := func(y int, m time.Month, d int) (time.Time, error) {
		if m < 1 || m > 12 || d < 1 || d > daysInMonth(y, m) {
			return time.Time{}, fmt.Errorf("%d 年 %d 月没有 %d 号", y, int(m), d)
		}
		return time.Date(y, m, d, hour, minute, 0, 0, location), nil
	}
	switch len(numbers) {
	case 3:
		value, err := build(numbers[0], time.Month(numbers[1]), numbers[2])
		return value, "", err
	case 2:
		value, err := build(year, time.Month(numbers[0]), numbers[1])
		if err != nil {
			return time.Time{}, "", err
		}
		if !value.After(reference) {
			value, err = build(year+1, time.Month(numbers[0]), numbers[1])
			return value, "今年这天已过，取明年", err
		}
		return value, "", nil
	case 1:
		value, err := build(year, month, numbers[0])
		if err != nil {
			return time.Time{}, "", fmt.Errorf("%w；如果是指下个月，请传完整日期", err)
		}
		if !value.After(reference) {
			next := time.Date(year, month+1, 1, 0, 0, 0, 0, location)
			value, err = build(next.Year(), next.Month(), numbers[0])
			return value, "本月这天已过，取下个月", err
		}
		return value, "本月", nil
	}
	return time.Time{}, "", fmt.Errorf("date %q 格式不正确，%s", raw, taskDateFormatsHint)
}

const taskDateFormatsHint = "可用 today、tomorrow、day_after_tomorrow、2026-09-28、09-28 或 28"

func parseTaskClockTime(raw string) (int, int, error) {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "：", ":")
	parts := strings.Split(raw, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, fmt.Errorf("time %q 格式不正确，请用 24 小时制 HH:MM，例如 22:00", raw)
	}
	hour, hourErr := strconv.Atoi(parts[0])
	minute, minuteErr := strconv.Atoi(parts[1])
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("time %q 格式不正确，请用 24 小时制 HH:MM，例如 22:00", raw)
	}
	return hour, minute, nil
}

func taskTimeNote(label string, at time.Time, location *time.Location) string {
	text := at.Format("2006-01-02") + "（" + chineseWeekday(at.Weekday()) + "）" + at.Format("15:04")
	if label != "" {
		text = label + " = " + text
	}
	return "按自然日换算：" + text + "，时区 " + location.String() + "。回复用户时照这个日期复述确认。"
}

// applyTaskDateTime 在工具入口把 date/time 换算成 at，返回改写后的参数副本和每一项
// 的换算说明。换算放在入口统一做，下游的创建、修改逻辑照旧只认 at 和 delay。
// items 里的每一项各自换算；同一项里 date/time 不能和 at、delay 同时给。
func applyTaskDateTime(input map[string]any, clock taskClock, strictFuture bool) (map[string]any, []string, error) {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	var notes []string
	convert := func(item map[string]any, label string) error {
		if !hasTaskDateTime(item) {
			delete(item, "date")
			delete(item, "time")
			return nil
		}
		for _, key := range []string{"at", "trigger_at", "delay"} {
			if strings.TrimSpace(configToolString(item, key)) != "" {
				return fmt.Errorf("%sdate/time 与 %s 只能选一种", label, key)
			}
		}
		resolved, err := resolveTaskDateTime(item, clock, strictFuture)
		if err != nil {
			return fmt.Errorf("%s%w", label, err)
		}
		delete(item, "date")
		delete(item, "time")
		item["at"] = resolved.At.Format(time.RFC3339)
		notes = append(notes, label+resolved.Note)
		return nil
	}
	if err := convert(out, ""); err != nil {
		return nil, nil, err
	}
	if raw, ok := out["items"].([]any); ok {
		items := make([]any, len(raw))
		for index, value := range raw {
			entry, isMap := value.(map[string]any)
			if !isMap {
				items[index] = value
				continue
			}
			copied := make(map[string]any, len(entry))
			for key, field := range entry {
				copied[key] = field
			}
			if err := convert(copied, fmt.Sprintf("第 %d 项：", index+1)); err != nil {
				return nil, nil, err
			}
			items[index] = copied
		}
		out["items"] = items
	}
	return out, notes, nil
}
