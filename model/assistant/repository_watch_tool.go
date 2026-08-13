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

func defaultRepositoryWatchInterval(settings SettingValues) time.Duration {
	if strings.TrimSpace(settings.String(repositoryWatchSettingToken, "")) != "" {
		return authenticatedRepositoryWatchDefaultInterval
	}
	return anonymousRepositoryWatchDefaultInterval
}

type RepositoryWatchCreateInput struct {
	Repository       string
	Branch           string
	Interval         time.Duration
	WatchCommits     bool
	WatchReleases    bool
	Platform         string
	ProfileID        string
	ContextNamespace string
	OwnerID          string
	GroupID          string
	UserID           string
}

type RepositoryWatchUpdateInput struct {
	Repository    string
	Branch        *string
	Interval      time.Duration
	WatchCommits  *bool
	WatchReleases *bool
}

func (r *Runtime) CreateRepositoryWatch(ctx context.Context, input RepositoryWatchCreateInput) (Reminder, error) {
	pluginValue, settings, enabled := r.plugins.PluginWithSettings(repositoryWatchPluginID, nil)
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
	if !input.WatchCommits && !input.WatchReleases {
		return Reminder{}, fmt.Errorf("Commit 和 Release 不能同时关闭")
	}
	baseline, err := plugin.snapshot(ctx, repository, strings.TrimSpace(input.Branch), input.WatchCommits, input.WatchReleases, settings)
	if err != nil {
		return Reminder{}, fmt.Errorf("建立仓库基线失败: %w", err)
	}
	event := MessageEvent{
		Platform: strings.TrimSpace(input.Platform), ProfileID: strings.TrimSpace(input.ProfileID),
		ContextNamespace: strings.TrimSpace(input.ContextNamespace), UserID: strings.TrimSpace(input.UserID),
		GroupID: strings.TrimSpace(input.GroupID),
	}
	if event.GroupID != "" {
		event.Kind = EventKindGroup
	} else {
		event.Kind = EventKindPrivate
		if event.UserID == "" {
			return Reminder{}, fmt.Errorf("私聊通知必须填写发送对象 ID")
		}
	}
	ownerID := firstNonEmpty(strings.TrimSpace(input.OwnerID), repositoryWatchWebUIOwner(event.ProfileID))
	return r.addRepositoryWatch(event, ownerID, repository, strings.TrimSpace(input.Branch), interval, input.WatchCommits, input.WatchReleases, baseline)
}

func repositoryWatchWebUIOwner(profileID string) string {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return "webui"
	}
	return "webui:" + profileID
}

func (r *Runtime) UpdateRepositoryWatch(ctx context.Context, ownerID, id string, input RepositoryWatchUpdateInput) (Reminder, error) {
	pluginValue, settings, enabled := r.plugins.PluginWithSettings(repositoryWatchPluginID, nil)
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
	if input.WatchReleases != nil {
		values["watch_releases"] = *input.WatchReleases
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

func (r *Runtime) addRepositoryWatch(event MessageEvent, ownerID, repository, branch string, interval time.Duration, commits, releases bool, baseline repositoryWatchSnapshot) (Reminder, error) {
	if r.reminders == nil {
		return Reminder{}, fmt.Errorf("当前未启用定时任务存储")
	}
	if !commits && !releases {
		return Reminder{}, fmt.Errorf("watch_commits 和 watch_releases 不能同时关闭")
	}
	r.reminderMu.Lock()
	defer r.reminderMu.Unlock()
	items := r.reminders.Reminders()
	now := time.Now()
	item := Reminder{
		ID:               uuid.NewString()[:8],
		Kind:             ReminderKindRepositoryWatch,
		Platform:         event.Platform,
		ProfileID:        event.ProfileID,
		ContextNamespace: event.ContextNamespace,
		OwnerID:          strings.TrimSpace(ownerID),
		GroupID:          event.GroupID,
		UserID:           event.UserID,
		Message:          "监控 " + repository + " 的仓库动态",
		Repository:       repository,
		RepositoryBranch: branch,
		WatchCommits:     commits,
		WatchReleases:    releases,
		LastCommitSHA:    baseline.CommitSHA,
		LastReleaseTag:   baseline.ReleaseTag,
		TriggerAt:        now.Add(interval),
		IntervalSeconds:  int64(interval / time.Second),
		CreatedAt:        now,
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
	commits, releases := current.WatchCommits, current.WatchReleases
	if value, present := input["watch_commits"].(bool); present {
		commits = value
	}
	if value, present := input["watch_releases"].(bool); present {
		releases = value
	}
	if !commits && !releases {
		return Reminder{}, fmt.Errorf("watch_commits 和 watch_releases 不能同时关闭")
	}
	repositoryChanged := repository != current.Repository || branch != current.RepositoryBranch
	baselineCommits := repositoryChanged && commits || commits && !current.WatchCommits
	baselineReleases := repositoryChanged && releases || releases && !current.WatchReleases
	var baseline repositoryWatchSnapshot
	if baselineCommits || baselineReleases {
		baseline, err = plugin.snapshot(ctx, repository, branch, baselineCommits, baselineReleases, settings)
		if err != nil {
			return Reminder{}, fmt.Errorf("更新仓库基线失败: %w", err)
		}
	}
	return r.mutateRepositoryWatch(ownerID, id, func(item *Reminder) error {
		if !item.CancelledAt.IsZero() {
			return fmt.Errorf("仓库更新订阅 %s 已取消，不能修改", id)
		}
		if baselineCommits {
			item.LastCommitSHA = baseline.CommitSHA
		}
		if baselineReleases {
			item.LastReleaseTag = baseline.ReleaseTag
		}
		item.Repository = repository
		item.RepositoryBranch = branch
		item.WatchCommits = commits
		item.WatchReleases = releases
		item.Message = "监控 " + repository + " 的仓库动态"
		if rawInterval != "" {
			item.IntervalSeconds = int64(interval / time.Second)
		}
		item.TriggerAt = time.Now().Add(time.Duration(item.IntervalSeconds) * time.Second)
		item.PendingDelivery = ""
		item.PendingSince = time.Time{}
		item.LastError = ""
		item.ConsecutiveFailures = 0
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
