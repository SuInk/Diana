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
	// 一条订阅最多盯多少个来源。每轮检查会挨个抓 Feed，来源越多一轮越久，
	// 十个足够「同一套规则盯一批人」，再多就该拆成两条订阅。
	maximumRSSWatchSources = 10
)

type RSSWatchCreateInput struct {
	FeedURL, TwitterHandle, JudgePrompt string
	// FeedURLs / TwitterHandles 是多来源写法，和单数字段合并后共用同一套规则。
	FeedURLs, TwitterHandles                                        []string
	Interval                                                        time.Duration
	Platform, ProfileID, ContextNamespace, OwnerID, GroupID, UserID string
}

type RSSWatchUpdateInput struct {
	FeedURL, TwitterHandle, JudgePrompt *string
	FeedURLs, TwitterHandles            *[]string
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
	ID string `json:"id"`
	// Sources 列出这条订阅盯的所有来源；feed_url / twitter_handle 保留第一个，
	// 方便只看单来源的旧读法。
	Sources         []string  `json:"sources,omitempty"`
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
	return `创建和管理 RSS/Atom 或 X (Twitter) 用户订阅：发现新条目后由模型按 judge_prompt 判断是否值得通知，不符合条件就保持静默。一条订阅可以同时盯多个账号或多个 Feed，它们共用同一套 judge_prompt，命中的内容合成一条消息发出；用户说「盯这几个人，条件一样」时建一条多来源订阅，不要一人建一条。用户要求持续关注某个网站 Feed 或某个推特用户、并且只在特定内容出现时才通知，必须使用本工具；普通周期搜索改用 diana.schedule。首次创建只建立当前内容基线，不补发历史条目。`
}

func (*dianaRSSWatchTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作。cancel 只停止并保留记录，delete 才彻底删除。",
			"create", "list", "update", "cancel", "delete"),
		"twitter_handle":  toolStringParam("要关注的单个 X (Twitter) 用户名，不带 @。盯多个人用 twitter_handles。"),
		"twitter_handles": toolStringArrayParam("要关注的多个 X (Twitter) 用户名，共用同一套 judge_prompt。最多 " + itoa(maximumRSSWatchSources) + " 个来源（和 feed_urls 合计）。"),
		"feed_url":        toolStringParam("要关注的单个 RSS/Atom feed 地址。盯多个 Feed 用 feed_urls。"),
		"feed_urls":       toolStringArrayParam("要关注的多个 RSS/Atom feed 地址，共用同一套 judge_prompt。"),
		"interval":        toolStringParam("检查间隔，只接受 Go 时长写法：15m、1h。不短于 " + minimumRSSWatchInterval.String() + "，省略按 " + defaultRSSWatchInterval.String() + " 处理。"),
		"judge_prompt":    toolStringParam("判断条件：写清楚什么样的新条目才值得通知、通知时要说什么。最多 " + itoa(maximumRSSJudgeRunes) + " 个字符。例如「仅当推文明确提到额度重置、恢复或刷新时通知，并用中文说明时间和原文链接」。"),
		"id":              toolStringParam("要操作的订阅 ID；update、cancel、delete 必填，可先用 list 查到。"),
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
			FeedURLs: configToolStringSlice(input, "feed_urls"), TwitterHandles: configToolStringSlice(input, "twitter_handles"),
			JudgePrompt: configToolString(input, "judge_prompt"), Interval: interval,
			Platform: t.event.Platform, ProfileID: t.event.ProfileID, ContextNamespace: t.event.ContextNamespace,
			OwnerID: targetID, GroupID: t.event.GroupID, UserID: targetID,
		})
		if err != nil {
			return "", err
		}
		return marshalDianaRSSWatchResult(dianaRSSWatchResult{OK: true, Action: "created", Message: fmt.Sprintf("订阅已创建，共 %d 个来源，当前 Feed 已作为基线；后续新条目会先经判断器决定是否回复。", len(ReminderFeedSources(item))), Watch: rssWatchForTool(item)})
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
	// 单数字段是老写法，和复数字段合并起来一起解析；用新切片装，免得 append
	// 写进调用方传来的数组里。
	rawURLs := append(append([]string{}, input.FeedURLs...), input.FeedURL)
	rawHandles := append(append([]string{}, input.TwitterHandles...), input.TwitterHandle)
	sources, err := resolveRSSWatchSources(rawURLs, rawHandles)
	if err != nil {
		return Reminder{}, err
	}
	for index := range sources {
		baseline, feedName, err := plugin.snapshot(ctx, sources[index].FeedURL, settings)
		if err != nil {
			return Reminder{}, fmt.Errorf("建立 %s 的 Feed 基线失败: %w", rssWatchSourceLabel(sources[index]), err)
		}
		sources[index].Name = feedName
		sources[index].LastItemID, sources[index].LastPublishedAt = baseline.ItemID, baseline.PublishedAt
	}
	return r.addRSSWatch(event, firstNonEmpty(strings.TrimSpace(input.OwnerID), event.UserID), judge, interval, sources)
}

