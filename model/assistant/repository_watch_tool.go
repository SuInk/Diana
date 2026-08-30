// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"slices"
	"sort"
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

const (
	starNotifyModeGrowth       = "growth"
	starNotifyModeMilestone    = "milestone"
	maximumStarNotifyThreshold = 1_000_000
	maximumStarMilestones      = 100
)

// PR 和 Issue 各自的动态种类。订阅时可以只挑其中几种：一个只关心「合并了什么」
// 的群，不需要每条评论都被顶一次。空集合表示全要——老订阅没有这个字段，不能因为
// 加了开关就把它们静音。
var (
	repositoryWatchPullEventKinds  = []string{"opened", "updated", "closed", "merged"}
	repositoryWatchIssueEventKinds = []string{"opened", "updated", "closed", "reopened"}
)

func normalizeRepositoryWatchEvents(values []string, allowed []string, label string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if !slices.Contains(allowed, value) {
			return nil, fmt.Errorf("%s动态种类只能是 %s", label, strings.Join(allowed, "、"))
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 || len(out) == len(allowed) {
		// 全选和没选过都存成空：回显时是同一句「全部」，也省掉一次无谓的迁移。
		return nil, nil
	}
	// 按固定顺序存，回显和比较都不受用户勾选顺序影响。
	sorted := make([]string, 0, len(out))
	for _, kind := range allowed {
		if slices.Contains(out, kind) {
			sorted = append(sorted, kind)
		}
	}
	return sorted, nil
}

func normalizeStarNotifyMode(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return starNotifyModeGrowth, nil
	}
	if value != starNotifyModeGrowth && value != starNotifyModeMilestone {
		return "", fmt.Errorf("Star 通知模式必须是 growth 或 milestone")
	}
	return value, nil
}

func normalizeStarNotifyThreshold(value int) (int, error) {
	if value == 0 {
		return 1, nil
	}
	if value < 1 || value > maximumStarNotifyThreshold {
		return 0, fmt.Errorf("Star 增长通知阈值必须在 1 到 %d 之间", maximumStarNotifyThreshold)
	}
	return value, nil
}

func normalizeStarNotifyMilestones(values []int) ([]int, error) {
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if value < 1 || value > maximumStarNotifyThreshold {
			return nil, fmt.Errorf("Star 里程碑必须在 1 到 %d 之间", maximumStarNotifyThreshold)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) > maximumStarMilestones {
		return nil, fmt.Errorf("Star 里程碑最多设置 %d 个", maximumStarMilestones)
	}
	sort.Ints(out)
	return out, nil
}

type RepositoryWatchCreateInput struct {
	Repository             string
	Branch                 string
	Interval               time.Duration
	WatchCommits           bool
	WatchPullRequests      bool
	WatchPullRequestEvents []string
	WatchIssueEvents       []string
	WatchIssues            bool
	WatchReleases          bool
	WatchStars             bool
	StarNotifyMode         string
	StarNotifyThreshold    int
	StarNotifyMilestones   []int
	Platform               string
	ProfileID              string
	ContextNamespace       string
	OwnerID                string
	GroupID                string
	UserID                 string
	NotificationEnabled    bool
	NotificationTargets    []ReminderDeliveryTarget
}

