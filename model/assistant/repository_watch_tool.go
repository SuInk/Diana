// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	minimumRepositoryWatchInterval              = 30 * time.Second
	authenticatedRepositoryWatchDefaultInterval = time.Minute
	anonymousRepositoryWatchDefaultInterval     = time.Hour
)

func normalizeReminderDeliveryTargets(targets []ReminderDeliveryTarget) []ReminderDeliveryTarget {
	out := make([]ReminderDeliveryTarget, 0, len(targets))
	seen := map[string]struct{}{}
	for _, target := range targets {
		target.Platform = strings.TrimSpace(target.Platform)
		target.ProfileID = strings.TrimSpace(target.ProfileID)
		target.ContextNamespace = strings.TrimSpace(target.ContextNamespace)
		target.GroupID = strings.TrimSpace(target.GroupID)
		target.UserID = strings.TrimSpace(target.UserID)
		if target.GroupID == "" && target.UserID == "" {
			continue
		}
		if target.GroupID != "" {
			target.UserID = ""
		}
		key := target.Platform + "|" + target.ProfileID + "|" + target.GroupID + "|" + target.UserID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, target)
	}
	return out
}

func defaultRepositoryWatchInterval(settings SettingValues) time.Duration {
	if strings.TrimSpace(settings.String(repositoryWatchSettingToken, "")) != "" {
		return authenticatedRepositoryWatchDefaultInterval
	}
	return anonymousRepositoryWatchDefaultInterval
}

type RepositoryWatchCreateInput struct {
	Repository          string
	Branch              string
	Interval            time.Duration
	WatchCommits        bool
	WatchPullRequests   bool
	WatchReleases       bool
	WatchStars          bool
	Platform            string
	ProfileID           string
	ContextNamespace    string
	OwnerID             string
	GroupID             string
	UserID              string
	NotificationEnabled bool
	NotificationTargets []ReminderDeliveryTarget
}

type RepositoryWatchUpdateInput struct {
	Repository          string
	Branch              *string
	Interval            time.Duration
	WatchCommits        *bool
	WatchPullRequests   *bool
	WatchReleases       *bool
	WatchStars          *bool
	Delivery            bool
	Platform            string
	ProfileID           string
	ContextNamespace    string
	OwnerID             string
	GroupID             string
	UserID              string
	NotificationEnabled *bool
	NotificationTargets []ReminderDeliveryTarget
}

func (r *Runtime) CreateRepositoryWatch(ctx context.Context, input RepositoryWatchCreateInput) (Reminder, error) {
	if len(input.NotificationTargets) > 0 {
		input.NotificationTargets = normalizeReminderDeliveryTargets(input.NotificationTargets)
		if len(input.NotificationTargets) == 0 {
			return Reminder{}, fmt.Errorf("通知目标不能为空")
		}
		first := input.NotificationTargets[0]
		input.Platform, input.ProfileID, input.ContextNamespace = first.Platform, first.ProfileID, first.ContextNamespace
		input.GroupID, input.UserID = first.GroupID, first.UserID
	} else if !input.NotificationEnabled {
		// Old API callers did not send NotificationEnabled; an existing legacy
		// destination means notifications are enabled for compatibility.
		if strings.TrimSpace(input.GroupID) != "" || strings.TrimSpace(input.UserID) != "" {
			input.NotificationEnabled = true
		} else {
			input.GroupID, input.UserID = "", ""
		}
	}
	event := MessageEvent{
		Platform: strings.TrimSpace(input.Platform), ProfileID: strings.TrimSpace(input.ProfileID),
		ContextNamespace: strings.TrimSpace(input.ContextNamespace), UserID: strings.TrimSpace(input.UserID),
		GroupID: strings.TrimSpace(input.GroupID),
	}
	if event.GroupID != "" {
		event.Kind = EventKindGroup
	} else if event.UserID != "" {
		event.Kind = EventKindPrivate
	} else if input.NotificationEnabled {
		return Reminder{}, fmt.Errorf("启用通知时至少填写一个群聊或私聊对象")
	} else {
		event.Kind = EventKindPrivate
	}
	pluginValue, settings, enabled := r.plugins.PluginWithSettingsForGroup(
		repositoryWatchPluginID,
		r.pluginOverridesForEvent(event),
		r.pluginSettingOverridesForEvent(event),
	)
	plugin, ok := pluginValue.(*RepositoryWatchPlugin)
	if !enabled || !ok {
		return Reminder{}, fmt.Errorf("仓库更新订阅插件未启用")
	}
	repository, err := normalizeGitHubRepository(input.Repository)
	if err != nil {
		return Reminder{}, err
	}
	interval := input.Interval
	if interval == 0 {
		interval = defaultRepositoryWatchInterval(settings)
	}
	if interval < minimumRepositoryWatchInterval {
		return Reminder{}, fmt.Errorf("仓库检查周期不能短于 %s", minimumRepositoryWatchInterval)
	}
	if interval > maximumScheduleInterval {
		return Reminder{}, fmt.Errorf("仓库检查周期不能超过 %s", maximumScheduleInterval)
	}
	selection := repositoryWatchSelection{Commits: input.WatchCommits, PullRequests: input.WatchPullRequests, Releases: input.WatchReleases, Stars: input.WatchStars}
	if !selection.Commits && !selection.PullRequests && !selection.Releases && !selection.Stars {
		return Reminder{}, fmt.Errorf("Commit、PR、Release 和 Star 至少启用一项")
	}
	baseline, err := plugin.snapshotSelected(ctx, repository, strings.TrimSpace(input.Branch), selection, settings)
	if err != nil {
		return Reminder{}, fmt.Errorf("建立仓库基线失败: %w", err)
	}
	ownerID := firstNonEmpty(strings.TrimSpace(input.OwnerID), repositoryWatchWebUIOwner(event.ProfileID))
	return r.addRepositoryWatch(event, ownerID, repository, strings.TrimSpace(input.Branch), interval, selection, baseline, input.NotificationEnabled, input.NotificationTargets)
}

