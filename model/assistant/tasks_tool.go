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
	ID                    string    `json:"id"`
	Kind                  string    `json:"kind"`
	OwnerID               string    `json:"owner_id"`
	GroupID               string    `json:"group_id,omitempty"`
	UserID                string    `json:"user_id,omitempty"`
	Message               string    `json:"message"`
	Status                string    `json:"status"`
	TriggerAt             time.Time `json:"trigger_at"`
	Interval              string    `json:"interval,omitempty"`
	LastRunAt             time.Time `json:"last_run_at,omitempty"`
	CancelledAt           time.Time `json:"cancelled_at,omitempty"`
	LastError             string    `json:"last_error,omitempty"`
	ConsecutiveFailures   int       `json:"consecutive_failures,omitempty"`
	PendingDelivery       bool      `json:"pending_delivery,omitempty"`
	Repository            string    `json:"repository,omitempty"`
	RepositoryBranch      string    `json:"repository_branch,omitempty"`
	WatchCommits          bool      `json:"watch_commits,omitempty"`
	WatchPullRequests     bool      `json:"watch_pull_requests,omitempty"`
	WatchReleases         bool      `json:"watch_releases,omitempty"`
	WatchStars            bool      `json:"watch_stars,omitempty"`
	LastCommitSHA         string    `json:"last_commit_sha,omitempty"`
	LastPullRequestCursor string    `json:"last_pull_request_cursor,omitempty"`
	LastReleaseTag        string    `json:"last_release_tag,omitempty"`
	LastStarCount         int       `json:"last_star_count,omitempty"`
	FeedURL               string    `json:"feed_url,omitempty"`
	FeedSource            string    `json:"feed_source,omitempty"`
	FeedHandle            string    `json:"feed_handle,omitempty"`
	FeedJudgePrompt       string    `json:"feed_judge_prompt,omitempty"`
	LastFeedItemID        string    `json:"last_feed_item_id,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	ConsumesQuota         bool      `json:"consumes_quota"`
}

func newDianaTasksTool(runtime *Runtime, event MessageEvent) *dianaTasksTool {
	return &dianaTasksTool{runtime: runtime, event: event}
}

func (t *dianaTasksTool) Name() string {
	return "diana.tasks"
}

func (t *dianaTasksTool) Description() string {
	return `一次查询持久化存储中的全部一次性提醒和周期订阅，包含运行中、已使用、已取消状态以及是否占用额度。用户询问“我的所有任务/提醒/订阅”“现在有哪些定时任务”时必须使用本工具，不要分别猜测。默认 scope=mine，只返回当前用户；只有主人可使用 scope=all 查询所有用户，或用 target_user_id 查询指定用户。input: {"operation":"list","scope":"mine|all，可选","target_user_id":"仅主人可选"}`
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
		return "", fmt.Errorf("target_user_id 必须是有效 QQ 号")
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
	items := make([]dianaTask, 0, len(stored))
	for _, item := range stored {
		if scope == "mine" && item.OwnerID != t.event.UserID {
			continue
		}
		if targetID != "" && item.OwnerID != targetID {
			continue
		}
		items = append(items, taskForTool(item))
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

func taskTargetUserID(ctx context.Context, runtime *Runtime, event MessageEvent, input map[string]any) (string, error) {
	raw := strings.TrimSpace(configToolString(input, "target_user_id"))
	if raw == "" {
		return strings.TrimSpace(event.UserID), nil
	}
	targetID := normalizeRelationshipUserID(raw)
	if targetID == "" {
		return "", fmt.Errorf("target_user_id 必须是有效 QQ 号")
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
	if reminderIsRecurring(item) {
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
		interval = (time.Duration(item.IntervalSeconds) * time.Second).String()
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
		LastRunAt:             item.LastRunAt,
		CancelledAt:           item.CancelledAt,
		LastError:             item.LastError,
		ConsecutiveFailures:   item.ConsecutiveFailures,
		PendingDelivery:       strings.TrimSpace(item.PendingDelivery) != "",
		Repository:            item.Repository,
		RepositoryBranch:      item.RepositoryBranch,
		WatchCommits:          item.WatchCommits,
		WatchPullRequests:     item.WatchPullRequests,
		WatchReleases:         item.WatchReleases,
		WatchStars:            item.WatchStars,
		LastCommitSHA:         item.LastCommitSHA,
		LastPullRequestCursor: item.LastPullRequestCursor,
		LastReleaseTag:        item.LastReleaseTag,
		LastStarCount:         item.LastStarCount,
		FeedURL:               item.FeedURL,
		FeedSource:            item.FeedSource,
		FeedHandle:            item.FeedHandle,
		FeedJudgePrompt:       item.FeedJudgePrompt,
		LastFeedItemID:        item.LastFeedItemID,
		CreatedAt:             item.CreatedAt,
		ConsumesQuota:         consumesQuota,
	}
}
