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

const dianaRepositoryWatchToolName = "github_watch"

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
	managerGroupRoles, _, err := repositoryPublishEffectiveGroupRoles(settings)
	if err != nil {
		return nil
	}
	managed := map[string]bool{}
	userID, groupID := strings.TrimSpace(event.UserID), strings.TrimSpace(event.GroupID)
	if event.Kind != EventKindGroup || groupID == "" {
		for repository := range managerUsers[userID] {
			managed[repository] = true
		}
		if len(managed) == 0 {
			return nil
		}
		return managed
	}
	// 群里只认这个群自己被授权的仓库：按用户的授权只在私聊算数，不带进群，口径和
	// Issue 写操作保持一致（见 repositoryPublishUserScopedAllowed）。
	//
	// 按群授权带了身份要求时，这里只认事件自带的群身份，不额外回查成员信息：构造
	// 工具描述属于每条消息都会走的热路径，不值得为它多打一次平台接口。拿不到身份
	// 就按最严处理，该仓库不进这份清单。
	role := NormalizeGroupRole(event.SenderRole)
	for repository := range managerGroups[groupID] {
		if !managerGroupRoles.requirement(groupID, repository).satisfiedBy(role) {
			continue
		}
		managed[repository] = true
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
		`能改监控哪几类动态（Commit / PR / Issue / Release / Star），以及 PR、Issue、Release 各自只收哪几种。` +
		`新建的订阅推送到当前这个会话。只有主人和该仓库的管理人员能调用。` +
		`关注 RSS 或推特用户改用 kind=rss，普通周期任务改用 kind=schedule。`
}

func (*dianaRepositoryWatchTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作。cancel 只暂停并保留记录，delete 才彻底删除；run 是立刻检查一次。",
			"create", "list", "update", "cancel", "delete", "run"),
		"id":         toolStringParam("要操作的订阅 ID；update、cancel、delete、run 必填，可以先用 list 查。"),
		"repository": toolStringParam("仓库，写成 owner/repo 或 GitHub 链接；create 必填。"),
		"branch":     toolStringParam("要盯的分支，留空是默认分支。"),
		"interval":   toolStringParam("检查间隔，单位 " + durationUnitsHint + "。例如 30min、2h、1d。不短于 " + formatDurationUnits(minimumRepositoryWatchInterval) + "。"),
		"watch": toolEnumArrayParam("要监控的类型，可多选。create 省略按全部处理；update 省略表示不改。",
			"commits", "pull_requests", "issues", "releases", "stars"),
		"pull_request_events": toolEnumArrayParam("PR 只收这几种动态；省略表示新订阅默认全选，空数组表示全不选。", repositoryWatchPullEventKinds...),
		"issue_events":        toolEnumArrayParam("Issue 只收这几种动态；省略表示新订阅默认全选，空数组表示全不选。", repositoryWatchIssueEventKinds...),
		"release_kinds": toolEnumArrayParam("Release 只收这几种版本：stable 是正式版，prerelease 是预发布；"+
			"省略表示新订阅默认全选，空数组表示全不选。草稿任何时候都不推送。", repositoryWatchReleaseKindList...),
	})
}