func repositoryWatchWebUIOwner(profileID string) string {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return "webui"
	}
	return "webui:" + profileID
}

func (r *Runtime) UpdateRepositoryWatch(ctx context.Context, ownerID, id string, input RepositoryWatchUpdateInput) (Reminder, error) {
	current, err := r.repositoryWatch(strings.TrimSpace(ownerID), strings.TrimSpace(id))
	if err != nil {
		return Reminder{}, err
	}
	event := reminderSourceEvent(current)
	if input.Delivery {
		event.Platform = strings.TrimSpace(input.Platform)
		event.ProfileID = strings.TrimSpace(input.ProfileID)
		event.ContextNamespace = strings.TrimSpace(input.ContextNamespace)
		event.GroupID = strings.TrimSpace(input.GroupID)
		event.UserID = strings.TrimSpace(input.UserID)
		if event.GroupID != "" {
			event.Kind = EventKindGroup
			event.UserID = ""
		} else if event.UserID != "" {
			event.Kind = EventKindPrivate
		} else if input.NotificationEnabled != nil && !*input.NotificationEnabled {
			event.Kind = EventKindPrivate
		} else {
			return Reminder{}, fmt.Errorf("启用通知时至少填写一个群聊或私聊对象")
		}
	}
	if input.NotificationEnabled != nil {
		// The legacy Delivery flag still updates the primary target; the new
		// target list is applied below by the web handler when present.
	}
	pluginValue, settings, enabled := r.plugins.PluginWithSettingsForGroup(
		repositoryWatchPluginID,
		r.pluginOverridesForEvent(event),
		r.pluginSettingOverridesForEvent(event),
	)
	plugin, ok := pluginValue.(*RepositoryWatchPlugin)
	if !enabled || !ok {
		return Reminder{}, fmt.Errorf("仓库更新订阅插件未启用")
	}
	values := map[string]any{}
	if strings.TrimSpace(input.Repository) != "" {
		values["repository"] = input.Repository
	}
	if input.Branch != nil {
		values["branch"] = *input.Branch
	}
	if input.Interval > 0 {
		if input.Interval < minimumRepositoryWatchInterval {
			return Reminder{}, fmt.Errorf("仓库检查周期不能短于 %s", minimumRepositoryWatchInterval)
		}
		if input.Interval > maximumScheduleInterval {
			return Reminder{}, fmt.Errorf("仓库检查周期不能超过 %s", maximumScheduleInterval)
		}
		values["interval"] = input.Interval.String()
	}
	if input.WatchCommits != nil {
		values["watch_commits"] = *input.WatchCommits
	}
	if input.WatchPullRequests != nil {
		values["watch_pull_requests"] = *input.WatchPullRequests
	}
	if input.WatchReleases != nil {
		values["watch_releases"] = *input.WatchReleases
	}
	if input.WatchStars != nil {
		values["watch_stars"] = *input.WatchStars
	}
	if input.Delivery {
		values["delivery"] = true
		values["platform"] = event.Platform
		values["profile_id"] = event.ProfileID
		values["context_namespace"] = event.ContextNamespace
		values["owner_id"] = strings.TrimSpace(input.OwnerID)
		values["group_id"] = event.GroupID
		values["user_id"] = event.UserID
	}
	if input.NotificationEnabled != nil {
		values["notification_enabled"] = *input.NotificationEnabled
	}
	if input.NotificationTargets != nil {
		values["notification_targets"] = normalizeReminderDeliveryTargets(input.NotificationTargets)
	}
	return r.updateRepositoryWatch(strings.TrimSpace(ownerID), strings.TrimSpace(id), values, plugin, settings, ctx)
}

