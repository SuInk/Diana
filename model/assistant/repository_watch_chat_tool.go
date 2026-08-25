// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const dianaRepositoryWatchToolName = "diana.repository_watch"

// repositoryWatchManagedRepositories 返回当前会话有权管的仓库集合。用的是「仓库
// Issue 发布」插件里那份管理人员名单——同一批人管 Issue，也就该管得了这个仓库的
// 订阅。草稿人不算：提草稿是提议，改订阅是直接改配置。
//
// 这里不查 Issue 写入白名单：那份名单管的是「能不能往仓库里写」，而订阅只是读。
func repositoryWatchManagedRepositories(event MessageEvent, settings SettingValues) map[string]bool {
	legacyUsers, err := repositoryPublishUserAccess(settings.String(repositoryPublishSettingUserAccess, ""))
	if err != nil {
		return nil
	}
	legacyGroups, err := repositoryPublishGroupAccess(settings.String(repositoryPublishSettingGroupAccess, ""))
	if err != nil {
		return nil
	}
	managerUsers, managerGroups, _, _, err := repositoryPublishEffectiveAccess(settings, legacyUsers, legacyGroups)
	if err != nil {
		return nil
	}
	managed := map[string]bool{}
	for repository := range managerUsers[strings.TrimSpace(event.UserID)] {
		managed[repository] = true
	}
	if event.Kind == EventKindGroup && strings.TrimSpace(event.GroupID) != "" {
		for repository := range managerGroups[strings.TrimSpace(event.GroupID)] {
			managed[repository] = true
		}
	}
	if len(managed) == 0 {
		return nil
	}
	return managed
}

type dianaRepositoryWatchTool struct {
	runtime  *Runtime
	event    MessageEvent
	owner    bool
	managed  map[string]bool
	settings SettingValues
}

func newDianaRepositoryWatchTool(runtime *Runtime, event MessageEvent, owner bool, managed map[string]bool, settings SettingValues) *dianaRepositoryWatchTool {
	return &dianaRepositoryWatchTool{runtime: runtime, event: event, owner: owner, managed: managed, settings: settings}
}

func (*dianaRepositoryWatchTool) Name() string { return dianaRepositoryWatchToolName }

func (*dianaRepositoryWatchTool) Description() string {
	return `管理 GitHub 仓库更新订阅：新建、查看、改设置、暂停、删除，也可以立刻检查一次。` +
		`能改监控哪几类动态（Commit / PR / Issue / Release / Star），以及 PR 和 Issue 各自只收哪几种动态。` +
		`新建的订阅推送到当前这个会话。只有主人和该仓库的管理人员能调用。` +
		`关注 RSS 或推特用户改用 diana.rss，普通周期任务改用 diana.schedule。`
}

func (*dianaRepositoryWatchTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作。cancel 只暂停并保留记录，delete 才彻底删除；run 是立刻检查一次。",
			"create", "list", "update", "cancel", "delete", "run"),
		"id":         toolStringParam("要操作的订阅 ID；update、cancel、delete、run 必填，可以先用 list 查。"),
		"repository": toolStringParam("仓库，写成 owner/repo 或 GitHub 链接；create 必填。"),
		"branch":     toolStringParam("要盯的分支，留空是默认分支。"),
		"interval":   toolStringParam("检查间隔，只接受 Go 时长写法：30s、1m、2h。不短于 " + minimumRepositoryWatchInterval.String() + "。"),
		"watch": toolEnumArrayParam("要监控的类型，可多选。create 省略按全部处理；update 省略表示不改。",
			"commits", "pull_requests", "issues", "releases", "stars"),
		"pull_request_events": toolEnumArrayParam("PR 只收这几种动态；省略或给全表示全都要。", repositoryWatchPullEventKinds...),
		"issue_events":        toolEnumArrayParam("Issue 只收这几种动态；省略或给全表示全都要。", repositoryWatchIssueEventKinds...),
	})
}

