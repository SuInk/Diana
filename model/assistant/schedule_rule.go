// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 周期订阅可以带一条日期规则，决定每个周期里落在哪几天，三种写法三选一：
//
//   - weekdays：每周的哪几天（每周一三五、工作日），interval 必须是整周（1w、2w）。
//   - month_days：每月的哪几号，1~31 是几号（没有这一天的月份取月末），-1 是
//     最后一天，-2 是倒数第二天；可以多个（每月 1 号和 15 号）。interval 是 mo/y。
//   - weekday + week：每月第几个星期几，week=1 是第一个，-1 是最后一个；第五个
//     周一这类不是每个月都有的，没有的月份跳过。interval 是 mo/y。
//
// 没有规则时按起点排：固定间隔落在 anchor + k*interval，按月的沿用起点那天的日子。
// 规则只管「哪几天」，几点、从哪周哪月开始、隔多久都来自 at 和 interval。
type scheduleDayRule struct {
	Weekdays  []string
	MonthDays []int
	Weekday   string
	Week      int
}

func (rule scheduleDayRule) IsZero() bool {
	return len(rule.Weekdays) == 0 && len(rule.MonthDays) == 0 && rule.Weekday == ""
}

func (rule scheduleDayRule) weekly() bool {
	return len(rule.Weekdays) > 0
}

var scheduleWeekdays = map[string]time.Weekday{
	"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday,
	"fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
}

var scheduleWeekdayNames = map[string]string{
	"mon": "周一", "tue": "周二", "wed": "周三", "thu": "周四", "fri": "周五", "sat": "周六", "sun": "周日",
}

var scheduleWeekdayOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

const scheduleWeek = 7 * 24 * time.Hour

const (
	scheduleWeekdaysDescription  = "按周重复时可选：每周的哪几天，例如每周一三五传 [\"mon\",\"wed\",\"fri\"]，工作日传 mon~fri 五天。interval 必须是整周（1w，隔周 2w）；必须传 at 定从哪周开始和几点触发。"
	scheduleMonthDaysDescription = "按月或按年重复时可选：每月的哪几号，可以多个，例如每月 1 号和 15 号传 [1,15]。1~31 是几号（没有这一天的月份取月末），-1 是最后一天，-2 是倒数第二天。必须传 at 定起始月份和几点触发。"
	scheduleWeekdayDescription   = "按月或按年重复时可选：配合 week 表示当月第几个星期几，例如 weekday=mon、week=1 是每月第一个周一。必须传 at。"
	scheduleWeekDescription      = "配合 weekday：第几个，1~5 从月初数，-1 是最后一个，-2 是倒数第二个。第五个这类不是每月都有的，没有的月份跳过。"
)

// ruleFromReminder 读出记录上的日期规则。
func ruleFromReminder(item Reminder) scheduleDayRule {
	return scheduleDayRule{
		Weekdays:  item.ScheduleWeekdays,
		MonthDays: item.ScheduleMonthDays,
		Weekday:   item.ScheduleWeekday,
		Week:      item.ScheduleWeekOrdinal,
	}
}

func setReminderDayRule(item *Reminder, rule scheduleDayRule) {
	item.ScheduleWeekdays, item.ScheduleMonthDays = rule.Weekdays, rule.MonthDays
	item.ScheduleWeekday, item.ScheduleWeekOrdinal = rule.Weekday, rule.Week
}

