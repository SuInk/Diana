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
	minimumRSSWatchInterval = 5 * time.Minute
	defaultRSSWatchInterval = 15 * time.Minute
	maximumRSSJudgeRunes    = 2000
)

type RSSWatchCreateInput struct {
	FeedURL, TwitterHandle, JudgePrompt                             string
	Interval                                                        time.Duration
	Platform, ProfileID, ContextNamespace, OwnerID, GroupID, UserID string
}

type RSSWatchUpdateInput struct {
	FeedURL, TwitterHandle, JudgePrompt *string
	Interval                            time.Duration
}

type dianaRSSWatchTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaRSSWatchResult struct {
	OK      bool            `json:"ok"`
	Action  string          `json:"action"`
	Message string          `json:"message,omitempty"`
	Watch   *dianaRSSWatch  `json:"watch,omitempty"`
	Items   []dianaRSSWatch `json:"items,omitempty"`
}

type dianaRSSWatch struct {
	ID              string    `json:"id"`
	FeedURL         string    `json:"feed_url"`
	Source          string    `json:"source"`
	TwitterHandle   string    `json:"twitter_handle,omitempty"`
	JudgePrompt     string    `json:"judge_prompt"`
	Interval        string    `json:"interval"`
	Status          string    `json:"status"`
	LastError       string    `json:"last_error,omitempty"`
	NextRunAt       time.Time `json:"next_run_at"`
	LastRunAt       time.Time `json:"last_run_at,omitempty"`
	PendingDelivery bool      `json:"pending_delivery,omitempty"`
}

func newDianaRSSWatchTool(runtime *Runtime, event MessageEvent) *dianaRSSWatchTool {
	return &dianaRSSWatchTool{runtime: runtime, event: event}
}

func (*dianaRSSWatchTool) Name() string { return "diana.rss" }

func (*dianaRSSWatchTool) Description() string {
	return `创建和管理 RSS/Atom 或 X (Twitter) 用户订阅：发现新条目后由模型按 judge_prompt 判断是否值得通知，不符合条件就保持静默。用户要求持续关注某个网站 Feed 或某个推特用户、并且只在特定内容出现时才通知，必须使用本工具；普通周期搜索改用 diana.schedule。首次创建只建立当前内容基线，不补发历史条目。`
}

func (*dianaRSSWatchTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作。cancel 只停止并保留记录，delete 才彻底删除。",
			"create", "list", "update", "cancel", "delete"),
		"twitter_handle": toolStringParam("要关注的 X (Twitter) 用户名，不带 @。create 时必须且只能提供它或 feed_url 之一。"),
		"feed_url":       toolStringParam("要关注的 RSS/Atom feed 地址。create 时必须且只能提供它或 twitter_handle 之一。"),
		"interval":       toolStringParam("检查间隔，只接受 Go 时长写法：15m、1h。不短于 " + minimumRSSWatchInterval.String() + "，省略按 " + defaultRSSWatchInterval.String() + " 处理。"),
		"judge_prompt":   toolStringParam("判断条件：写清楚什么样的新条目才值得通知、通知时要说什么。最多 " + itoa(maximumRSSJudgeRunes) + " 个字符。例如「仅当推文明确提到额度重置、恢复或刷新时通知，并用中文说明时间和原文链接」。"),
		"id":             toolStringParam("要操作的订阅 ID；update、cancel、delete 必填，可先用 list 查到。"),
	})
}

