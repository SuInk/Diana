// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 按月、按年的周期订阅可以带一条日期规则，决定每次落在当月哪一天：
//
//   - month_day：第几天。1~31 是几号，没有这一天的月份取月末；-1 是最后一天，
//     -2 是倒数第二天，以此类推。
//   - weekday + week：第几个星期几。week=1 是第一个，-1 是最后一个；第五个周一
//     这类不是每个月都有的，没有的月份跳过。
//
// 没有规则时沿用起点那天的日子（31 号在小月取月末），见 calendarSlotAfter。
// 规则只管「哪一天」，几点、从哪个月开始、隔几个月都来自 at 和 interval。
type scheduleDayRule struct {
	MonthDay int
	Weekday  string
	Week     int
}

func (rule scheduleDayRule) IsZero() bool {
	return rule.MonthDay == 0 && rule.Weekday == ""
}

var scheduleWeekdays = map[string]time.Weekday{
	"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday,
	"fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
}

var scheduleWeekdayNames = map[string]string{
	"mon": "周一", "tue": "周二", "wed": "周三", "thu": "周四", "fri": "周五", "sat": "周六", "sun": "周日",
}

var scheduleWeekdayOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

const (
	scheduleMonthDayDescription = "按月或按年重复时可选：每次落在当月第几天。1~31 是几号（没有这一天的月份取月末），-1 是最后一天，-2 是倒数第二天。和 weekday 二选一；用了就必须传 at 定起始月份和时刻。"
	scheduleWeekdayDescription  = "按月或按年重复时可选：配合 week 表示当月第几个星期几，例如 weekday=mon、week=1 是每月第一个周一。和 month_day 二选一；用了就必须传 at。"
	scheduleWeekDescription     = "配合 weekday：第几个，1~5 从月初数，-1 是最后一个，-2 是倒数第二个。第五个这类不是每月都有的，没有的月份跳过。"
)

// ruleFromReminder 读出记录上的日期规则。
func ruleFromReminder(item Reminder) scheduleDayRule {
	return scheduleDayRule{MonthDay: item.ScheduleMonthDay, Weekday: item.ScheduleWeekday, Week: item.ScheduleWeekOrdinal}
}

func setReminderDayRule(item *Reminder, rule scheduleDayRule) {
	item.ScheduleMonthDay, item.ScheduleWeekday, item.ScheduleWeekOrdinal = rule.MonthDay, rule.Weekday, rule.Week
}

// parseScheduleDayRule 从工具参数里读规则。present 表示调用方至少给了其中一个键，
// update 据此判断要不要替换原规则：month_day=0 且 weekday 为空就是清掉规则。
func parseScheduleDayRule(input map[string]any) (rule scheduleDayRule, present bool, err error) {
	_, hasMonthDay := input["month_day"]
	_, hasWeekday := input["weekday"]
	_, hasWeek := input["week"]
	present = hasMonthDay || hasWeekday || hasWeek
	if !present {
		return scheduleDayRule{}, false, nil
	}
	if raw := strings.TrimSpace(configToolString(input, "month_day")); raw != "" {
		rule.MonthDay, err = strconv.Atoi(raw)
		if err != nil {
			return scheduleDayRule{}, true, fmt.Errorf("month_day 必须是整数")
		}
	}
	rule.Weekday = strings.ToLower(strings.TrimSpace(configToolString(input, "weekday")))
	if raw := strings.TrimSpace(configToolString(input, "week")); raw != "" {
		rule.Week, err = strconv.Atoi(raw)
		if err != nil {
			return scheduleDayRule{}, true, fmt.Errorf("week 必须是整数")
		}
	}
	return rule, true, validateScheduleDayRule(rule)
}

func validateScheduleDayRule(rule scheduleDayRule) error {
	if rule.MonthDay != 0 && rule.Weekday != "" {
		return fmt.Errorf("month_day 和 weekday 只能二选一")
	}
	if rule.MonthDay > 31 || rule.MonthDay < -31 {
		return fmt.Errorf("month_day 必须在 1~31 或 -1~-31 之间")
	}
	if rule.Weekday == "" {
		if rule.Week != 0 {
			return fmt.Errorf("week 要和 weekday 一起用")
		}
		return nil
	}
	if _, ok := scheduleWeekdays[rule.Weekday]; !ok {
		return fmt.Errorf("weekday 只能是 mon、tue、wed、thu、fri、sat、sun")
	}
	if rule.Week == 0 || rule.Week > 5 || rule.Week < -5 {
		return fmt.Errorf("week 必须在 1~5 或 -1~-5 之间")
	}
	return nil
}