// parseScheduleDayRule 从工具参数里读规则。present 表示调用方至少给了其中一个键，
// update 据此判断要不要替换原规则：全部给空就是清掉规则。
func parseScheduleDayRule(input map[string]any) (rule scheduleDayRule, present bool, err error) {
	for _, key := range []string{"weekdays", "month_days", "weekday", "week"} {
		if _, ok := input[key]; ok {
			present = true
		}
	}
	if !present {
		return scheduleDayRule{}, false, nil
	}
	for _, raw := range configToolStringSlice(input, "weekdays") {
		rule.Weekdays = append(rule.Weekdays, strings.ToLower(raw))
	}
	rule.MonthDays, err = toolIntList(input["month_days"])
	if err != nil {
		return scheduleDayRule{}, true, fmt.Errorf("month_days 必须是整数数组")
	}
	rule.Weekday = strings.ToLower(strings.TrimSpace(configToolString(input, "weekday")))
	if raw := strings.TrimSpace(configToolString(input, "week")); raw != "" {
		rule.Week, err = strconv.Atoi(raw)
		if err != nil {
			return scheduleDayRule{}, true, fmt.Errorf("week 必须是整数")
		}
	}
	rule, err = normalizeScheduleDayRule(rule)
	return rule, true, err
}

// toolIntList 读整数数组：JSON 里是 []any（float64），也容忍单个数和「1,15」这样
// 的字符串。
func toolIntList(value any) ([]int, error) {
	var parts []string
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case []any:
		for _, item := range typed {
			parts = append(parts, strings.TrimSpace(fmt.Sprint(item)))
		}
	case []int:
		return append([]int(nil), typed...), nil
	case string:
		parts = strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || r == '，' || r == ' ' || r == '、' })
	default:
		parts = []string{strings.TrimSpace(fmt.Sprint(typed))}
	}
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		number, err := strconv.Atoi(part)
		if err != nil {
			return nil, err
		}
		out = append(out, number)
	}
	return out, nil
}

// normalizeScheduleDayRule 校验规则并去重排序：weekdays 按周一到周日，month_days
// 正数在前从小到大、负数在后从月初往月末（-2 在 -1 前）。
func normalizeScheduleDayRule(rule scheduleDayRule) (scheduleDayRule, error) {
	kinds := 0
	for _, used := range []bool{len(rule.Weekdays) > 0, len(rule.MonthDays) > 0, rule.Weekday != ""} {
		if used {
			kinds++
		}
	}
	if kinds > 1 {
		return scheduleDayRule{}, fmt.Errorf("weekdays、month_days、weekday 三者只能选一种")
	}
	if len(rule.Weekdays) > 0 {
		seen := map[string]bool{}
		for _, day := range rule.Weekdays {
			if _, ok := scheduleWeekdays[day]; !ok {
				return scheduleDayRule{}, fmt.Errorf("weekdays 只能是 mon、tue、wed、thu、fri、sat、sun")
			}
			seen[day] = true
		}
		ordered := make([]string, 0, len(seen))
		for _, day := range scheduleWeekdayOrder {
			if seen[day] {
				ordered = append(ordered, day)
			}
		}
		rule.Weekdays = ordered
	}
	if len(rule.MonthDays) > 0 {
		seen := map[int]bool{}
		days := make([]int, 0, len(rule.MonthDays))
		for _, day := range rule.MonthDays {
			if day == 0 || day > 31 || day < -31 {
				return scheduleDayRule{}, fmt.Errorf("month_days 每一项必须在 1~31 或 -1~-31 之间")
			}
			if !seen[day] {
				seen[day] = true
				days = append(days, day)
			}
		}
		sort.Slice(days, func(i, j int) bool {
			if (days[i] > 0) != (days[j] > 0) {
				return days[i] > 0
			}
			return days[i] < days[j]
		})
		rule.MonthDays = days
	}
	if rule.Weekday == "" {
		if rule.Week != 0 {
			return scheduleDayRule{}, fmt.Errorf("week 要和 weekday 一起用")
		}
		return rule, nil
	}
	if _, ok := scheduleWeekdays[rule.Weekday]; !ok {
		return scheduleDayRule{}, fmt.Errorf("weekday 只能是 mon、tue、wed、thu、fri、sat、sun")
	}
	if rule.Week == 0 || rule.Week > 5 || rule.Week < -5 {
		return scheduleDayRule{}, fmt.Errorf("week 必须在 1~5 或 -1~-5 之间")
	}
	return rule, nil
}