func (t *dianaRSSWatchTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana rss: runtime is not configured")
	}
	policy := t.runtime.relationshipPolicy(ctx, t.event)
	if !policy.AllowPersonalSchedule {
		return "", fmt.Errorf("好感度不足：当前关系等级为“%s”，尚未解锁个人订阅", policy.Name)
	}
	targetID, err := taskTargetUserID(ctx, t.runtime, t.event, input)
	if err != nil {
		return "", err
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		operation = "create"
	}
	switch operation {
	case "create", "add":
		if err := t.runtime.ensureRecurringTaskCapacity(targetID, policy.personalScheduleLimit()); err != nil {
			return "", err
		}
		interval, err := parseRSSWatchInterval(configToolString(input, "interval"))
		if err != nil {
			return "", err
		}
		item, err := t.runtime.CreateRSSWatch(ctx, RSSWatchCreateInput{
			FeedURL: configToolString(input, "feed_url"), TwitterHandle: configToolString(input, "twitter_handle"),
			JudgePrompt: configToolString(input, "judge_prompt"), Interval: interval,
			Platform: t.event.Platform, ProfileID: t.event.ProfileID, ContextNamespace: t.event.ContextNamespace,
			OwnerID: targetID, GroupID: t.event.GroupID, UserID: targetID,
		})
		if err != nil {
			return "", err
		}
		return marshalDianaRSSWatchResult(dianaRSSWatchResult{OK: true, Action: "created", Message: "订阅已创建，当前 Feed 已作为基线；后续新条目会先经判断器决定是否回复。", Watch: rssWatchForTool(item)})
	case "list":
		stored := t.runtime.rssWatches(targetID)
		items := make([]dianaRSSWatch, 0, len(stored))
		for _, item := range stored {
			items = append(items, *rssWatchForTool(item))
		}
		return marshalDianaRSSWatchResult(dianaRSSWatchResult{OK: true, Action: "listed", Message: fmt.Sprintf("当前共有 %d 个 RSS/社交订阅。", len(items)), Items: items})
	case "update", "edit":
		id := strings.TrimSpace(configToolString(input, "id"))
		if id == "" {
			return "", fmt.Errorf("修改 RSS 订阅时必须提供 id")
		}
		update, err := rssWatchUpdateFromTool(input)
		if err != nil {
			return "", err
		}
		item, err := t.runtime.UpdateRSSWatch(ctx, targetID, id, update)
		if err != nil {
			return "", err
		}
		return marshalDianaRSSWatchResult(dianaRSSWatchResult{OK: true, Action: "updated", Message: "RSS 订阅已更新。", Watch: rssWatchForTool(item)})
	case "cancel":
		id := strings.TrimSpace(configToolString(input, "id"))
		if id == "" {
			return "", fmt.Errorf("取消 RSS 订阅时必须提供 id")
		}
		item, err := t.runtime.CancelRSSWatch(targetID, id)
		if err != nil {
			return "", err
		}
		return marshalDianaRSSWatchResult(dianaRSSWatchResult{OK: true, Action: "cancelled", Message: "RSS 订阅已取消。", Watch: rssWatchForTool(item)})
	case "delete", "remove":
		id := strings.TrimSpace(configToolString(input, "id"))
		if id == "" {
			return "", fmt.Errorf("删除 RSS 订阅时必须提供 id")
		}
		removed, err := t.runtime.DeleteRSSWatch(targetID, id)
		if err != nil {
			return "", err
		}
		if !removed {
			return "", fmt.Errorf("没有找到 RSS 订阅 %s", id)
		}
		return marshalDianaRSSWatchResult(dianaRSSWatchResult{OK: true, Action: "deleted", Message: "RSS 订阅已删除。"})
	default:
		return "", fmt.Errorf("operation 必须是 create、list、update、cancel 或 delete")
	}
}

func (r *Runtime) ensureRecurringTaskCapacity(ownerID string, limit int) error {
	if r.reminders == nil {
		return fmt.Errorf("当前未启用定时任务存储")
	}
	if limit <= 0 {
		return fmt.Errorf("当前关系等级没有个人定时订阅权限")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	count := 0
	for _, item := range r.reminders.Reminders() {
		if reminderIsRecurring(item) && item.OwnerID == ownerID && item.CancelledAt.IsZero() {
			count++
		}
	}
	if count >= limit {
		return fmt.Errorf("当前关系等级最多创建 %d 个定时订阅，额度已满", limit)
	}
	return nil
}

func parseRSSWatchInterval(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultRSSWatchInterval, nil
	}
	value, err := time.ParseDuration(strings.ToLower(strings.TrimSpace(raw)))
	if err != nil {
		return 0, fmt.Errorf("周期格式不正确，请使用 5m、15m、2h 这类格式")
	}
	if value < minimumRSSWatchInterval {
		return 0, fmt.Errorf("RSS 检查周期不能短于 %s", minimumRSSWatchInterval)
	}
	if value > maximumScheduleInterval {
		return 0, fmt.Errorf("RSS 检查周期不能超过 %s", maximumScheduleInterval)
	}
	return value, nil
}