type dianaRepositoryWatchView struct {
	ID                string   `json:"id"`
	Repository        string   `json:"repository"`
	Branch            string   `json:"branch,omitempty"`
	Interval          string   `json:"interval"`
	Watch             []string `json:"watch"`
	PullRequestEvents []string `json:"pull_request_events,omitempty"`
	IssueEvents       []string `json:"issue_events,omitempty"`
	Status            string   `json:"status"`
	LastError         string   `json:"last_error,omitempty"`
	NextRunAt         string   `json:"next_run_at,omitempty"`
	LastRunAt         string   `json:"last_run_at,omitempty"`
}

type dianaRepositoryWatchResult struct {
	OK      bool                       `json:"ok"`
	Action  string                     `json:"action"`
	Message string                     `json:"message,omitempty"`
	Watch   *dianaRepositoryWatchView  `json:"watch,omitempty"`
	Items   []dianaRepositoryWatchView `json:"items,omitempty"`
}

func repositoryWatchViewForTool(item Reminder) dianaRepositoryWatchView {
	watch := make([]string, 0, 5)
	for _, pair := range []struct {
		on   bool
		name string
	}{
		{item.WatchCommits, "commits"}, {item.WatchPullRequests, "pull_requests"},
		{item.WatchIssues, "issues"}, {item.WatchReleases, "releases"}, {item.WatchStars, "stars"},
	} {
		if pair.on {
			watch = append(watch, pair.name)
		}
	}
	view := dianaRepositoryWatchView{
		ID: item.ID, Repository: item.Repository, Branch: item.RepositoryBranch,
		Interval: (time.Duration(item.IntervalSeconds) * time.Second).String(),
		Watch:    watch, Status: scheduleStatus(item), LastError: item.LastError,
		PullRequestEvents: append([]string(nil), item.WatchPullRequestEvents...),
		IssueEvents:       append([]string(nil), item.WatchIssueEvents...),
	}
	if !item.TriggerAt.IsZero() {
		view.NextRunAt = item.TriggerAt.Format(time.RFC3339)
	}
	if !item.LastRunAt.IsZero() {
		view.LastRunAt = item.LastRunAt.Format(time.RFC3339)
	}
	return view
}

// allows 判断这个会话能不能碰某个仓库的订阅。
func (t *dianaRepositoryWatchTool) allows(repository string) bool {
	if t.owner {
		return true
	}
	return t.managed[strings.ToLower(strings.TrimSpace(repository))]
}

// resolve 按 ID 找订阅，并顺带做权限判断。找不到和没权限回同一句话：不然
// 「没有这个订阅」和「有但你动不了」会把别人配的仓库名试探出来。
func (t *dianaRepositoryWatchTool) resolve(id string) (Reminder, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Reminder{}, fmt.Errorf("必须提供订阅 id，可以先用 list 查")
	}
	for _, item := range t.runtime.repositoryWatchItems() {
		if item.ID == id && t.allows(item.Repository) {
			return item, nil
		}
	}
	return Reminder{}, fmt.Errorf("没有找到可以操作的仓库订阅 %s", id)
}