// checkRuleInterval 确认规则和间隔搭得上：按周的规则要整周间隔，按月的要 mo/y。
func checkRuleInterval(rule scheduleDayRule, interval calendarDuration) error {
	if rule.IsZero() {
		return nil
	}
	if rule.weekly() {
		if interval.Months != 0 || interval.Fixed <= 0 || interval.Fixed%scheduleWeek != 0 {
			return fmt.Errorf("weekdays 只能用于按整周重复（interval 写 1w、2w）")
		}
		return nil
	}
	if interval.Months == 0 {
		return fmt.Errorf("month_days、weekday 只能用于按月或按年重复（interval 写 mo 或 y）")
	}
	return nil
}

func monthDayLabel(day int) string {
	switch {
	case day == -1:
		return "最后一天"
	case day < 0:
		return fmt.Sprintf("倒数第 %d 天", -day)
	}
	return fmt.Sprintf("%d号", day)
}

// Describe 是规则里「哪几天」的说明，例如「周一、周三、周五」「1 号、15 号」
// 「第 1 个周一」。
func (rule scheduleDayRule) Describe() string {
	switch {
	case rule.weekly():
		names := make([]string, 0, len(rule.Weekdays))
		for _, day := range rule.Weekdays {
			names = append(names, scheduleWeekdayNames[day])
		}
		return strings.Join(names, "、")
	case len(rule.MonthDays) > 0:
		labels := make([]string, 0, len(rule.MonthDays))
		for _, day := range rule.MonthDays {
			labels = append(labels, monthDayLabel(day))
		}
		return strings.Join(labels, "、")
	case rule.Weekday != "" && rule.Week == -1:
		return "最后一个" + scheduleWeekdayNames[rule.Weekday]
	case rule.Weekday != "" && rule.Week < 0:
		return fmt.Sprintf("倒数第 %d 个%s", -rule.Week, scheduleWeekdayNames[rule.Weekday])
	case rule.Weekday != "":
		return fmt.Sprintf("第 %d 个%s", rule.Week, scheduleWeekdayNames[rule.Weekday])
	}
	return ""
}

// daysInMonthFor 算按月规则在某年某月落在哪几天，从小到大、去重（31 号和最后一天
// 在小月是同一天，只算一次）；一天都没有（第五个周一）时返回空。
func (rule scheduleDayRule) daysInMonthFor(year int, month time.Month) []int {
	days := daysInMonth(year, month)
	if len(rule.MonthDays) > 0 {
		seen := map[int]bool{}
		out := make([]int, 0, len(rule.MonthDays))
		for _, value := range rule.MonthDays {
			day := min(value, days)
			if value < 0 {
				day = max(days+value+1, 1)
			}
			if !seen[day] {
				seen[day] = true
				out = append(out, day)
			}
		}
		sort.Ints(out)
		return out
	}
	weekday := scheduleWeekdays[rule.Weekday]
	if rule.Week > 0 {
		first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).Weekday()
		day := 1 + (int(weekday)-int(first)+7)%7 + (rule.Week-1)*7
		if day > days {
			return nil
		}
		return []int{day}
	}
	last := time.Date(year, month, days, 0, 0, 0, 0, time.UTC).Weekday()
	day := days - (int(last)-int(weekday)+7)%7 + (rule.Week+1)*7
	if day < 1 {
		return nil
	}
	return []int{day}
}

// maximumRuleSearchSteps 限制往后找几个周期。第五个周一在按年重复、又恰好选在
// 2 月时要好几年才出现一次，这里放到 400 个周期，找不到就当规则不成立。
const maximumRuleSearchSteps = 400