func (r *Runtime) CancelRepositoryWatch(ownerID, id string) (Reminder, error) {
	return r.cancelRepositoryWatch(strings.TrimSpace(ownerID), strings.TrimSpace(id))
}

func (r *Runtime) DeleteRepositoryWatch(ownerID, id string) (bool, error) {
	return r.deleteRepositoryWatch(strings.TrimSpace(ownerID), strings.TrimSpace(id))
}

func parseRepositoryWatchInterval(raw string, settings SettingValues) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultRepositoryWatchInterval(settings), nil
	}
	interval, err := time.ParseDuration(strings.TrimSpace(strings.ToLower(raw)))
	if err != nil {
		return 0, fmt.Errorf("周期格式不正确，请使用 30s、1m、2h 这类格式")
	}
	if interval < minimumRepositoryWatchInterval {
		return 0, fmt.Errorf("仓库检查周期不能短于 %s", minimumRepositoryWatchInterval)
	}
	if interval > maximumScheduleInterval {
		return 0, fmt.Errorf("仓库检查周期不能超过 %s", maximumScheduleInterval)
	}
	return interval, nil
}

func (r *Runtime) addRepositoryWatch(event MessageEvent, ownerID, repository, branch string, interval time.Duration, selection repositoryWatchSelection, baseline repositoryWatchSnapshot, notificationEnabled bool, targets []ReminderDeliveryTarget) (Reminder, error) {
	if r.reminders == nil {
		return Reminder{}, fmt.Errorf("当前未启用定时任务存储")
	}
	if !selection.Commits && !selection.PullRequests && !selection.Releases && !selection.Stars {
		return Reminder{}, fmt.Errorf("仓库动态监控类型不能全部关闭")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	now := time.Now()
	item := Reminder{
		ID:                      uuid.NewString()[:8],
		Kind:                    ReminderKindRepositoryWatch,
		Platform:                event.Platform,
		ProfileID:               event.ProfileID,
		ContextNamespace:        event.ContextNamespace,
		OwnerID:                 strings.TrimSpace(ownerID),
		GroupID:                 event.GroupID,
		UserID:                  event.UserID,
		NotificationEnabled:     notificationEnabled,
		NotificationTargetsJSON: encodeReminderDeliveryTargets(normalizeReminderDeliveryTargets(targets)),
		Message:                 "监控 " + repository + " 的仓库动态",
		Repository:              repository,
		RepositoryBranch:        branch,
		WatchCommits:            selection.Commits,
		WatchPullRequests:       selection.PullRequests,
		WatchReleases:           selection.Releases,
		WatchStars:              selection.Stars,
		LastCommitSHA:           baseline.CommitSHA,
		LastPullRequestCursor:   baseline.PullRequestCursor,
		LastReleaseTag:          baseline.ReleaseTag,
		LastStarCount:           baseline.StarCount,
		TriggerAt:               now.Add(interval),
		IntervalSeconds:         int64(interval / time.Second),
		CreatedAt:               now,
	}
	if err := r.reminders.SaveReminders(append(items, item)); err != nil {
		return Reminder{}, fmt.Errorf("保存仓库更新订阅失败: %w", err)
	}
	return item, nil
}

func (r *Runtime) cancelRepositoryWatch(ownerID, id string) (Reminder, error) {
	return r.mutateRepositoryWatch(ownerID, id, func(item *Reminder) error {
		if !item.CancelledAt.IsZero() {
			return fmt.Errorf("仓库更新订阅 %s 已经取消", id)
		}
		item.CancelledAt = time.Now()
		item.PendingDelivery = ""
		item.PendingSince = time.Time{}
		clearRepositoryWatchFailureState(item)
		return nil
	})
}

func (r *Runtime) updateRepositoryWatch(ownerID, id string, input map[string]any, plugin *RepositoryWatchPlugin, settings SettingValues, ctx context.Context) (Reminder, error) {
	if strings.TrimSpace(id) == "" {
		return Reminder{}, fmt.Errorf("修改仓库更新订阅时必须提供 id")
	}
	var replacementRepository string
	if raw := strings.TrimSpace(configToolString(input, "repository")); raw != "" {
		var err error
		replacementRepository, err = normalizeGitHubRepository(raw)
		if err != nil {
			return Reminder{}, err
		}
	}
	rawInterval := strings.TrimSpace(configToolString(input, "interval"))
	var interval time.Duration
	var err error
	if rawInterval != "" {
		interval, err = parseRepositoryWatchInterval(rawInterval, settings)
		if err != nil {
			return Reminder{}, err
		}
	}
	current, err := r.repositoryWatch(ownerID, id)
	if err != nil {
		return Reminder{}, err
	}
	if !current.CancelledAt.IsZero() {
		return Reminder{}, fmt.Errorf("仓库更新订阅 %s 已取消，不能修改", id)
	}
	branch := current.RepositoryBranch
	if _, present := input["branch"]; present {
		branch = strings.TrimSpace(configToolString(input, "branch"))
	}
	repository := current.Repository
	if replacementRepository != "" {
		repository = replacementRepository
	}
	selection := repositoryWatchSelection{
		Commits: current.WatchCommits, PullRequests: current.WatchPullRequests,
		Releases: current.WatchReleases, Stars: current.WatchStars,
	}
	if value, present := input["watch_commits"].(bool); present {
		selection.Commits = value
	}
	if value, present := input["watch_pull_requests"].(bool); present {
		selection.PullRequests = value
	}
	if value, present := input["watch_releases"].(bool); present {
		selection.Releases = value
	}
	if value, present := input["watch_stars"].(bool); present {
		selection.Stars = value
	}
	if !selection.Commits && !selection.PullRequests && !selection.Releases && !selection.Stars {
		return Reminder{}, fmt.Errorf("仓库动态监控类型不能全部关闭")
	}
	repositoryChanged := repository != current.Repository || branch != current.RepositoryBranch
	baselineSelection := repositoryWatchSelection{
		Commits:      repositoryChanged && selection.Commits || selection.Commits && !current.WatchCommits,
		PullRequests: repositoryChanged && selection.PullRequests || selection.PullRequests && !current.WatchPullRequests,
		Releases:     repositoryChanged && selection.Releases || selection.Releases && !current.WatchReleases,
		Stars:        repositoryChanged && selection.Stars || selection.Stars && !current.WatchStars,
	}
	var baseline repositoryWatchSnapshot
	if baselineSelection.Commits || baselineSelection.PullRequests || baselineSelection.Releases || baselineSelection.Stars {
		baseline, err = plugin.snapshotSelected(ctx, repository, branch, baselineSelection, settings)
		if err != nil {
			return Reminder{}, fmt.Errorf("更新仓库基线失败: %w", err)
		}
	}
	return r.mutateRepositoryWatch(ownerID, id, func(item *Reminder) error {
		if !item.CancelledAt.IsZero() {
			return fmt.Errorf("仓库更新订阅 %s 已取消，不能修改", id)
		}
		if baselineSelection.Commits {
			item.LastCommitSHA = baseline.CommitSHA
		}
		if baselineSelection.PullRequests {
			item.LastPullRequestCursor = baseline.PullRequestCursor
		}
		if baselineSelection.Releases {
			item.LastReleaseTag = baseline.ReleaseTag
		}
		if baselineSelection.Stars {
			item.LastStarCount = baseline.StarCount
		}
		item.Repository = repository
		item.RepositoryBranch = branch
		item.WatchCommits = selection.Commits
		item.WatchPullRequests = selection.PullRequests
		item.WatchReleases = selection.Releases
		item.WatchStars = selection.Stars
		if delivery, _ := input["delivery"].(bool); delivery {
			item.Platform = strings.TrimSpace(configToolString(input, "platform"))
			item.ProfileID = strings.TrimSpace(configToolString(input, "profile_id"))
			item.ContextNamespace = strings.TrimSpace(configToolString(input, "context_namespace"))
			item.OwnerID = strings.TrimSpace(configToolString(input, "owner_id"))
			item.GroupID = strings.TrimSpace(configToolString(input, "group_id"))
			item.UserID = strings.TrimSpace(configToolString(input, "user_id"))
		}
		if enabled, ok := input["notification_enabled"].(bool); ok {
			item.NotificationEnabled = enabled
		}
		if targets, ok := input["notification_targets"].([]ReminderDeliveryTarget); ok {
			normalizedTargets := normalizeReminderDeliveryTargets(targets)
			item.NotificationTargetsJSON = encodeReminderDeliveryTargets(normalizedTargets)
			if len(normalizedTargets) > 0 {
				first := normalizedTargets[0]
				item.Platform, item.ProfileID, item.ContextNamespace = first.Platform, first.ProfileID, first.ContextNamespace
				item.GroupID, item.UserID = first.GroupID, first.UserID
			} else if !item.NotificationEnabled {
				item.GroupID, item.UserID = "", ""
			}
		}
		item.Message = "监控 " + repository + " 的仓库动态"
		if rawInterval != "" {
			item.IntervalSeconds = int64(interval / time.Second)
		}
		item.TriggerAt = time.Now().Add(time.Duration(item.IntervalSeconds) * time.Second)
		item.PendingDelivery = ""
		item.PendingSince = time.Time{}
		item.LastError = ""
		item.ConsecutiveFailures = 0
		clearRepositoryWatchFailureState(item)
		return nil
	})
}

func (r *Runtime) repositoryWatch(ownerID, id string) (Reminder, error) {
	if r.reminders == nil {
		return Reminder{}, fmt.Errorf("当前未启用定时任务存储")
	}
	if strings.TrimSpace(id) == "" {
		return Reminder{}, fmt.Errorf("必须提供仓库更新订阅 id")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	for _, item := range r.reminders.Reminders() {
		if reminderIsRepositoryWatch(item) && item.OwnerID == ownerID && item.ID == id {
			return item, nil
		}
	}
	return Reminder{}, fmt.Errorf("没有找到属于目标用户的仓库更新订阅 %s", id)
}

func (r *Runtime) mutateRepositoryWatch(ownerID, id string, mutate func(*Reminder) error) (Reminder, error) {
	if r.reminders == nil {
		return Reminder{}, fmt.Errorf("当前未启用定时任务存储")
	}
	if strings.TrimSpace(id) == "" {
		return Reminder{}, fmt.Errorf("必须提供仓库更新订阅 id")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	for index := range items {
		item := &items[index]
		if !reminderIsRepositoryWatch(*item) || item.OwnerID != ownerID || item.ID != id {
			continue
		}
		if err := mutate(item); err != nil {
			return Reminder{}, err
		}
		if err := r.reminders.SaveReminders(items); err != nil {
			return Reminder{}, fmt.Errorf("保存仓库更新订阅失败: %w", err)
		}
		return *item, nil
	}
	return Reminder{}, fmt.Errorf("没有找到属于目标用户的仓库更新订阅 %s", id)
}

func (r *Runtime) deleteRepositoryWatch(ownerID, id string) (bool, error) {
	if r.reminders == nil {
		return false, fmt.Errorf("当前未启用定时任务存储")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	next := make([]Reminder, 0, len(items))
	removed := false
	for _, item := range items {
		if reminderIsRepositoryWatch(item) && item.OwnerID == ownerID && item.ID == id {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if removed {
		if err := r.reminders.SaveReminders(next); err != nil {
			return false, fmt.Errorf("删除仓库更新订阅失败: %w", err)
		}
	}
	return removed, nil
}

func reminderIsRepositoryWatch(item Reminder) bool {
	return item.Kind == ReminderKindRepositoryWatch && item.IntervalSeconds > 0 && strings.TrimSpace(item.Repository) != ""
}

func reminderIsRecurring(item Reminder) bool {
	return reminderIsScheduledQuery(item) || reminderIsRepositoryWatch(item) || reminderIsRSSWatch(item)
}