func (r *Runtime) CreateRSSWatch(ctx context.Context, input RSSWatchCreateInput) (Reminder, error) {
	event := MessageEvent{Platform: strings.TrimSpace(input.Platform), ProfileID: strings.TrimSpace(input.ProfileID), ContextNamespace: strings.TrimSpace(input.ContextNamespace), GroupID: strings.TrimSpace(input.GroupID), UserID: strings.TrimSpace(input.UserID)}
	if event.GroupID != "" {
		event.Kind = EventKindGroup
	} else {
		event.Kind = EventKindPrivate
		if event.UserID == "" {
			return Reminder{}, fmt.Errorf("私聊通知必须填写发送对象 ID")
		}
	}
	pluginValue, settings, enabled := r.plugins.PluginWithSettingsForGroup(
		rssWatchPluginID,
		r.pluginOverridesForEvent(event),
		r.pluginSettingOverridesForEvent(event),
	)
	plugin, ok := pluginValue.(*RSSWatchPlugin)
	if !enabled || !ok {
		return Reminder{}, fmt.Errorf("RSS 与社交订阅插件未启用")
	}
	interval := input.Interval
	if interval == 0 {
		interval = defaultRSSWatchInterval
	}
	if interval < minimumRSSWatchInterval || interval > maximumScheduleInterval {
		return Reminder{}, fmt.Errorf("RSS 检查周期必须在 %s 到 %s 之间", minimumRSSWatchInterval, maximumScheduleInterval)
	}
	judge := strings.TrimSpace(input.JudgePrompt)
	if judge == "" {
		return Reminder{}, fmt.Errorf("judge_prompt 不能为空，请说明什么情况下回复以及回复内容要求")
	}
	if len([]rune(judge)) > maximumRSSJudgeRunes {
		return Reminder{}, fmt.Errorf("judge_prompt 不能超过 %d 个字符", maximumRSSJudgeRunes)
	}
	feedURL, source, handle, err := resolveRSSWatchSource(input.FeedURL, input.TwitterHandle, settings)
	if err != nil {
		return Reminder{}, err
	}
	baseline, feedName, err := plugin.snapshot(ctx, feedURL, settings)
	if err != nil {
		return Reminder{}, fmt.Errorf("建立 Feed 基线失败: %w", err)
	}
	return r.addRSSWatch(event, firstNonEmpty(strings.TrimSpace(input.OwnerID), event.UserID), feedURL, source, handle, judge, feedName, interval, baseline)
}

func resolveRSSWatchSource(rawURL, rawHandle string, settings SettingValues) (string, string, string, error) {
	rawURL, rawHandle = strings.TrimSpace(rawURL), strings.TrimSpace(rawHandle)
	if (rawURL == "") == (rawHandle == "") {
		return "", "", "", fmt.Errorf("feed_url 和 twitter_handle 必须且只能填写一个")
	}
	if rawHandle != "" {
		handle, err := normalizeTwitterHandle(rawHandle)
		if err != nil {
			return "", "", "", err
		}
		feedURL, err := twitterFeedURL(handle, settings)
		return feedURL, "twitter", handle, err
	}
	feedURL, err := normalizeRSSURL(rawURL)
	return feedURL, "rss", "", err
}