type RepositoryWatchUpdateInput struct {
	Repository             string
	Branch                 *string
	Interval               time.Duration
	WatchCommits           *bool
	WatchPullRequests      *bool
	WatchPullRequestEvents []string
	WatchIssueEvents       []string
	WatchIssues            *bool
	WatchReleases          *bool
	WatchStars             *bool
	StarNotifyMode         *string
	StarNotifyThreshold    *int
	StarNotifyMilestones   []int
	Delivery               bool
	Platform               string
	ProfileID              string
	ContextNamespace       string
	OwnerID                string
	GroupID                string
	UserID                 string
	NotificationEnabled    *bool
	NotificationTargets    []ReminderDeliveryTarget
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
	pullEvents, err := normalizeRepositoryWatchEvents(input.WatchPullRequestEvents, repositoryWatchPullEventKinds, "PR ")
	if err != nil {
		return Reminder{}, err
	}
	issueEvents, err := normalizeRepositoryWatchEvents(input.WatchIssueEvents, repositoryWatchIssueEventKinds, "Issue ")
	if err != nil {
		return Reminder{}, err
	}
	selection := repositoryWatchSelection{
		Commits: input.WatchCommits, PullRequests: input.WatchPullRequests, Issues: input.WatchIssues,
		Releases: input.WatchReleases, Stars: input.WatchStars,
		PullRequestEvents: pullEvents, IssueEvents: issueEvents,
	}
	if !selection.Commits && !selection.PullRequests && !selection.Issues && !selection.Releases && !selection.Stars {
		return Reminder{}, fmt.Errorf("Commit、PR、Issue、Release 和 Star 至少启用一项")
	}
	starThreshold, err := normalizeStarNotifyThreshold(input.StarNotifyThreshold)
	if err != nil {
		return Reminder{}, err
	}
	starMode, err := normalizeStarNotifyMode(input.StarNotifyMode)
	if err != nil {
		return Reminder{}, err
	}
	starMilestones, err := normalizeStarNotifyMilestones(input.StarNotifyMilestones)
	if err != nil {
		return Reminder{}, err
	}
	if starMode == starNotifyModeMilestone && len(starMilestones) == 0 {
		return Reminder{}, fmt.Errorf("里程碑模式至少需要一个 Star 里程碑")
	}
	baseline, err := plugin.snapshotSelected(ctx, repository, strings.TrimSpace(input.Branch), selection, settings)
	if err != nil {
		return Reminder{}, fmt.Errorf("建立仓库基线失败: %w", err)
	}
	ownerID := firstNonEmpty(strings.TrimSpace(input.OwnerID), repositoryWatchWebUIOwner(event.ProfileID))
	return r.addRepositoryWatch(event, ownerID, repository, strings.TrimSpace(input.Branch), interval, selection, baseline, starMode, starThreshold, starMilestones, input.NotificationEnabled, input.NotificationTargets)
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
	if input.WatchPullRequestEvents != nil {
		values["watch_pull_request_events"] = input.WatchPullRequestEvents
	}
	if input.WatchIssueEvents != nil {
		values["watch_issue_events"] = input.WatchIssueEvents
	}
	if input.WatchIssues != nil {
		values["watch_issues"] = *input.WatchIssues
	}
	if input.WatchReleases != nil {
		values["watch_releases"] = *input.WatchReleases
	}
	if input.WatchStars != nil {
		values["watch_stars"] = *input.WatchStars
	}
	if input.StarNotifyMode != nil {
		values["star_notify_mode"] = *input.StarNotifyMode
	}
	if input.StarNotifyThreshold != nil {
		values["star_notify_threshold"] = *input.StarNotifyThreshold
	}
	if input.StarNotifyMilestones != nil {
		values["star_notify_milestones"] = input.StarNotifyMilestones
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

// RunRepositoryWatchNow 把下一次触发时间提前到现在；调度循环每秒扫一次，
// 会立即执行检查，退避等待中的积压通知也会先补发。
func (r *Runtime) RunRepositoryWatchNow(ownerID, id string) (Reminder, error) {
	return r.mutateRepositoryWatch(strings.TrimSpace(ownerID), strings.TrimSpace(id), func(item *Reminder) error {
		if !item.CancelledAt.IsZero() {
			return fmt.Errorf("仓库更新订阅 %s 已取消，无法立即检查", id)
		}
		item.TriggerAt = time.Now()
		return nil
	})
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

func (r *Runtime) addRepositoryWatch(event MessageEvent, ownerID, repository, branch string, interval time.Duration, selection repositoryWatchSelection, baseline repositoryWatchSnapshot, starNotifyMode string, starNotifyThreshold int, starNotifyMilestones []int, notificationEnabled bool, targets []ReminderDeliveryTarget) (Reminder, error) {
	if r.reminders == nil {
		return Reminder{}, fmt.Errorf("当前未启用定时任务存储")
	}
	if !selection.Commits && !selection.PullRequests && !selection.Issues && !selection.Releases && !selection.Stars {
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
		WatchPullRequestEvents:  append([]string(nil), selection.PullRequestEvents...),
		WatchIssueEvents:        append([]string(nil), selection.IssueEvents...),
		WatchIssues:             selection.Issues,
		WatchReleases:           selection.Releases,
		WatchStars:              selection.Stars,
		StarNotifyMode:          starNotifyMode,
		StarNotifyThreshold:     starNotifyThreshold,
		StarNotifyMilestones:    append([]int(nil), starNotifyMilestones...),
		LastCommitSHA:           baseline.CommitSHA,
		LastPullRequestCursor:   baseline.PullRequestCursor,
		LastIssueCursor:         baseline.IssueCursor,
		LastReleaseTag:          baseline.ReleaseTag,
		LastStarCount:           baseline.StarCount,
		LastNotifiedStarCount:   baseline.StarCount,
		LastStarEventID:         baseline.StarEventID,
		LastStarEventAt:         baseline.StarEventAt,
		TriggerAt:               now.Add(interval),
		IntervalSeconds:         int64(interval / time.Second),
		CreatedAt:               now,
	}
	if existing, ok := findOverlappingRepositoryWatch(items, item); ok {
		// 两份订阅各记各的游标、各轮各的，同一条动态就会一字不差地发两遍。
		// 模型被反复要求「盯一下这个仓库」时很容易再建一份，这里直接挡住并把
		// 已有的 id 报出去，让它改成 update。
		return Reminder{}, fmt.Errorf("这里已经有一份 %s 的仓库更新订阅（id %s），要调整监控类型或周期请用 update，不要新建", existing.Repository, existing.ID)
	}
	if err := r.reminders.SaveReminders(append(items, item)); err != nil {
		return Reminder{}, fmt.Errorf("保存仓库更新订阅失败: %w", err)
	}
	return item, nil
}

// findOverlappingRepositoryWatch 找出会和 candidate 撞车的既有订阅：同一个仓库、
// 同一个分支，且至少有一个投递目标重合。仓库名和分支名都按大小写不敏感比对，
// GitHub 两者都不区分。
func findOverlappingRepositoryWatch(items []Reminder, candidate Reminder) (Reminder, bool) {
	targets := repositoryWatchDeliveryKeys(candidate)
	if len(targets) == 0 {
		return Reminder{}, false
	}
	for _, existing := range items {
		if !reminderIsRepositoryWatch(existing) || !existing.CancelledAt.IsZero() {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(existing.Repository), strings.TrimSpace(candidate.Repository)) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(existing.RepositoryBranch), strings.TrimSpace(candidate.RepositoryBranch)) {
			continue
		}
		for key := range repositoryWatchDeliveryKeys(existing) {
			if _, ok := targets[key]; ok {
				return existing, true
			}
		}
	}
	return Reminder{}, false
}

func repositoryWatchDeliveryKeys(item Reminder) map[string]struct{} {
	targets := repositoryWatchDeliveryTargets(item)
	keys := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		keys[messageEventDeliveryKey(target)] = struct{}{}
	}
	return keys
}

func (r *Runtime) cancelRepositoryWatch(ownerID, id string) (Reminder, error) {
	return r.mutateRepositoryWatch(ownerID, id, func(item *Reminder) error {
		if !item.CancelledAt.IsZero() {
			return fmt.Errorf("仓库更新订阅 %s 已经取消", id)
		}
		item.CancelledAt = time.Now()
		item.PendingDelivery = ""
		item.PendingDeliveryReference = ""
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
		Issues: current.WatchIssues, Releases: current.WatchReleases, Stars: current.WatchStars,
		PullRequestEvents: append([]string(nil), current.WatchPullRequestEvents...),
		IssueEvents:       append([]string(nil), current.WatchIssueEvents...),
	}
	if value, present := input["watch_commits"].(bool); present {
		selection.Commits = value
	}
	if value, present := input["watch_pull_requests"].(bool); present {
		selection.PullRequests = value
	}
	if value, present := input["watch_pull_request_events"]; present {
		raw, ok := value.([]string)
		if !ok {
			return Reminder{}, fmt.Errorf("PR 动态种类格式无效")
		}
		parsed, parseErr := normalizeRepositoryWatchEvents(raw, repositoryWatchPullEventKinds, "PR ")
		if parseErr != nil {
			return Reminder{}, parseErr
		}
		selection.PullRequestEvents = parsed
	}
	if value, present := input["watch_issue_events"]; present {
		raw, ok := value.([]string)
		if !ok {
			return Reminder{}, fmt.Errorf("Issue 动态种类格式无效")
		}
		parsed, parseErr := normalizeRepositoryWatchEvents(raw, repositoryWatchIssueEventKinds, "Issue ")
		if parseErr != nil {
			return Reminder{}, parseErr
		}
		selection.IssueEvents = parsed
	}
	if value, present := input["watch_issues"].(bool); present {
		selection.Issues = value
	}
	if value, present := input["watch_releases"].(bool); present {
		selection.Releases = value
	}
	if value, present := input["watch_stars"].(bool); present {
		selection.Stars = value
	}
	starNotifyThreshold := current.StarNotifyThreshold
	if starNotifyThreshold <= 0 {
		starNotifyThreshold = 1
	}
	starNotifyMode, _ := normalizeStarNotifyMode(current.StarNotifyMode)
	starNotifyMilestones, _ := normalizeStarNotifyMilestones(current.StarNotifyMilestones)
	starConfigProvided, starConfigChanged := false, false
	if value, present := input["star_notify_threshold"]; present {
		starConfigProvided = true
		parsed, parseErr := normalizeStarNotifyThreshold(intFromAny(value))
		if parseErr != nil {
			return Reminder{}, parseErr
		}
		starConfigChanged = current.StarNotifyThreshold <= 0 || parsed != starNotifyThreshold
		starNotifyThreshold = parsed
	}
	if value, present := input["star_notify_mode"]; present {
		starConfigProvided = true
		parsed, parseErr := normalizeStarNotifyMode(stringFromAny(value))
		if parseErr != nil {
			return Reminder{}, parseErr
		}
		starConfigChanged = starConfigChanged || parsed != starNotifyMode
		starNotifyMode = parsed
	}
	if value, present := input["star_notify_milestones"]; present {
		starConfigProvided = true
		raw, ok := value.([]int)
		if !ok {
			return Reminder{}, fmt.Errorf("Star 里程碑格式无效")
		}
		parsed, parseErr := normalizeStarNotifyMilestones(raw)
		if parseErr != nil {
			return Reminder{}, parseErr
		}
		starConfigChanged = starConfigChanged || !slices.Equal(parsed, starNotifyMilestones)
		starNotifyMilestones = parsed
	}
	if starNotifyMode == starNotifyModeMilestone && len(starNotifyMilestones) == 0 {
		return Reminder{}, fmt.Errorf("里程碑模式至少需要一个 Star 里程碑")
	}
	if !selection.Commits && !selection.PullRequests && !selection.Issues && !selection.Releases && !selection.Stars {
		return Reminder{}, fmt.Errorf("仓库动态监控类型不能全部关闭")
	}
	repositoryChanged := repository != current.Repository || branch != current.RepositoryBranch
	baselineSelection := repositoryWatchSelection{
		Commits:      repositoryChanged && selection.Commits || selection.Commits && !current.WatchCommits,
		PullRequests: repositoryChanged && selection.PullRequests || selection.PullRequests && !current.WatchPullRequests,
		Issues:       repositoryChanged && selection.Issues || selection.Issues && !current.WatchIssues,
		Releases:     repositoryChanged && selection.Releases || selection.Releases && !current.WatchReleases,
		Stars:        repositoryChanged && selection.Stars || selection.Stars && !current.WatchStars,
	}
	var baseline repositoryWatchSnapshot
	if baselineSelection.Commits || baselineSelection.PullRequests || baselineSelection.Issues || baselineSelection.Releases || baselineSelection.Stars {
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
		if baselineSelection.Issues {
			item.LastIssueCursor = baseline.IssueCursor
		}
		if baselineSelection.Releases {
			item.LastReleaseTag = baseline.ReleaseTag
		}
		if baselineSelection.Stars {
			item.LastStarCount = baseline.StarCount
			item.LastNotifiedStarCount = baseline.StarCount
			item.LastStarEventID = baseline.StarEventID
			item.LastStarEventAt = baseline.StarEventAt
		}
		if starConfigChanged {
			item.LastNotifiedStarCount = item.LastStarCount
		}
		item.Repository = repository
		item.RepositoryBranch = branch
		item.WatchCommits = selection.Commits
		item.WatchPullRequests = selection.PullRequests
		item.WatchPullRequestEvents = append([]string(nil), selection.PullRequestEvents...)
		item.WatchIssueEvents = append([]string(nil), selection.IssueEvents...)
		item.WatchIssues = selection.Issues
		item.WatchReleases = selection.Releases
		item.WatchStars = selection.Stars
		if starConfigProvided {
			item.StarNotifyMode = starNotifyMode
			item.StarNotifyThreshold = starNotifyThreshold
			item.StarNotifyMilestones = append([]int(nil), starNotifyMilestones...)
		}
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
		item.PendingDeliveryReference = ""
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

// repositoryWatchItems 返回全部仓库订阅，不按 owner 过滤：聊天里改订阅是按仓库
// 的管理权限判断的，调用方拿到之后自己筛。
func (r *Runtime) repositoryWatchItems() []Reminder {
	if r.reminders == nil {
		return nil
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	var out []Reminder
	for _, item := range r.reminders.Reminders() {
		if reminderIsRepositoryWatch(item) {
			out = append(out, item)
		}
	}
	return out
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