// resolveRSSWatchSources 把用户填的一堆用户名和 Feed 地址整理成来源列表。
// 同一条订阅里重复填同一个来源没有意义，只会让一轮抓两遍、通知里出现两段一样
// 的内容，所以按最终 URL 去重。
func resolveRSSWatchSources(rawURLs, rawHandles []string) ([]ReminderFeedSource, error) {
	sources := make([]ReminderFeedSource, 0, len(rawURLs)+len(rawHandles))
	seen := make(map[string]struct{}, len(rawURLs)+len(rawHandles))
	appendSource := func(source ReminderFeedSource) {
		if _, duplicate := seen[source.FeedURL]; duplicate {
			return
		}
		seen[source.FeedURL] = struct{}{}
		sources = append(sources, source)
	}
	for _, raw := range rawHandles {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		handle, err := normalizeTwitterHandle(raw)
		if err != nil {
			return nil, err
		}
		feedURL, err := twitterFeedURL(handle)
		if err != nil {
			return nil, err
		}
		appendSource(ReminderFeedSource{FeedURL: feedURL, Source: "twitter", Handle: handle})
	}
	for _, raw := range rawURLs {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		feedURL, err := normalizeRSSURL(raw)
		if err != nil {
			return nil, err
		}
		appendSource(ReminderFeedSource{FeedURL: feedURL, Source: "rss"})
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("至少要填写一个 twitter_handle(s) 或 feed_url(s)")
	}
	if len(sources) > maximumRSSWatchSources {
		return nil, fmt.Errorf("一条订阅最多盯 %d 个来源，当前填了 %d 个，请拆成多条", maximumRSSWatchSources, len(sources))
	}
	return sources, nil
}

// rssWatchSourceLabel 是来源在提示、抬头和错误里的短名字：推特用 @handle，
// 普通 Feed 优先用标题，最后才退回地址。
func rssWatchSourceLabel(source ReminderFeedSource) string {
	if handle := strings.TrimSpace(source.Handle); source.Source == "twitter" && handle != "" {
		return "@" + handle
	}
	if name := strings.TrimSpace(source.Name); name != "" {
		return name
	}
	return strings.TrimSpace(source.FeedURL)
}

// rssWatchMessage 是订阅在任务列表里显示的一句话。多来源只列前三个，剩下的
// 折成数量，避免几十个字的标题把列表挤爆。
func rssWatchMessage(sources []ReminderFeedSource) string {
	if len(sources) == 0 {
		return "监控 RSS Feed"
	}
	if len(sources) == 1 {
		if sources[0].Source == "twitter" && strings.TrimSpace(sources[0].Handle) != "" {
			return "监控 @" + sources[0].Handle + " 的最新推文"
		}
		if label := rssWatchSourceLabel(sources[0]); label != "" {
			return "监控 " + label
		}
		return "监控 RSS Feed"
	}
	labels := make([]string, 0, len(sources))
	for _, source := range sources {
		labels = append(labels, rssWatchSourceLabel(source))
	}
	if len(labels) > 3 {
		return fmt.Sprintf("监控 %s 等 %d 个来源", strings.Join(labels[:3], "、"), len(labels))
	}
	return "监控 " + strings.Join(labels, "、")
}

