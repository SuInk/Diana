// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	minimumScheduleInterval   = 1 * time.Minute
	maximumScheduleInterval   = 365 * 24 * time.Hour
	maximumScheduleQueryRunes = 2000
)

type dianaScheduleTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaScheduleResult struct {
	OK       bool            `json:"ok"`
	Action   string          `json:"action"`
	Message  string          `json:"message,omitempty"`
	Schedule *dianaSchedule  `json:"schedule,omitempty"`
	Items    []dianaSchedule `json:"items,omitempty"`
}

type dianaSchedule struct {
	ID                  string    `json:"id"`
	Query               string    `json:"query"`
	Interval            string    `json:"interval"`
	NextRunAt           time.Time `json:"next_run_at"`
	LastRunAt           time.Time `json:"last_run_at,omitempty"`
	Status              string    `json:"status"`
	CancelledAt         time.Time `json:"cancelled_at,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	PendingDelivery     bool      `json:"pending_delivery,omitempty"`
	PendingSince        time.Time `json:"pending_since,omitempty"`
	GroupID             string    `json:"group_id,omitempty"`
	UserID              string    `json:"user_id,omitempty"`
}

func newDianaScheduleTool(runtime *Runtime, event MessageEvent) *dianaScheduleTool {
	return &dianaScheduleTool{runtime: runtime, event: event}
}

func (t *dianaScheduleTool) Name() string {
	return "schedule"
}

func (t *dianaScheduleTool) Description() string {
	return `创建和管理持久化周期查询/订阅：按固定间隔重复执行一段查询或提醒并把结果通知用户，包括「每天早上八点提醒我」「每周日 22:00 提醒我睡觉」这类周期提醒——用 at 指定首次时间、interval 指定重复间隔。只执行一次的提醒改用 reminder。GitHub 仓库的 Commit、PR、Release、Star 更新订阅不属于本工具，也不能由聊天创建，只能提示用户去 WebUI 的「提醒与订阅」页面管理。禁止用 run_command、sleep 或后台进程代替。初识及以上可用。`
}

// InputSchema 声明参数契约。interval 的上下限直接引用校验用的同一份常量，
// 避免文案和校验代码各写一份数字然后漂移。
func (t *dianaScheduleTool) InputSchema() map[string]any {
	item := map[string]any{
		"interval": toolStringParam("重复间隔，只接受 Go 时长写法：30m、2h、24h（可组合成 1h30m）。不短于 " + minimumScheduleInterval.String() + "，不超过 " + maximumScheduleInterval.String() + "。"),
		"query":    toolStringParam("每次触发时要执行的查询要求，写成一句完整的自然语言指令；周期提醒就写到点要提醒什么。"),
		"at":       toolStringParam(scheduleAtDescription),
	}
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作。cancel 只停止并保留记录，delete 才彻底删除。",
			"create", "list", "update", "cancel", "delete"),
		"interval": item["interval"],
		"query":    item["query"],
		"at":       item["at"],
		"items": toolItemsParam("一次创建多个订阅；只在 create 时有效，最多 "+itoa(maximumTasksPerToolCall)+" 项。剩余额度不足时按顺序创建到额度上限。",
			maximumTasksPerToolCall, []string{"interval", "query"}, item),
		"id":             toolStringParam("要操作的订阅 ID；update、cancel、delete 必填，可先用 list 查到。"),
		"target_user_id": toolStringParam("代其他用户管理时的目标账号，仅机器人主人可用；创建仍占目标用户的额度。"),
	})
}

func (t *dianaScheduleTool) Run(_ context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana schedule: runtime is not configured")
	}
	// 按 id 改、取消、删除之前先认归属：别的机器人名下的任务一律按找不到处理，
	// 不管什么模式。见 taskOfOtherBot。
	if id := strings.TrimSpace(configToolString(input, "id")); id != "" && t.runtime.taskOfOtherBot(id, t.event) {
		return "", fmt.Errorf("没有找到任务 %s", id)
	}
	targetID, err := taskTargetUserID(context.Background(), t.runtime, t.event, input)
	if err != nil {
		return "", err
	}
	targetEvent := t.event
	targetEvent.UserID = targetID
	targetEvent.taskRequester = t.event.UserID
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	switch operation {
	case "create", "add":
		requests, err := parseScheduleCreateRequests(input)
		if err != nil {
			return "", err
		}
		items, err := t.runtime.addScheduledQueries(targetEvent, requests)
		if err != nil {
			return "", err
		}
		message := fmt.Sprintf("已创建并持久化 %d 个周期查询，将在到期后自动执行并发送到当前会话。", len(items))
		if len(items) < len(requests) {
			message = fmt.Sprintf("本次请求 %d 个周期查询，按剩余额度创建了 %d 个。", len(requests), len(items))
		}
		result := dianaScheduleResult{
			OK:      true,
			Action:  "created",
			Message: message,
			Items:   make([]dianaSchedule, 0, len(items)),
		}
		for _, item := range items {
			result.Items = append(result.Items, *scheduleForTool(item))
		}
		if len(items) == 1 {
			result.Schedule = scheduleForTool(items[0])
		}
		return marshalDianaScheduleResult(result)
	case "list":
		items := t.runtime.scheduledQueries(targetID)
		result := make([]dianaSchedule, 0, len(items))
		for _, item := range items {
			// 查别人的只看这台机器人名下的，理由同提醒列表。
			if !sameAccountID(targetID, t.event.UserID) && !t.runtime.sameBotAsEvent(item.ProfileID, t.event) {
				continue
			}
			result = append(result, *scheduleForTool(item))
		}
		return marshalDianaScheduleResult(dianaScheduleResult{
			OK:      true,
			Action:  "listed",
			Message: fmt.Sprintf("当前共有 %d 个周期查询。", len(result)),
			Items:   result,
		})
	case "update", "edit":
		id := strings.TrimSpace(configToolString(input, "id"))
		if id == "" {
			return "", fmt.Errorf("修改定时订阅时必须提供 id")
		}
		item, err := t.runtime.updateScheduledQuery(targetID, id, input)
		if err != nil {
			return "", err
		}
		return marshalDianaScheduleResult(dianaScheduleResult{
			OK:       true,
			Action:   "updated",
			Message:  "定时订阅已更新。",
			Schedule: scheduleForTool(item),
		})
	case "cancel":
		id := strings.TrimSpace(configToolString(input, "id"))
		if id == "" {
			return "", fmt.Errorf("取消定时订阅时必须提供 id")
		}
		item, err := t.runtime.cancelScheduledQuery(targetID, id)
		if err != nil {
			return "", err
		}
		return marshalDianaScheduleResult(dianaScheduleResult{
			OK:       true,
			Action:   "cancelled",
			Message:  "定时订阅已取消并释放额度，记录仍保留。",
			Schedule: scheduleForTool(item),
		})
	case "delete", "remove":
		id := strings.TrimSpace(configToolString(input, "id"))
		if id == "" {
			return "", fmt.Errorf("删除定时订阅时必须提供 id")
		}
		removed, err := t.runtime.deleteScheduledQuery(targetID, id)
		if err != nil {
			return "", err
		}
		if !removed {
			return "", fmt.Errorf("没有找到属于目标用户的定时订阅 %s", id)
		}
		return marshalDianaScheduleResult(dianaScheduleResult{
			OK:      true,
			Action:  "deleted",
			Message: "定时订阅已删除。",
		})
	default:
		return "", fmt.Errorf("operation 必须是 create、list、update、cancel 或 delete")
	}
}

// scheduleAtDescription 是 at 参数的说明，schedule 和 subscription 两处共用。
const scheduleAtDescription = "首次触发时间，RFC3339（例如 2026-09-27T22:00:00+08:00）。用户说了固定时间点（每天早上八点、每周日 22:00）时必须传，之后每隔 interval 在同一时间点重复；省略表示从现在起过一个 interval 首次触发。已经过去的时间会按 interval 顺延到下一个时间点。"

type scheduleCreateRequest struct {
	Interval time.Duration
	Query    string
	// FirstAt 是用户指定的首次触发时间，零值表示从现在起过一个 Interval。
	FirstAt time.Time
}

func parseScheduleCreateRequests(input map[string]any) ([]scheduleCreateRequest, error) {
	batch, batched, err := toolBatchItems(input)
	if err != nil {
		return nil, err
	}
	if !batched {
		batch = []map[string]any{input}
	}
	requests := make([]scheduleCreateRequest, 0, len(batch))
	for index, item := range batch {
		interval, err := parseScheduleInterval(configToolString(item, "interval"))
		if err != nil {
			return nil, fmt.Errorf("第 %d 个周期任务: %w", index+1, err)
		}
		query := strings.TrimSpace(configToolString(item, "query"))
		if query == "" {
			return nil, fmt.Errorf("第 %d 个周期任务 query 不能为空", index+1)
		}
		if len([]rune(query)) > maximumScheduleQueryRunes {
			return nil, fmt.Errorf("第 %d 个周期任务 query 不能超过 %d 个字符", index+1, maximumScheduleQueryRunes)
		}
		firstAt, err := parseScheduleFirstAt(configToolString(item, "at"))
		if err != nil {
			return nil, fmt.Errorf("第 %d 个周期任务: %w", index+1, err)
		}
		requests = append(requests, scheduleCreateRequest{Interval: interval, Query: query, FirstAt: firstAt})
	}
	return requests, nil
}

func parseScheduleInterval(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	interval, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("周期格式不正确，请使用 1m、2h、24h 这类格式")
	}
	if interval < minimumScheduleInterval {
		return 0, fmt.Errorf("周期不能短于 %s", minimumScheduleInterval)
	}
	if interval > maximumScheduleInterval {
		return 0, fmt.Errorf("周期不能超过 %s", maximumScheduleInterval)
	}
	return interval, nil
}

// parseScheduleFirstAt 解析首次触发时间，空串返回零值。
func parseScheduleFirstAt(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	at, err := parseReminderAt(raw)
	if err != nil {
		return time.Time{}, err
	}
	if at.After(time.Now().Add(maximumScheduleInterval)) {
		return time.Time{}, fmt.Errorf("首次触发时间不能晚于 %s 之后", maximumScheduleInterval)
	}
	return at, nil
}

// firstScheduleTrigger 算新订阅第一次什么时候跑，也就是它的时间网格原点。没给 at
// 就是现在起过一个 interval；给了就对齐到 at，已经过去的顺延到网格上的下一个格子，
// 所以周日 23 点说「每周日 22 点」，第一次是下周日 22 点，而不是立刻补跑一次。
func firstScheduleTrigger(firstAt time.Time, interval time.Duration, now time.Time) time.Time {
	if firstAt.IsZero() {
		return now.Add(interval)
	}
	return scheduleSlotAfter(firstAt, interval, now)
}

func (r *Runtime) addScheduledQuery(event MessageEvent, interval time.Duration, query string) (Reminder, error) {
	items, err := r.addScheduledQueries(event, []scheduleCreateRequest{{Interval: interval, Query: query}})
	if err != nil {
		return Reminder{}, err
	}
	return items[0], nil
}

func (r *Runtime) addScheduledQueries(event MessageEvent, requests []scheduleCreateRequest) ([]Reminder, error) {
	if r.reminders == nil {
		return nil, fmt.Errorf("当前未启用定时任务存储")
	}
	if len(requests) == 0 {
		return nil, fmt.Errorf("至少需要一个周期任务")
	}
	if len(requests) > maximumTasksPerToolCall {
		return nil, fmt.Errorf("一次最多创建 %d 个任务", maximumTasksPerToolCall)
	}
	policy := r.relationshipPolicy(context.Background(), event)
	limit := policy.personalScheduleLimit()
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	count := 0
	for _, item := range items {
		if reminderIsRecurring(item) && item.OwnerID == event.UserID && item.CancelledAt.IsZero() {
			count++
		}
	}
	remaining := limit - count
	if remaining <= 0 {
		return nil, fmt.Errorf("当前最多可创建 %d 个定时订阅，额度已满", limit)
	}
	if len(requests) > remaining {
		requests = requests[:remaining]
	}
	now := time.Now()
	created := make([]Reminder, 0, len(requests))
	for index, request := range requests {
		query := strings.TrimSpace(request.Query)
		if request.Interval < minimumScheduleInterval || request.Interval > maximumScheduleInterval {
			return nil, fmt.Errorf("第 %d 个周期任务间隔无效", index+1)
		}
		if query == "" || len([]rune(query)) > maximumScheduleQueryRunes {
			return nil, fmt.Errorf("第 %d 个周期任务 query 无效", index+1)
		}
		triggerAt := firstScheduleTrigger(request.FirstAt, request.Interval, now)
		created = append(created, Reminder{
			ID:               uuid.NewString()[:8],
			Kind:             ReminderKindQuery,
			Platform:         event.Platform,
			ProfileID:        event.ProfileID,
			ContextNamespace: event.ContextNamespace,
			OwnerID:          event.UserID,
			GroupID:          event.GroupID,
			UserID:           event.UserID,
			RequestedBy:      firstNonEmpty(event.taskRequester, event.UserID),
			Message:          query,
			TriggerAt:        triggerAt,
			IntervalSeconds:  int64(request.Interval / time.Second),
			ScheduleAnchorAt: triggerAt,
			CreatedAt:        now,
		})
	}
	if err := r.reminders.SaveReminders(append(items, created...)); err != nil {
		return nil, fmt.Errorf("保存定时订阅失败: %w", err)
	}
	return created, nil
}

func (r *Runtime) scheduledQueries(ownerID string) []Reminder {
	if r.reminders == nil {
		return nil
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	out := make([]Reminder, 0, len(items))
	for _, item := range items {
		if reminderIsScheduledQuery(item) && item.OwnerID == ownerID {
			out = append(out, item)
		}
	}
	return out
}

func (r *Runtime) deleteScheduledQuery(ownerID string, id string) (bool, error) {
	if r.reminders == nil {
		return false, fmt.Errorf("当前未启用定时任务存储")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	next := make([]Reminder, 0, len(items))
	removed := false
	for _, item := range items {
		if reminderIsScheduledQuery(item) && item.OwnerID == ownerID && item.ID == id {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if !removed {
		return false, nil
	}
	if err := r.reminders.SaveReminders(next); err != nil {
		return false, fmt.Errorf("删除定时订阅失败: %w", err)
	}
	return true, nil
}

func (r *Runtime) cancelScheduledQuery(ownerID string, id string) (Reminder, error) {
	if r.reminders == nil {
		return Reminder{}, fmt.Errorf("当前未启用定时任务存储")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	for index := range items {
		item := &items[index]
		if !reminderIsScheduledQuery(*item) || item.OwnerID != ownerID || item.ID != id {
			continue
		}
		if !item.CancelledAt.IsZero() {
			return Reminder{}, fmt.Errorf("定时订阅 %s 已经取消", id)
		}
		item.CancelledAt = time.Now()
		item.PendingDelivery = ""
		item.PendingSince = time.Time{}
		if err := r.reminders.SaveReminders(items); err != nil {
			return Reminder{}, fmt.Errorf("取消定时订阅失败: %w", err)
		}
		return *item, nil
	}
	return Reminder{}, fmt.Errorf("没有找到属于当前用户的定时订阅 %s", id)
}

func (r *Runtime) updateScheduledQuery(ownerID string, id string, input map[string]any) (Reminder, error) {
	if r.reminders == nil {
		return Reminder{}, fmt.Errorf("当前未启用定时任务存储")
	}
	rawInterval := strings.TrimSpace(configToolString(input, "interval"))
	query := strings.TrimSpace(configToolString(input, "query"))
	firstAt, err := parseScheduleFirstAt(configToolString(input, "at"))
	if err != nil {
		return Reminder{}, err
	}
	if rawInterval == "" && query == "" && firstAt.IsZero() {
		return Reminder{}, fmt.Errorf("修改定时订阅时至少提供 interval、at 或 query")
	}
	var interval time.Duration
	if rawInterval != "" {
		interval, err = parseScheduleInterval(rawInterval)
		if err != nil {
			return Reminder{}, err
		}
	}
	if len([]rune(query)) > maximumScheduleQueryRunes {
		return Reminder{}, fmt.Errorf("定时订阅查询不能超过 %d 个字符", maximumScheduleQueryRunes)
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	for index := range items {
		item := &items[index]
		if !reminderIsScheduledQuery(*item) || item.OwnerID != ownerID || item.ID != id {
			continue
		}
		if !item.CancelledAt.IsZero() {
			return Reminder{}, fmt.Errorf("定时订阅 %s 已取消，不能修改；请新建订阅", id)
		}
		now := time.Now()
		if rawInterval != "" {
			item.IntervalSeconds = int64(interval / time.Second)
		}
		current := time.Duration(item.IntervalSeconds) * time.Second
		if query != "" {
			item.Message = query
			item.PendingDelivery = ""
			item.PendingSince = time.Time{}
			item.LastError = ""
			item.ConsecutiveFailures = 0
		}
		switch {
		case !firstAt.IsZero() || rawInterval != "":
			// 改了时间或间隔就重新定网格原点；只改间隔没给 at 时从现在起算，和新建一致。
			item.TriggerAt = firstScheduleTrigger(firstAt, current, now)
			item.ScheduleAnchorAt = item.TriggerAt
		case !item.ScheduleAnchorAt.IsZero():
			// 只改查询内容：时间点不动，落回原来网格上的下一格。
			item.TriggerAt = scheduleSlotAfter(item.ScheduleAnchorAt, current, now)
		default:
			item.TriggerAt = now.Add(current)
		}
		if err := r.reminders.SaveReminders(items); err != nil {
			return Reminder{}, fmt.Errorf("修改定时订阅失败: %w", err)
		}
		return *item, nil
	}
	return Reminder{}, fmt.Errorf("没有找到属于目标用户的定时订阅 %s", id)
}

func reminderIsScheduledQuery(item Reminder) bool {
	return item.Kind == ReminderKindQuery && item.IntervalSeconds > 0
}

func scheduleForTool(item Reminder) *dianaSchedule {
	return &dianaSchedule{
		ID:                  item.ID,
		Query:               item.Message,
		Interval:            (time.Duration(item.IntervalSeconds) * time.Second).String(),
		NextRunAt:           item.TriggerAt,
		LastRunAt:           item.LastRunAt,
		Status:              scheduleStatus(item),
		CancelledAt:         item.CancelledAt,
		LastError:           item.LastError,
		ConsecutiveFailures: item.ConsecutiveFailures,
		PendingDelivery:     strings.TrimSpace(item.PendingDelivery) != "",
		PendingSince:        item.PendingSince,
		GroupID:             item.GroupID,
		UserID:              item.UserID,
	}
}

func scheduleStatus(item Reminder) string {
	if !item.CancelledAt.IsZero() {
		return "cancelled"
	}
	if item.ConsecutiveFailures > 0 {
		return "retrying"
	}
	return "active"
}

func marshalDianaScheduleResult(result dianaScheduleResult) (string, error) {
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// CanonicalOperation 是 Run 实际执行的操作；投递目标不是当前会话时带 _elsewhere，
// 见 taskCanonicalOperation。
func (t *dianaScheduleTool) CanonicalOperation(input map[string]any) string {
	return t.runtime.taskCanonicalOperation(t.event, input, "")
}