// Describe 是给人看的规则说明，例如「最后一天」「第 1 个周一」。
func (rule scheduleDayRule) Describe() string {
	switch {
	case rule.MonthDay == -1:
		return "最后一天"
	case rule.MonthDay < 0:
		return fmt.Sprintf("倒数第 %d 天", -rule.MonthDay)
	case rule.MonthDay > 0:
		return fmt.Sprintf("%d 号", rule.MonthDay)
	case rule.Weekday != "" && rule.Week == -1:
		return "最后一个" + scheduleWeekdayNames[rule.Weekday]
	case rule.Weekday != "" && rule.Week < 0:
		return fmt.Sprintf("倒数第 %d 个%s", -rule.Week, scheduleWeekdayNames[rule.Weekday])
	case rule.Weekday != "":
		return fmt.Sprintf("第 %d 个%s", rule.Week, scheduleWeekdayNames[rule.Weekday])
	}
	return ""
}

// dayInMonth 算规则在某年某月落在哪一天；这个月没有（第五个周一）时 ok 为 false。
func (rule scheduleDayRule) dayInMonth(year int, month time.Month) (int, bool) {
	days := daysInMonth(year, month)
	switch {
	case rule.MonthDay > 0:
		return min(rule.MonthDay, days), true
	case rule.MonthDay < 0:
		return max(days+rule.MonthDay+1, 1), true
	}
	weekday := scheduleWeekdays[rule.Weekday]
	if rule.Week > 0 {
		first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).Weekday()
		day := 1 + (int(weekday)-int(first)+7)%7 + (rule.Week-1)*7
		return day, day <= days
	}
	last := time.Date(year, month, days, 0, 0, 0, 0, time.UTC).Weekday()
	day := days - (int(last)-int(weekday)+7)%7 + (rule.Week+1)*7
	return day, day >= 1
}

// maximumRuleSearchSteps 限制往后找几个周期。第五个周一在按年重复、又恰好选在
// 2 月时要好几年才出现一次，这里放到 400 个周期，找不到就当规则不成立。
const maximumRuleSearchSteps = 400

// ruleSlotAfter 在 anchor 所在月份起、每隔 months 个月的那些月份里，按规则取日子，
// 时刻沿用 anchor 的时分秒和时区，返回第一个晚于 after 的时间；notBefore 非零时还
// 不能早于它。找不到返回零值。
func ruleSlotAfter(anchor time.Time, months int, rule scheduleDayRule, after, notBefore time.Time) time.Time {
	if months <= 0 {
		return time.Time{}
	}
	year, month, _ := anchor.Date()
	hour, minute, second := anchor.Clock()
	elapsed := (after.Year()-year)*12 + int(after.Month()-month)
	step := elapsed/months - 1
	if step < 0 {
		step = 0
	}
	for limit := step + maximumRuleSearchSteps; step < limit; step++ {
		first := time.Date(year, month+time.Month(step*months), 1, 0, 0, 0, 0, anchor.Location())
		day, ok := rule.dayInMonth(first.Year(), first.Month())
		if !ok {
			continue
		}
		slot := time.Date(first.Year(), first.Month(), day, hour, minute, second, anchor.Nanosecond(), anchor.Location())
		if slot.After(after) && (notBefore.IsZero() || !slot.Before(notBefore)) {
			return slot
		}
	}
	return time.Time{}
}

// ScheduleRuleLabel 给 WebUI 用：周期订阅的日期规则说明，没有规则返回空串。
func ScheduleRuleLabel(item Reminder) string {
	return scheduleRuleLabel(item)
}

// scheduleRuleLabel 是列表里显示的规则，例如「每月最后一天」「每年第 2 个周日」；
// 没有规则返回空串。
func scheduleRuleLabel(item Reminder) string {
	rule := ruleFromReminder(item)
	if rule.IsZero() || item.IntervalMonths <= 0 {
		return ""
	}
	switch {
	case item.IntervalMonths == 1:
		return "每月" + rule.Describe()
	case item.IntervalMonths == 12:
		return "每年 " + strconv.Itoa(int(item.ScheduleAnchorAt.Month())) + " 月" + rule.Describe()
	case item.IntervalMonths%12 == 0:
		return fmt.Sprintf("每 %d 年的 %d 月%s", item.IntervalMonths/12, int(item.ScheduleAnchorAt.Month()), rule.Describe())
	}
	return fmt.Sprintf("每 %d 个月的%s", item.IntervalMonths, rule.Describe())
}

// scheduleEveryLabel 是列表里「多久一次」那一栏：有日期规则的直接写规则，没有的写
// 「每 1d」这样的间隔。
func scheduleEveryLabel(item Reminder) string {
	if label := scheduleRuleLabel(item); label != "" {
		return label
	}
	return "每 " + reminderScheduleInterval(item).String()
}