func (t *dianaRepositoryWatchTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("diana repository watch: runtime is not configured")
	}
	if !t.owner && len(t.managed) == 0 {
		return "", fmt.Errorf("只有主人和该仓库的管理人员可以管理仓库订阅")
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		operation = "list"
	}
	switch operation {
	case "list":
		items := make([]dianaRepositoryWatchView, 0)
		for _, item := range t.runtime.repositoryWatchItems() {
			if t.allows(item.Repository) {
				items = append(items, repositoryWatchViewForTool(item))
			}
		}
		return marshalRepositoryWatchToolResult(dianaRepositoryWatchResult{
			OK: true, Action: "listed", Items: items,
			Message: fmt.Sprintf("当前有 %d 个能管的仓库订阅。", len(items)),
		})
	case "create", "add":
		repository, err := normalizeGitHubRepository(configToolString(input, "repository"))
		if err != nil {
			return "", err
		}
		if !t.allows(repository) {
			return "", fmt.Errorf("没有 %s 的管理权限", repository)
		}
		interval, err := parseRepositoryWatchInterval(configToolString(input, "interval"), t.settings)
		if err != nil {
			return "", err
		}
		selection, provided, err := repositoryWatchSelectionFromTool(input)
		if err != nil {
			return "", err
		}
		if !provided {
			selection = repositoryWatchSelection{Commits: true, PullRequests: true, Issues: true, Releases: true, Stars: true}
		}
		pullEvents, issueEvents, err := repositoryWatchEventKindsFromTool(input)
		if err != nil {
			return "", err
		}
		item, err := t.runtime.CreateRepositoryWatch(ctx, RepositoryWatchCreateInput{
			Repository: repository, Branch: configToolString(input, "branch"), Interval: interval,
			WatchCommits: selection.Commits, WatchPullRequests: selection.PullRequests,
			WatchIssues: selection.Issues, WatchReleases: selection.Releases, WatchStars: selection.Stars,
			WatchPullRequestEvents: pullEvents, WatchIssueEvents: issueEvents,
			Platform: t.event.Platform, ProfileID: t.event.ProfileID, ContextNamespace: t.event.ContextNamespace,
			OwnerID: strings.TrimSpace(t.event.UserID), GroupID: t.event.GroupID, UserID: t.event.UserID,
			NotificationEnabled: true,
		})
		if err != nil {
			return "", err
		}
		view := repositoryWatchViewForTool(item)
		return marshalRepositoryWatchToolResult(dianaRepositoryWatchResult{
			OK: true, Action: "created", Watch: &view,
			Message: "订阅已创建，当前状态作为基线，不补发历史动态；之后的更新会发到这里。",
		})
	case "update", "edit":
		current, err := t.resolve(configToolString(input, "id"))
		if err != nil {
			return "", err
		}
		update, err := repositoryWatchUpdateFromTool(input, current)
		if err != nil {
			return "", err
		}
		item, err := t.runtime.UpdateRepositoryWatch(ctx, current.OwnerID, current.ID, update)
		if err != nil {
			return "", err
		}
		view := repositoryWatchViewForTool(item)
		return marshalRepositoryWatchToolResult(dianaRepositoryWatchResult{OK: true, Action: "updated", Watch: &view, Message: "仓库订阅已更新。"})
	case "cancel", "pause":
		current, err := t.resolve(configToolString(input, "id"))
		if err != nil {
			return "", err
		}
		item, err := t.runtime.CancelRepositoryWatch(current.OwnerID, current.ID)
		if err != nil {
			return "", err
		}
		view := repositoryWatchViewForTool(item)
		return marshalRepositoryWatchToolResult(dianaRepositoryWatchResult{OK: true, Action: "cancelled", Watch: &view, Message: "仓库订阅已暂停，记录还在。"})
	case "delete", "remove":
		current, err := t.resolve(configToolString(input, "id"))
		if err != nil {
			return "", err
		}
		removed, err := t.runtime.DeleteRepositoryWatch(current.OwnerID, current.ID)
		if err != nil {
			return "", err
		}
		if !removed {
			return "", fmt.Errorf("没有找到仓库订阅 %s", current.ID)
		}
		return marshalRepositoryWatchToolResult(dianaRepositoryWatchResult{OK: true, Action: "deleted", Message: "仓库订阅已删除。"})
	case "run", "check":
		current, err := t.resolve(configToolString(input, "id"))
		if err != nil {
			return "", err
		}
		item, err := t.runtime.RunRepositoryWatchNow(current.OwnerID, current.ID)
		if err != nil {
			return "", err
		}
		view := repositoryWatchViewForTool(item)
		return marshalRepositoryWatchToolResult(dianaRepositoryWatchResult{OK: true, Action: "queued", Watch: &view, Message: "已排到下一秒检查，有更新会推过来。"})
	default:
		return "", fmt.Errorf("operation 必须是 create、list、update、cancel、delete 或 run")
	}
}