// ruleSlotAfter 按规则在 anchor 起的各个周期里找日子，时刻沿用 anchor 的时分秒和
// 时区，返回第一个晚于 after、且（notBefore 非零时）不早于 notBefore 的时间。
// 按周的规则以 anchor 所在那一周（周一起算）为第 0 周，每隔 interval 周一轮；按月的
// 以 anchor 所在月份为第 0 个月。找不到返回零值。
func ruleSlotAfter(anchor time.Time, interval calendarDuration, rule scheduleDayRule, after, notBefore time.Time) time.Time {
	location := anchor.Location()
	after = after.In(location)
	hour, minute, second := anchor.Clock()
	at := func(year int, month time.Month, day int) time.Time {
		return time.Date(year, month, day, hour, minute, second, anchor.Nanosecond(), location)
	}
	accept := func(slot time.Time) bool {
		return slot.After(after) && (notBefore.IsZero() || !slot.Before(notBefore))
	}
	year, month, day := anchor.Date()
	if rule.weekly() {
		weeks := int(interval.Fixed / scheduleWeek)
		if weeks <= 0 {
			return time.Time{}
		}
		// 周一为一周的第一天：time.Weekday 里周日是 0，要挪到最后。
		weekStart := time.Date(year, month, day-(int(anchor.Weekday())+6)%7, 0, 0, 0, 0, location)
		elapsed := calendarDaysBetween(weekStart, after) / 7
		step := max(elapsed/weeks-1, 0)
		for limit := step + maximumRuleSearchSteps; step < limit; step++ {
			start := weekStart.AddDate(0, 0, step*weeks*7)
			for _, name := range rule.Weekdays {
				shift := (int(scheduleWeekdays[name]) + 6) % 7
				if slot := at(start.Year(), start.Month(), start.Day()+shift); accept(slot) {
					return slot
				}
			}
		}
		return time.Time{}
	}
	months := interval.Months
	if months <= 0 {
		return time.Time{}
	}
	elapsed := (after.Year()-year)*12 + int(after.Month()-month)
	step := max(elapsed/months-1, 0)
	for limit := step + maximumRuleSearchSteps; step < limit; step++ {
		first := time.Date(year, month+time.Month(step*months), 1, 0, 0, 0, 0, location)
		for _, candidate := range rule.daysInMonthFor(first.Year(), first.Month()) {
			if slot := at(first.Year(), first.Month(), candidate); accept(slot) {
				return slot
			}
		}
	}
	return time.Time{}
}

// calendarDaysBetween 是两个时刻之间相差的日历天数（按 from 的时区看日期），夏令
// 时那天少一小时也照样算一天。
func calendarDaysBetween(from, to time.Time) int {
	to = to.In(from.Location())
	fy, fm, fd := from.Date()
	ty, tm, td := to.Date()
	start := time.Date(fy, fm, fd, 0, 0, 0, 0, time.UTC)
	end := time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC)
	return int(end.Sub(start) / (24 * time.Hour))
}

// ScheduleRuleLabel 给 WebUI 用：周期订阅的日期规则说明，没有规则返回空串。
func ScheduleRuleLabel(item Reminder) string {
	return scheduleRuleLabel(item)
}

// scheduleRuleLabel 是列表里显示的规则，例如「每周一、周三、周五」「每月 1 号、
// 15 号」「每年 5 月第 2 个周日」；没有规则返回空串。
func scheduleRuleLabel(item Reminder) string {
	rule := ruleFromReminder(item)
	if rule.IsZero() {
		return ""
	}
	if rule.weekly() {
		weeks := int((time.Duration(item.IntervalSeconds) * time.Second) / scheduleWeek)
		if weeks <= 1 {
			return "每" + rule.Describe()
		}
		return fmt.Sprintf("每 %d 周的%s", weeks, rule.Describe())
	}
	switch {
	case item.IntervalMonths <= 0:
		return ""
	case item.IntervalMonths == 1:
		return "每月" + rule.Describe()
	case item.IntervalMonths == 12:
		return fmt.Sprintf("每年 %d 月%s", int(item.ScheduleAnchorAt.Month()), rule.Describe())
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