func (r *Runtime) addRSSWatch(event MessageEvent, owner, judge string, interval time.Duration, sources []ReminderFeedSource) (Reminder, error) {
	if r.reminders == nil {
		return Reminder{}, fmt.Errorf("当前未启用定时任务存储")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	now := time.Now()
	item := Reminder{
		ID: uuid.NewString()[:8], Kind: ReminderKindRSSWatch, Platform: event.Platform, ProfileID: event.ProfileID,
		ContextNamespace: event.ContextNamespace, OwnerID: owner, GroupID: event.GroupID, UserID: event.UserID,
		Message: rssWatchMessage(sources), FeedJudgePrompt: judge,
		TriggerAt: now.Add(interval), IntervalSeconds: int64(interval / time.Second), CreatedAt: now,
	}
	applyRSSWatchSources(&item, sources)
	if err := r.reminders.SaveReminders(append(r.reminders.Reminders(), item)); err != nil {
		return Reminder{}, fmt.Errorf("保存 RSS 订阅失败: %w", err)
	}
	return item, nil
}

// applyRSSWatchSources 写回来源列表，并把第一个来源镜像到单来源字段上。
// 镜像是为了让只认 FeedURL/FeedHandle 的老读取方（任务列表、失败告警、历史
// 记录）在多来源订阅上也能显示出东西，而不是一片空白。
func applyRSSWatchSources(item *Reminder, sources []ReminderFeedSource) {
	item.FeedSourcesJSON = encodeReminderFeedSources(sources)
	if len(sources) == 0 {
		item.FeedURL, item.FeedSource, item.FeedHandle = "", "", ""
		item.LastFeedItemID, item.LastFeedPublishedAt = "", time.Time{}
		return
	}
	first := sources[0]
	item.FeedURL, item.FeedSource, item.FeedHandle = first.FeedURL, first.Source, first.Handle
	item.LastFeedItemID, item.LastFeedPublishedAt = first.LastItemID, first.LastPublishedAt
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
	sources := ReminderFeedSources(current)
	sourcesChanged := false
	if input.FeedURL != nil || input.TwitterHandle != nil || input.FeedURLs != nil || input.TwitterHandles != nil {
		rawURLs, rawHandles := []string{}, []string{}
		if input.FeedURLs != nil {
			rawURLs = append(rawURLs, *input.FeedURLs...)
		}
		if input.FeedURL != nil {
			rawURLs = append(rawURLs, *input.FeedURL)
		}
		if input.TwitterHandles != nil {
			rawHandles = append(rawHandles, *input.TwitterHandles...)
		}
		if input.TwitterHandle != nil {
			rawHandles = append(rawHandles, *input.TwitterHandle)
		}
		next, err := resolveRSSWatchSources(rawURLs, rawHandles)
		if err != nil {
			return Reminder{}, err
		}
		sources, sourcesChanged = carryRSSWatchCursors(next, ReminderFeedSources(current))
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
	// 只给新加进来的来源建基线：留下来的来源保持自己的游标，否则改一次订阅
	// 就把所有人的进度抹平，改完那一刻的存量内容会被当成「已读」。
	for index := range sources {
		if sources[index].LastItemID != "" || !sources[index].LastPublishedAt.IsZero() {
			continue
		}
		baseline, feedName, err := plugin.snapshot(ctx, sources[index].FeedURL, settings)
		if err != nil {
			return Reminder{}, fmt.Errorf("更新 %s 的 Feed 基线失败: %w", rssWatchSourceLabel(sources[index]), err)
		}
		sources[index].Name = feedName
		sources[index].LastItemID, sources[index].LastPublishedAt = baseline.ItemID, baseline.PublishedAt
	}
	return r.mutateRSSWatch(owner, id, func(item *Reminder) error {
		item.FeedJudgePrompt = judge
		item.IntervalSeconds, item.TriggerAt = int64(interval/time.Second), time.Now().Add(interval)
		item.PendingDelivery, item.PendingSince, item.LastError, item.ConsecutiveFailures = "", time.Time{}, "", 0
		applyRSSWatchSources(item, sources)
		if sourcesChanged || strings.TrimSpace(item.Message) == "" {
			item.Message = rssWatchMessage(sources)
		}
		return nil
	})
}

// carryRSSWatchCursors 把新旧来源按 URL 对上，沿用老来源的游标和标题，
// 顺带告诉调用方来源集合到底变没变。
func carryRSSWatchCursors(next, current []ReminderFeedSource) ([]ReminderFeedSource, bool) {
	previous := make(map[string]ReminderFeedSource, len(current))
	for _, source := range current {
		previous[source.FeedURL] = source
	}
	changed := len(next) != len(current)
	for index := range next {
		old, kept := previous[next[index].FeedURL]
		if !kept {
			changed = true
			continue
		}
		next[index].Name = old.Name
		next[index].LastItemID, next[index].LastPublishedAt = old.LastItemID, old.LastPublishedAt
	}
	return next, changed
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
	if _, present := input["feed_urls"]; present {
		values := configToolStringSlice(input, "feed_urls")
		result.FeedURLs = &values
	}
	if _, present := input["twitter_handles"]; present {
		values := configToolStringSlice(input, "twitter_handles")
		result.TwitterHandles = &values
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

// rssWatchSourceLabels 把订阅的来源列成短名字，给工具结果和任务列表用。
func rssWatchSourceLabels(item Reminder) []string {
	sources := ReminderFeedSources(item)
	if len(sources) == 0 {
		return nil
	}
	labels := make([]string, 0, len(sources))
	for _, source := range sources {
		labels = append(labels, rssWatchSourceLabel(source))
	}
	return labels
}

func rssWatchForTool(item Reminder) *dianaRSSWatch {
	return &dianaRSSWatch{ID: item.ID, Sources: rssWatchSourceLabels(item), FeedURL: item.FeedURL, Source: item.FeedSource, TwitterHandle: item.FeedHandle, JudgePrompt: item.FeedJudgePrompt, Interval: (time.Duration(item.IntervalSeconds) * time.Second).String(), NextRunAt: item.TriggerAt, LastRunAt: item.LastRunAt, Status: scheduleStatus(item), LastError: item.LastError, PendingDelivery: strings.TrimSpace(item.PendingDelivery) != ""}
}

func marshalDianaRSSWatchResult(result dianaRSSWatchResult) (string, error) {
	body, err := json.MarshalIndent(result, "", "  ")
	return string(body), err
}