// repositoryWatchSelectionFromTool 读 watch 数组。第二个返回值说明用户到底传没传
// 这个字段——create 时省略是「全都要」，update 时省略是「别动」，两者不能混。
func repositoryWatchSelectionFromTool(input map[string]any) (repositoryWatchSelection, bool, error) {
	raw, present := input["watch"]
	if !present {
		return repositoryWatchSelection{}, false, nil
	}
	values, err := toolStringValues(raw)
	if err != nil {
		return repositoryWatchSelection{}, false, fmt.Errorf("watch 必须是字符串数组")
	}
	selection := repositoryWatchSelection{}
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "commits", "commit":
			selection.Commits = true
		case "pull_requests", "pull_request", "pr", "prs":
			selection.PullRequests = true
		case "issues", "issue":
			selection.Issues = true
		case "releases", "release":
			selection.Releases = true
		case "stars", "star":
			selection.Stars = true
		case "":
			continue
		default:
			return repositoryWatchSelection{}, false, fmt.Errorf("watch 只能是 commits、pull_requests、issues、releases、stars")
		}
	}
	if !selection.Commits && !selection.PullRequests && !selection.Issues && !selection.Releases && !selection.Stars {
		return repositoryWatchSelection{}, false, fmt.Errorf("watch 至少要留一项")
	}
	return selection, true, nil
}

func repositoryWatchEventKindsFromTool(input map[string]any) ([]string, []string, error) {
	pullEvents, err := repositoryWatchEventKindFromTool(input, "pull_request_events", repositoryWatchPullEventKinds, "PR ")
	if err != nil {
		return nil, nil, err
	}
	issueEvents, err := repositoryWatchEventKindFromTool(input, "issue_events", repositoryWatchIssueEventKinds, "Issue ")
	if err != nil {
		return nil, nil, err
	}
	return pullEvents, issueEvents, nil
}

func repositoryWatchEventKindFromTool(input map[string]any, key string, allowed []string, label string) ([]string, error) {
	raw, present := input[key]
	if !present {
		return nil, nil
	}
	values, err := toolStringValues(raw)
	if err != nil {
		return nil, fmt.Errorf("%s必须是字符串数组", key)
	}
	return normalizeRepositoryWatchEvents(values, allowed, label)
}

func repositoryWatchUpdateFromTool(input map[string]any, current Reminder) (RepositoryWatchUpdateInput, error) {
	update := RepositoryWatchUpdateInput{Repository: current.Repository}
	if raw, present := input["repository"]; present && strings.TrimSpace(stringFromAny(raw)) != "" {
		repository, err := normalizeGitHubRepository(stringFromAny(raw))
		if err != nil {
			return RepositoryWatchUpdateInput{}, err
		}
		update.Repository = repository
	}
	if raw, present := input["branch"]; present {
		branch := strings.TrimSpace(stringFromAny(raw))
		update.Branch = &branch
	}
	if raw := strings.TrimSpace(configToolString(input, "interval")); raw != "" {
		interval, err := time.ParseDuration(strings.ToLower(raw))
		if err != nil {
			return RepositoryWatchUpdateInput{}, fmt.Errorf("周期格式不正确，请使用 30s、1m、2h 这类格式")
		}
		update.Interval = interval
	}
	selection, provided, err := repositoryWatchSelectionFromTool(input)
	if err != nil {
		return RepositoryWatchUpdateInput{}, err
	}
	if provided {
		update.WatchCommits, update.WatchPullRequests = &selection.Commits, &selection.PullRequests
		update.WatchIssues, update.WatchReleases = &selection.Issues, &selection.Releases
		update.WatchStars = &selection.Stars
	}
	if _, present := input["pull_request_events"]; present {
		events, eventsErr := repositoryWatchEventKindFromTool(input, "pull_request_events", repositoryWatchPullEventKinds, "PR ")
		if eventsErr != nil {
			return RepositoryWatchUpdateInput{}, eventsErr
		}
		// nil 会被更新层当成「没提」，而清空正是「改回全部」的表达方式。
		update.WatchPullRequestEvents = append([]string{}, events...)
	}
	if _, present := input["issue_events"]; present {
		events, eventsErr := repositoryWatchEventKindFromTool(input, "issue_events", repositoryWatchIssueEventKinds, "Issue ")
		if eventsErr != nil {
			return RepositoryWatchUpdateInput{}, eventsErr
		}
		update.WatchIssueEvents = append([]string{}, events...)
	}
	return update, nil
}

func marshalRepositoryWatchToolResult(result dianaRepositoryWatchResult) (string, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("序列化仓库订阅结果失败: %w", err)
	}
	return string(body), nil
}