type dianaRepositoryWatchView struct {
	ID                string   `json:"id"`
	Repository        string   `json:"repository"`
	Branch            string   `json:"branch,omitempty"`
	Interval          string   `json:"interval"`
	Watch             []string `json:"watch"`
	PullRequestEvents []string `json:"pull_request_events"`
	IssueEvents       []string `json:"issue_events"`
	ReleaseKinds      []string `json:"release_kinds"`
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
		Interval: formatDurationUnits(time.Duration(item.IntervalSeconds) * time.Second),
		Watch:    watch, Status: scheduleStatus(item), LastError: item.LastError,
		PullRequestEvents: EffectiveRepositoryWatchPullRequestEvents(item.WatchPullRequestEvents),
		IssueEvents:       EffectiveRepositoryWatchIssueEvents(item.WatchIssueEvents),
		ReleaseKinds:      EffectiveRepositoryWatchReleaseKinds(item.WatchReleaseKinds),
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

// allowsItem 判断这个会话能不能碰一条已有的订阅：先得是这台机器人名下的，再看仓库
// 权限。几台机器人共用一个 Runtime 时主人和仓库管理人员都是按机器人配的，A 这边
// 的权限不该伸到 B 在 WebUI 里建的订阅上。
func (t *dianaRepositoryWatchTool) allowsItem(item Reminder) bool {
	return t.runtime.sameProfileAsEvent(item.ProfileID, t.event) && t.allows(item.Repository)
}

// resolve 按 ID 找订阅，并顺带做权限判断。找不到和没权限回同一句话：不然
// 「没有这个订阅」和「有但你动不了」会把别人配的仓库名试探出来。
func (t *dianaRepositoryWatchTool) resolve(id string) (Reminder, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Reminder{}, fmt.Errorf("必须提供订阅 id，可以先用 list 查")
	}
	for _, item := range t.runtime.repositoryWatchItems() {
		if item.ID == id && t.allowsItem(item) {
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
			if t.allowsItem(item) {
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
		pullEvents, issueEvents, releaseKinds, err := repositoryWatchEventKindsFromTool(input)
		if err != nil {
			return "", err
		}
		item, err := t.runtime.CreateRepositoryWatch(ctx, RepositoryWatchCreateInput{
			Repository: repository, Branch: configToolString(input, "branch"), Interval: interval,
			WatchCommits: selection.Commits, WatchPullRequests: selection.PullRequests,
			WatchIssues: selection.Issues, WatchReleases: selection.Releases, WatchStars: selection.Stars,
			WatchPullRequestEvents: pullEvents, WatchIssueEvents: issueEvents, WatchReleaseKinds: releaseKinds,
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

func repositoryWatchEventKindsFromTool(input map[string]any) ([]string, []string, []string, error) {
	pullEvents, err := repositoryWatchEventKindFromTool(input, "pull_request_events", repositoryWatchPullEventKinds, "PR ")
	if err != nil {
		return nil, nil, nil, err
	}
	issueEvents, err := repositoryWatchEventKindFromTool(input, "issue_events", repositoryWatchIssueEventKinds, "Issue ")
	if err != nil {
		return nil, nil, nil, err
	}
	releaseKinds, err := repositoryWatchEventKindFromTool(input, "release_kinds", repositoryWatchReleaseKindList, "Release ")
	if err != nil {
		return nil, nil, nil, err
	}
	return pullEvents, issueEvents, releaseKinds, nil
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
		interval, err := parseFixedDurationUnits(raw)
		if err != nil {
			return RepositoryWatchUpdateInput{}, fmt.Errorf("周期格式不正确：%w", err)
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
		// 更新层用 nil 表示“没提”；显式空数组表示取消全部勾选。
		update.WatchPullRequestEvents = append([]string{}, events...)
	}
	if _, present := input["issue_events"]; present {
		events, eventsErr := repositoryWatchEventKindFromTool(input, "issue_events", repositoryWatchIssueEventKinds, "Issue ")
		if eventsErr != nil {
			return RepositoryWatchUpdateInput{}, eventsErr
		}
		update.WatchIssueEvents = append([]string{}, events...)
	}
	if _, present := input["release_kinds"]; present {
		kinds, kindsErr := repositoryWatchEventKindFromTool(input, "release_kinds", repositoryWatchReleaseKindList, "Release ")
		if kindsErr != nil {
			return RepositoryWatchUpdateInput{}, kindsErr
		}
		update.WatchReleaseKinds = append([]string{}, kinds...)
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

// CanonicalOperation 是 Run 实际执行的操作：没写按 list 算，add/edit/pause/remove/check
// 各算 create/update/cancel/delete/run。改或立即执行一条投递到当前会话以外（别的群、
// WebUI 配的投递目标）的订阅时带 _elsewhere，见 taskCanonicalOperation。
func (t *dianaRepositoryWatchTool) CanonicalOperation(input map[string]any) string {
	return t.runtime.taskCanonicalOperation(t.event, input, "list")
}