func (r *Runtime) addRSSWatch(event MessageEvent, owner, feedURL, source, handle, judge, feedName string, interval time.Duration, baseline rssWatchSnapshot) (Reminder, error) {
	if r.reminders == nil {
		return Reminder{}, fmt.Errorf("当前未启用定时任务存储")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	now := time.Now()
	message := "监控 RSS Feed"
	if source == "twitter" {
		message = "监控 @" + handle + " 的最新推文"
	} else if feedName != "" {
		message = "监控 " + feedName
	}
	item := Reminder{
		ID: uuid.NewString()[:8], Kind: ReminderKindRSSWatch, Platform: event.Platform, ProfileID: event.ProfileID,
		ContextNamespace: event.ContextNamespace, OwnerID: owner, GroupID: event.GroupID, UserID: event.UserID,
		Message: message, FeedURL: feedURL, FeedSource: source, FeedHandle: handle, FeedJudgePrompt: judge,
		LastFeedItemID: baseline.ItemID, LastFeedPublishedAt: baseline.PublishedAt,
		TriggerAt: now.Add(interval), IntervalSeconds: int64(interval / time.Second), CreatedAt: now,
	}
	if err := r.reminders.SaveReminders(append(r.reminders.Reminders(), item)); err != nil {
		return Reminder{}, fmt.Errorf("保存 RSS 订阅失败: %w", err)
	}
	return item, nil
}

func (r *Runtime) UpdateRSSWatch(ctx context.Context, owner, id string, input RSSWatchUpdateInput) (Reminder, error) {
	current, err := r.rssWatch(owner, id)
	if err != nil {
		return Reminder{}, err
	}
	event := reminderSourceEvent(current)
	pluginValue, settings, enabled := r.plugins.PluginWithSettingsForGroup(
		rssWatchPluginID,
		r.pluginOverridesForEvent(event),
		r.pluginSettingOverridesForEvent(event),
	)
	plugin, ok := pluginValue.(*RSSWatchPlugin)
	if !enabled || !ok {
		return Reminder{}, fmt.Errorf("RSS 与社交订阅插件未启用")
	}
	if !current.CancelledAt.IsZero() {
		return Reminder{}, fmt.Errorf("RSS 订阅 %s 已取消，不能修改", id)
	}
	feedURL, source, handle := current.FeedURL, current.FeedSource, current.FeedHandle
	sourceChanged := false
	if input.FeedURL != nil || input.TwitterHandle != nil {
		rawURL, rawHandle := "", ""
		if input.FeedURL != nil {
			rawURL = *input.FeedURL
		}
		if input.TwitterHandle != nil {
			rawHandle = *input.TwitterHandle
		}
		feedURL, source, handle, err = resolveRSSWatchSource(rawURL, rawHandle, settings)
		if err != nil {
			return Reminder{}, err
		}
		sourceChanged = feedURL != current.FeedURL
	}
	judge := current.FeedJudgePrompt
	if input.JudgePrompt != nil {
		judge = strings.TrimSpace(*input.JudgePrompt)
		if judge == "" || len([]rune(judge)) > maximumRSSJudgeRunes {
			return Reminder{}, fmt.Errorf("judge_prompt 必须为 1-%d 个字符", maximumRSSJudgeRunes)
		}
	}
	interval := time.Duration(current.IntervalSeconds) * time.Second
	if input.Interval > 0 {
		interval = input.Interval
	}
	if interval < minimumRSSWatchInterval || interval > maximumScheduleInterval {
		return Reminder{}, fmt.Errorf("RSS 检查周期必须在 %s 到 %s 之间", minimumRSSWatchInterval, maximumScheduleInterval)
	}
	baseline := rssWatchSnapshot{ItemID: current.LastFeedItemID, PublishedAt: current.LastFeedPublishedAt}
	feedName := ""
	if sourceChanged {
		baseline, feedName, err = plugin.snapshot(ctx, feedURL, settings)
		if err != nil {
			return Reminder{}, fmt.Errorf("更新 Feed 基线失败: %w", err)
		}
	}
	return r.mutateRSSWatch(owner, id, func(item *Reminder) error {
		item.FeedURL, item.FeedSource, item.FeedHandle, item.FeedJudgePrompt = feedURL, source, handle, judge
		item.IntervalSeconds, item.TriggerAt = int64(interval/time.Second), time.Now().Add(interval)
		item.PendingDelivery, item.PendingSince, item.LastError, item.ConsecutiveFailures = "", time.Time{}, "", 0
		if sourceChanged {
			item.LastFeedItemID, item.LastFeedPublishedAt = baseline.ItemID, baseline.PublishedAt
		}
		if source == "twitter" {
			item.Message = "监控 @" + handle + " 的最新推文"
		} else if feedName != "" {
			item.Message = "监控 " + feedName
		}
		return nil
	})
}

func (r *Runtime) rssWatches(owner string) []Reminder {
	if r.reminders == nil {
		return nil
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	var out []Reminder
	for _, item := range r.reminders.Reminders() {
		if reminderIsRSSWatch(item) && item.OwnerID == owner {
			out = append(out, item)
		}
	}
	return out
}

func (r *Runtime) rssWatch(owner, id string) (Reminder, error) {
	for _, item := range r.rssWatches(owner) {
		if item.ID == strings.TrimSpace(id) {
			return item, nil
		}
	}
	return Reminder{}, fmt.Errorf("没有找到属于目标用户的 RSS 订阅 %s", id)
}

func (r *Runtime) mutateRSSWatch(owner, id string, mutate func(*Reminder) error) (Reminder, error) {
	if r.reminders == nil {
		return Reminder{}, fmt.Errorf("当前未启用定时任务存储")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	for index := range items {
		item := &items[index]
		if !reminderIsRSSWatch(*item) || item.OwnerID != owner || item.ID != strings.TrimSpace(id) {
			continue
		}
		if err := mutate(item); err != nil {
			return Reminder{}, err
		}
		if err := r.reminders.SaveReminders(items); err != nil {
			return Reminder{}, fmt.Errorf("保存 RSS 订阅失败: %w", err)
		}
		return *item, nil
	}
	return Reminder{}, fmt.Errorf("没有找到属于目标用户的 RSS 订阅 %s", id)
}

func (r *Runtime) CancelRSSWatch(owner, id string) (Reminder, error) {
	return r.mutateRSSWatch(owner, id, func(item *Reminder) error {
		if !item.CancelledAt.IsZero() {
			return fmt.Errorf("RSS 订阅 %s 已经取消", id)
		}
		item.CancelledAt, item.PendingDelivery, item.PendingSince = time.Now(), "", time.Time{}
		return nil
	})
}

func (r *Runtime) DeleteRSSWatch(owner, id string) (bool, error) {
	if r.reminders == nil {
		return false, fmt.Errorf("当前未启用定时任务存储")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	next := make([]Reminder, 0, len(items))
	removed := false
	for _, item := range items {
		if reminderIsRSSWatch(item) && item.OwnerID == owner && item.ID == strings.TrimSpace(id) {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if removed {
		if err := r.reminders.SaveReminders(next); err != nil {
			return false, fmt.Errorf("删除 RSS 订阅失败: %w", err)
		}
	}
	return removed, nil
}

func reminderIsRSSWatch(item Reminder) bool {
	return item.Kind == ReminderKindRSSWatch && item.IntervalSeconds > 0 && strings.TrimSpace(item.FeedURL) != ""
}

func rssWatchUpdateFromTool(input map[string]any) (RSSWatchUpdateInput, error) {
	result := RSSWatchUpdateInput{}
	if value, present := input["feed_url"]; present {
		raw := strings.TrimSpace(fmt.Sprint(value))
		result.FeedURL = &raw
	}
	if value, present := input["twitter_handle"]; present {
		raw := strings.TrimSpace(fmt.Sprint(value))
		result.TwitterHandle = &raw
	}
	if value, present := input["judge_prompt"]; present {
		raw := strings.TrimSpace(fmt.Sprint(value))
		result.JudgePrompt = &raw
	}
	if raw := strings.TrimSpace(configToolString(input, "interval")); raw != "" {
		value, err := parseRSSWatchInterval(raw)
		if err != nil {
			return result, err
		}
		result.Interval = value
	}
	return result, nil
}

func rssWatchForTool(item Reminder) *dianaRSSWatch {
	return &dianaRSSWatch{ID: item.ID, FeedURL: item.FeedURL, Source: item.FeedSource, TwitterHandle: item.FeedHandle, JudgePrompt: item.FeedJudgePrompt, Interval: (time.Duration(item.IntervalSeconds) * time.Second).String(), NextRunAt: item.TriggerAt, LastRunAt: item.LastRunAt, Status: scheduleStatus(item), LastError: item.LastError, PendingDelivery: strings.TrimSpace(item.PendingDelivery) != ""}
}

func marshalDianaRSSWatchResult(result dianaRSSWatchResult) (string, error) {
	body, err := json.MarshalIndent(result, "", "  ")
	return string(body), err
}
