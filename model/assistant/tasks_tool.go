// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/internal/secretmask"
)

type dianaTasksTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaTasksResult struct {
	OK      bool        `json:"ok"`
	Action  string      `json:"action"`
	Scope   string      `json:"scope"`
	Message string      `json:"message"`
	Items   []dianaTask `json:"items"`
}

type dianaTask struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	OwnerID string `json:"owner_id"`
	GroupID string `json:"group_id,omitempty"`
	UserID  string `json:"user_id,omitempty"`
	Message string `json:"message"`
	Status  string `json:"status"`
	// HeldBySafeMode 表示机器人在安全模式，这条任务往当前会话以外投递，到点暂停发送；
	// 任务保留，切回标准模式后恢复。
	HeldBySafeMode bool      `json:"held_by_safe_mode,omitempty"`
	TriggerAt      time.Time `json:"trigger_at"`
	Interval       string    `json:"interval,omitempty"`
	// Trigger 是事件触发任务的条件摘要，其他种类为空。
	Trigger               string    `json:"trigger,omitempty"`
	LastRunAt             time.Time `json:"last_run_at,omitempty"`
	CancelledAt           time.Time `json:"cancelled_at,omitempty"`
	LastError             string    `json:"last_error,omitempty"`
	ConsecutiveFailures   int       `json:"consecutive_failures,omitempty"`
	PendingDelivery       bool      `json:"pending_delivery,omitempty"`
	Repository            string    `json:"repository,omitempty"`
	RepositoryBranch      string    `json:"repository_branch,omitempty"`
	WatchCommits          bool      `json:"watch_commits,omitempty"`
	WatchPullRequests     bool      `json:"watch_pull_requests,omitempty"`
	PullRequestEvents     []string  `json:"watch_pull_request_events"`
	IssueEvents           []string  `json:"watch_issue_events"`
	WatchIssues           bool      `json:"watch_issues,omitempty"`
	WatchReleases         bool      `json:"watch_releases,omitempty"`
	ReleaseKinds          []string  `json:"watch_release_kinds"`
	WatchStars            bool      `json:"watch_stars,omitempty"`
	StarNotifyMode        string    `json:"star_notify_mode,omitempty"`
	StarNotifyThreshold   int       `json:"star_notify_threshold,omitempty"`
	StarNotifyMilestones  []int     `json:"star_notify_milestones,omitempty"`
	LastCommitSHA         string    `json:"last_commit_sha,omitempty"`
	LastPullRequestCursor string    `json:"last_pull_request_cursor,omitempty"`
	LastReleaseTag        string    `json:"last_release_tag,omitempty"`
	LastStarCount         int       `json:"last_star_count,omitempty"`
	LastNotifiedStarCount int       `json:"last_notified_star_count,omitempty"`
	// FeedSources 是多来源订阅盯的全部账号或 Feed；单来源时和 feed_url 一致。
	FeedSources     []string  `json:"feed_sources,omitempty"`
	FeedURL         string    `json:"feed_url,omitempty"`
	FeedSource      string    `json:"feed_source,omitempty"`
	FeedHandle      string    `json:"feed_handle,omitempty"`
	FeedJudgePrompt string    `json:"feed_judge_prompt,omitempty"`
	LastFeedItemID  string    `json:"last_feed_item_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	ConsumesQuota   bool      `json:"consumes_quota"`
}

func newDianaTasksTool(runtime *Runtime, event MessageEvent) *dianaTasksTool {
	return &dianaTasksTool{runtime: runtime, event: event}
}

func (t *dianaTasksTool) Name() string {
	return "tasks"
}

func (t *dianaTasksTool) Description() string {
	return `一次查询持久化存储中的全部一次性提醒、事件触发任务和周期订阅，含运行中、已使用、已取消状态以及是否占用额度。用户问「我的所有任务/提醒/订阅」「现在有哪些定时任务」时必须使用本工具，不要分别猜测。`
}

func (t *dianaTasksTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("要执行的操作。", "list"),
		"scope": toolEnumParam("查询范围。默认 mine 只返回当前用户；all 查询所有用户，仅机器人主人可用。",
			"mine", "all"),
		"target_user_id": toolStringParam("查询指定用户的任务，仅机器人主人可用。"),
	})
}

func (t *dianaTasksTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil || t.runtime.reminders == nil {
		return "", fmt.Errorf("当前未启用任务存储")
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		operation = "list"
	}
	if operation != "list" {
		return "", fmt.Errorf("operation 必须是 list")
	}
	scope := strings.ToLower(strings.TrimSpace(configToolString(input, "scope")))
	if scope == "" {
		scope = "mine"
	}
	if scope != "mine" && scope != "all" {
		return "", fmt.Errorf("scope 必须是 mine 或 all")
	}
	requester := t.runtime.relationshipPolicy(ctx, t.event)
	targetID := normalizeRelationshipUserID(configToolString(input, "target_user_id"))
	if configToolString(input, "target_user_id") != "" && targetID == "" {
		return "", fmt.Errorf("target_user_id 必须是有效账号")
	}
	if scope == "all" && !requester.Owner {
		return "", fmt.Errorf("只有主人可以查询所有用户的提醒和订阅")
	}
	if targetID != "" && targetID != t.event.UserID && !requester.Owner {
		return "", fmt.Errorf("只有主人可以查询其他用户的提醒和订阅")
	}
	if targetID != "" {
		scope = "target:" + targetID
	}

	t.runtime.reminderMu.Lock()
	stored := t.runtime.reminders.Reminders()
	t.runtime.reminderMu.Unlock()
	// 查别人的（全部或指定用户）只能查这台机器人名下的：主人权限是按机器人给的，
	// 同一个 Runtime 里另一台机器人的用户和订阅不归这位主人管。查自己的不按机器人
	// 过滤，和额度的统计口径保持一致。
	crossUser := scope == "all" || (targetID != "" && targetID != t.event.UserID)
	holds := t.runtime.safeModeHoldChecker()
	now := time.Now()
	items := make([]dianaTask, 0, len(stored))
	for _, item := range stored {
		if crossUser && !t.runtime.sameProfileAsEvent(item.ProfileID, t.event) {
			continue
		}
		if scope == "mine" && item.OwnerID != t.event.UserID {
			continue
		}
		if targetID != "" && item.OwnerID != targetID {
			continue
		}
		task := taskForTool(item)
		task.HeldBySafeMode = reminderStillPending(item, now) && holds(item)
		items = append(items, task)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].ID < items[j].ID
	})
	body, err := json.MarshalIndent(dianaTasksResult{
		OK:      true,
		Action:  "listed",
		Scope:   scope,
		Message: fmt.Sprintf("已读取 %d 个一次性提醒和周期订阅。", len(items)),
		Items:   items,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// taskCanonicalOperation 给提醒和订阅按操作拦截用，和工具自己的换算一致：add 算
// create、edit 算 update、remove 算 delete、check 算 run，defaultOp 是工具对空
// operation 的缺省。
//
// 建、改、立即执行的任务投递到的不是当前会话时，名字加上 _elsewhere：那是一条定时往
// 别人私聊或别的群发消息的通道，周期查询还会在那边跑一轮 Agent。判断规则：
//   - create：私聊里 target_user_id 指向别人算别处；群里建的任务投递回当前群，
//     target_user_id 只决定@谁、算谁的额度，不算别处；
//   - update / run：target_user_id 指向别人，或者按 id 找到的那条已有任务投递到当前会话
//     以外（别的群、别人的私聊、WebUI 配的投递目标、别的机器人），都算别处——改的是
//     发往那边的话，立即执行就是现在往那边发。
//
// cancel / delete 不分别处：它们只会让任务少发，不会往外发新东西；按 id 碰到别的
// 机器人的任务由各工具的归属检查挡住（见 taskOfOtherBot）。
func (r *Runtime) taskCanonicalOperation(event MessageEvent, input map[string]any, defaultOp string) string {
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		operation = defaultOp
	}
	switch operation {
	case "add":
		operation = "create"
	case "edit":
		operation = "update"
	case "remove":
		operation = "delete"
	case "check":
		operation = "run"
	case "pause":
		operation = "cancel"
	}
	switch operation {
	case "create":
		if taskTargetsElsewhere(event, input, false) {
			return operation + "_elsewhere"
		}
	case "update", "run":
		if taskTargetsElsewhere(event, input, true) {
			return operation + "_elsewhere"
		}
		if item, ok := r.taskByID(configToolString(input, "id")); ok && r.taskDeliversOutsideConversation(item, event) {
			return operation + "_elsewhere"
		}
	}
	return operation
}

func taskTargetsElsewhere(event MessageEvent, input map[string]any, anyConversation bool) bool {
	raw := stripAccountIDMarkup(configToolString(input, "target_user_id"))
	if raw == "" {
		return false
	}
	// 按账号原文比，非数字账号（飞书 ou_xxx 等）也比得出来，见 sameAccountID。
	if sameAccountID(raw, event.UserID) {
		return false
	}
	return anyConversation || strings.TrimSpace(event.GroupID) == ""
}

// taskByID 按 id 取任务记录，不做任何归属判断。
func (r *Runtime) taskByID(id string) (Reminder, bool) {
	id = strings.TrimSpace(id)
	if r == nil || r.reminders == nil || id == "" {
		return Reminder{}, false
	}
	r.reminderMu.Lock()
	items := r.reminders.Reminders()
	r.reminderMu.Unlock()
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return Reminder{}, false
}

// sameBotAsEvent 判断记录是不是这条消息所属机器人的。ID 相同直接算；否则按
// sameProfileAsEvent 认旧号和单机器人时的空 ID。
//
// 没有机器人 ID 的是多机器人之前的旧记录，分不出归谁，按这台机器人的算：不然多机器人
// 部署里这些旧任务在对话里谁都改不了、删不掉。
func (r *Runtime) sameBotAsEvent(profileID string, event MessageEvent) bool {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" || profileID == strings.TrimSpace(event.ProfileID) {
		return true
	}
	return r.sameProfileAsEvent(profileID, event)
}

// taskOfOtherBot 报告这个 id 是不是别的机器人名下的任务。改、取消、删除、立即执行都
// 先过这一关，不管什么模式：几台机器人共用一个 Runtime 时，任务按归属人匹配会让 A 的
// 主人按 id 改到 B 的任务（两台机器人主人是同一个号时尤其如此）。
func (r *Runtime) taskOfOtherBot(id string, event MessageEvent) bool {
	item, ok := r.taskByID(id)
	return ok && !r.sameBotAsEvent(item.ProfileID, event)
}

// taskDeliversOutsideConversation 报告一条已有任务是不是投递到当前会话以外：别的机器人、
// 别的群、别人的私聊，或者 WebUI 配的投递目标里有一个不是当前会话。
func (r *Runtime) taskDeliversOutsideConversation(item Reminder, event MessageEvent) bool {
	if !r.sameBotAsEvent(item.ProfileID, event) {
		return true
	}
	for _, target := range decodeReminderDeliveryTargets(item.NotificationTargetsJSON) {
		if profile := strings.TrimSpace(target.ProfileID); profile != "" && !r.sameBotAsEvent(profile, event) {
			return true
		}
		if !deliveryTargetIsConversation(target.GroupID, target.UserID, event) {
			return true
		}
	}
	return !deliveryTargetIsConversation(item.GroupID, item.UserID, event)
}

func deliveryTargetIsConversation(groupID, userID string, event MessageEvent) bool {
	if group := strings.TrimSpace(groupID); group != "" {
		return group == strings.TrimSpace(event.GroupID)
	}
	return strings.TrimSpace(event.GroupID) == "" && sameAccountID(userID, event.UserID)
}

func taskTargetUserID(ctx context.Context, runtime *Runtime, event MessageEvent, input map[string]any) (string, error) {
	raw := strings.TrimSpace(configToolString(input, "target_user_id"))
	if raw == "" {
		return strings.TrimSpace(event.UserID), nil
	}
	targetID := normalizeRelationshipUserID(raw)
	if targetID == "" {
		return "", fmt.Errorf("target_user_id 必须是有效账号")
	}
	if targetID != strings.TrimSpace(event.UserID) && !runtime.relationshipPolicy(ctx, event).Owner {
		return "", fmt.Errorf("只有主人可以管理其他用户的提醒和订阅")
	}
	return targetID, nil
}

func taskForTool(item Reminder) dianaTask {
	kind := "reminder"
	status := reminderStatus(item)
	consumesQuota := item.LastRunAt.IsZero() && item.CancelledAt.IsZero()
	interval := ""
	trigger := ""
	if spec, ok := EventTriggerSpec(item); ok {
		kind = string(ReminderKindEventTrigger)
		status = eventTriggerStatus(item, spec)
		consumesQuota = eventTriggerArmed(item, spec, time.Now())
		trigger = eventTriggerSummary(item, spec)
	} else if reminderIsRecurring(item) {
		kind = "schedule"
		if reminderIsRepositoryWatch(item) {
			kind = "repository_watch"
			consumesQuota = false
		} else if reminderIsRSSWatch(item) {
			kind = "rss_watch"
			consumesQuota = !strings.HasPrefix(item.OwnerID, "webui") && item.CancelledAt.IsZero()
		} else {
			consumesQuota = item.CancelledAt.IsZero()
		}
		status = scheduleStatus(item)
		interval = reminderScheduleInterval(item).String()
	}
	return dianaTask{
		ID:                    item.ID,
		Kind:                  kind,
		OwnerID:               item.OwnerID,
		GroupID:               item.GroupID,
		UserID:                item.UserID,
		Message:               item.Message,
		Status:                status,
		TriggerAt:             item.TriggerAt,
		Interval:              interval,
		Trigger:               trigger,
		LastRunAt:             item.LastRunAt,
		CancelledAt:           item.CancelledAt,
		LastError:             item.LastError,
		ConsecutiveFailures:   item.ConsecutiveFailures,
		PendingDelivery:       strings.TrimSpace(item.PendingDelivery) != "",
		Repository:            item.Repository,
		RepositoryBranch:      item.RepositoryBranch,
		WatchCommits:          item.WatchCommits,
		WatchPullRequests:     item.WatchPullRequests,
		PullRequestEvents:     EffectiveRepositoryWatchPullRequestEvents(item.WatchPullRequestEvents),
		IssueEvents:           EffectiveRepositoryWatchIssueEvents(item.WatchIssueEvents),
		WatchIssues:           item.WatchIssues,
		WatchReleases:         item.WatchReleases,
		ReleaseKinds:          EffectiveRepositoryWatchReleaseKinds(item.WatchReleaseKinds),
		WatchStars:            item.WatchStars,
		StarNotifyMode:        item.StarNotifyMode,
		StarNotifyThreshold:   item.StarNotifyThreshold,
		StarNotifyMilestones:  append([]int(nil), item.StarNotifyMilestones...),
		LastCommitSHA:         item.LastCommitSHA,
		LastPullRequestCursor: item.LastPullRequestCursor,
		LastReleaseTag:        item.LastReleaseTag,
		LastStarCount:         item.LastStarCount,
		LastNotifiedStarCount: item.LastNotifiedStarCount,
		FeedSources:           rssWatchSourceLabels(item),
		FeedURL:               secretmask.URLs(item.FeedURL),
		FeedSource:            item.FeedSource,
		FeedHandle:            item.FeedHandle,
		FeedJudgePrompt:       item.FeedJudgePrompt,
		LastFeedItemID:        item.LastFeedItemID,
		CreatedAt:             item.CreatedAt,
		ConsumesQuota:         consumesQuota,
	}
}
