// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

type groupTestRecallPayload struct {
	MessageID string `json:"message_id"`
}

type groupTestRecallResponse struct {
	MessageID string         `json:"message_id"`
	Recalled  bool           `json:"recalled"`
	Result    map[string]any `json:"result,omitempty"`
}

type groupTestFilePayload struct {
	GroupID   string `json:"group_id"`
	FileID    string `json:"file_id"`
	BusID     string `json:"busid,omitempty"`
	Name      string `json:"name"`
	LocalPath string `json:"local_path,omitempty"`
}

type groupTestFileResponse struct {
	GroupID string `json:"group_id"`
	FileID  string `json:"file_id"`
	Name    string `json:"name"`
	Context string `json:"context"`
}

type groupTestUploadFilePayload struct {
	GroupID string `json:"group_id"`
	File    string `json:"file"`
	Name    string `json:"name"`
}

type groupTestOneBotPayload struct {
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
}

type botAutoInfoResponse struct {
	BotAccount    string             `json:"bot_account,omitempty"`
	Nickname      string             `json:"nickname,omitempty"`
	AvatarURL     string             `json:"avatar_url,omitempty"`
	Groups        []botAutoGroupInfo `json:"groups,omitempty"`
	RecentGroupID string             `json:"recent_group_id,omitempty"`
	RecentUserID  string             `json:"recent_user_id,omitempty"`
}

type botAutoGroupInfo struct {
	GroupID        string `json:"group_id"`
	GroupName      string `json:"group_name,omitempty"`
	MemberCount    int    `json:"member_count,omitempty"`
	MaxMemberCount int    `json:"max_member_count,omitempty"`
}

type botTasksResponse struct {
	Items []botTaskPayload `json:"items"`
}

type repositoryWatchCreatePayload struct {
	Repository           string                         `json:"repository"`
	Branch               string                         `json:"branch,omitempty"`
	IntervalSeconds      int64                          `json:"interval_seconds"`
	WatchCommits         bool                           `json:"watch_commits"`
	WatchPullRequests    bool                           `json:"watch_pull_requests"`
	WatchIssues          bool                           `json:"watch_issues"`
	WatchReleases        bool                           `json:"watch_releases"`
	WatchStars           bool                           `json:"watch_stars"`
	StarNotifyMode       string                         `json:"star_notify_mode,omitempty"`
	StarNotifyThreshold  int                            `json:"star_notify_threshold,omitempty"`
	StarNotifyMilestones []int                          `json:"star_notify_milestones,omitempty"`
	ProfileID            string                         `json:"profile_id"`
	Destination          string                         `json:"destination"`
	GroupID              string                         `json:"group_id,omitempty"`
	UserID               string                         `json:"user_id,omitempty"`
	NotificationEnabled  *bool                          `json:"notification_enabled,omitempty"`
	NotificationTargets  []repositoryWatchTargetPayload `json:"notification_targets,omitempty"`
}

type repositoryWatchTargetPayload struct {
	Destination string `json:"destination"`
	GroupID     string `json:"group_id,omitempty"`
	UserID      string `json:"user_id,omitempty"`
}

type repositoryWatchUpdatePayload struct {
	Repository           string                         `json:"repository,omitempty"`
	Branch               *string                        `json:"branch,omitempty"`
	IntervalSeconds      int64                          `json:"interval_seconds,omitempty"`
	WatchCommits         *bool                          `json:"watch_commits,omitempty"`
	WatchPullRequests    *bool                          `json:"watch_pull_requests,omitempty"`
	WatchIssues          *bool                          `json:"watch_issues,omitempty"`
	WatchReleases        *bool                          `json:"watch_releases,omitempty"`
	WatchStars           *bool                          `json:"watch_stars,omitempty"`
	StarNotifyMode       *string                        `json:"star_notify_mode,omitempty"`
	StarNotifyThreshold  *int                           `json:"star_notify_threshold,omitempty"`
	StarNotifyMilestones []int                          `json:"star_notify_milestones,omitempty"`
	ProfileID            string                         `json:"profile_id"`
	Destination          string                         `json:"destination"`
	GroupID              string                         `json:"group_id,omitempty"`
	UserID               string                         `json:"user_id,omitempty"`
	NotificationEnabled  *bool                          `json:"notification_enabled,omitempty"`
	NotificationTargets  []repositoryWatchTargetPayload `json:"notification_targets,omitempty"`
}

type rssWatchCreatePayload struct {
	FeedURL         string `json:"feed_url,omitempty"`
	TwitterHandle   string `json:"twitter_handle,omitempty"`
	JudgePrompt     string `json:"judge_prompt"`
	IntervalSeconds int64  `json:"interval_seconds"`
	ProfileID       string `json:"profile_id"`
	Destination     string `json:"destination"`
	GroupID         string `json:"group_id,omitempty"`
	UserID          string `json:"user_id,omitempty"`
}

type rssWatchUpdatePayload struct {
	FeedURL         *string `json:"feed_url,omitempty"`
	TwitterHandle   *string `json:"twitter_handle,omitempty"`
	JudgePrompt     *string `json:"judge_prompt,omitempty"`
	IntervalSeconds int64   `json:"interval_seconds,omitempty"`
}

type botTaskPayload struct {
	ID                    string                             `json:"id"`
	Kind                  string                             `json:"kind"`
	Platform              string                             `json:"platform,omitempty"`
	ProfileID             string                             `json:"profile_id,omitempty"`
	OwnerID               string                             `json:"owner_id"`
	GroupID               string                             `json:"group_id,omitempty"`
	UserID                string                             `json:"user_id,omitempty"`
	Message               string                             `json:"message"`
	Status                string                             `json:"status"`
	TriggerAt             time.Time                          `json:"trigger_at"`
	IntervalSeconds       int64                              `json:"interval_seconds,omitempty"`
	LastRunAt             time.Time                          `json:"last_run_at,omitempty"`
	CancelledAt           time.Time                          `json:"cancelled_at,omitempty"`
	LastError             string                             `json:"last_error,omitempty"`
	ConsecutiveFailures   int                                `json:"consecutive_failures,omitempty"`
	PendingDelivery       bool                               `json:"pending_delivery,omitempty"`
	PendingSince          time.Time                          `json:"pending_since,omitempty"`
	Repository            string                             `json:"repository,omitempty"`
	RepositoryBranch      string                             `json:"repository_branch,omitempty"`
	WatchCommits          bool                               `json:"watch_commits,omitempty"`
	WatchPullRequests     bool                               `json:"watch_pull_requests,omitempty"`
	WatchIssues           bool                               `json:"watch_issues,omitempty"`
	WatchReleases         bool                               `json:"watch_releases,omitempty"`
	WatchStars            bool                               `json:"watch_stars,omitempty"`
	StarNotifyMode        string                             `json:"star_notify_mode,omitempty"`
	StarNotifyThreshold   int                                `json:"star_notify_threshold,omitempty"`
	StarNotifyMilestones  []int                              `json:"star_notify_milestones,omitempty"`
	LastCommitSHA         string                             `json:"last_commit_sha,omitempty"`
	LastPullRequestCursor string                             `json:"last_pull_request_cursor,omitempty"`
	LastIssueCursor       string                             `json:"last_issue_cursor,omitempty"`
	LastReleaseTag        string                             `json:"last_release_tag,omitempty"`
	LastStarCount         int                                `json:"last_star_count,omitempty"`
	LastNotifiedStarCount int                                `json:"last_notified_star_count,omitempty"`
	FeedURL               string                             `json:"feed_url,omitempty"`
	FeedSource            string                             `json:"feed_source,omitempty"`
	FeedHandle            string                             `json:"feed_handle,omitempty"`
	FeedJudgePrompt       string                             `json:"feed_judge_prompt,omitempty"`
	LastFeedItemID        string                             `json:"last_feed_item_id,omitempty"`
	LastFeedPublishedAt   time.Time                          `json:"last_feed_published_at,omitempty"`
	CreatedAt             time.Time                          `json:"created_at"`
	ConsumesQuota         bool                               `json:"consumes_quota"`
	NotificationEnabled   bool                               `json:"notification_enabled,omitempty"`
	NotificationTargets   []assistant.ReminderDeliveryTarget `json:"notification_targets,omitempty"`
}

type pluginTaskRunner interface {
	RunPluginTask(context.Context, assistant.PluginTask) (assistant.PluginTaskResult, error)
}

var groupTestOneBotReadActions = map[string]struct{}{
	"get_version_info":      {},
	"get_login_info":        {},
	"get_stranger_info":     {},
	"get_group_list":        {},
	"get_group_member_info": {},
	"get_group_member_list": {},
	"get_group_msg_history": {},
	"get_forward_msg":       {},
	"get_group_file_url":    {},
	"get_file":              {},
	"get_image":             {},
	"get_msg":               {},
}

func (h *BotHandler) dashboardStats(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusOK, storage.DashboardStats{Server: collectDashboardServerStats(time.Now())})
		return
	}
	stats, err := h.sqlite.DashboardStatsForDay(c.Request.Context(), time.Now())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.dashboard_stats", err, "dashboard", nil)
		return
	}
	stats.Server = collectDashboardServerStats(time.Now())
	c.JSON(http.StatusOK, stats)
}

func (h *BotHandler) shareNapCatQRCode(c *gin.Context) {
	if h.localMedia == nil {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.group_test.napcat_qrcode", fmt.Errorf("local media store is unavailable"), "napcat-qrcode", nil)
		return
	}
	path := strings.TrimSpace(os.Getenv("DIANA_NAPCAT_QRCODE_PATH"))
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			h.writeError(c, http.StatusInternalServerError, "assistant.group_test.napcat_qrcode", err, "napcat-qrcode", nil)
			return
		}
		path = filepath.Join(home, "Library", "Containers", "com.tencent.qq", "Data", "Library", "Application Support", "QQ", "NapCat", "cache", "qrcode.png")
	}
	sharedURL, ok := h.localMedia.Share(path, 2*time.Minute)
	if !ok {
		h.writeError(c, http.StatusNotFound, "assistant.group_test.napcat_qrcode", fmt.Errorf("NapCat login QR code is unavailable"), "napcat-qrcode", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": sharedURL, "expires_in_seconds": 120})
}

func (h *BotHandler) listGroupTestFiles(c *gin.Context) {
	groupID := strings.TrimSpace(c.Query("group_id"))
	parsedGroupID, err := strconv.ParseInt(groupID, 10, 64)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.files", fmt.Errorf("valid group_id is required"), groupID, nil)
		return
	}
	result, err := h.runtime.CallOneBotAPI(c.Request.Context(), "get_group_root_files", map[string]any{"group_id": parsedGroupID})
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.files", err, groupID, map[string]any{"group_id": groupID})
		return
	}
	c.JSON(http.StatusOK, gin.H{"group_id": groupID, "result": result})
}

func (h *BotHandler) uploadGroupTestFile(c *gin.Context) {
	var payload groupTestUploadFilePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.upload_file", err, "", nil)
		return
	}
	groupID := strings.TrimSpace(payload.GroupID)
	file := strings.TrimSpace(payload.File)
	name := strings.TrimSpace(payload.Name)
	parsedGroupID, err := strconv.ParseInt(groupID, 10, 64)
	if err != nil || file == "" || name == "" {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.upload_file", fmt.Errorf("valid group_id, file and name are required"), groupID, nil)
		return
	}
	uploadSource := file
	if h.localMedia != nil {
		if sharedURL, ok := h.localMedia.Share(file, 10*time.Minute); ok {
			uploadSource = sharedURL
		}
	}
	result, err := h.runtime.CallOneBotAPI(c.Request.Context(), "upload_group_file", map[string]any{
		"group_id": parsedGroupID,
		"file":     uploadSource,
		"name":     name,
	})
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.upload_file", err, groupID, map[string]any{"group_id": groupID, "name": name})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.group_test.upload_file", "群测试文件已上传", groupID, map[string]any{"group_id": groupID, "name": name})
	c.JSON(http.StatusOK, gin.H{"group_id": groupID, "name": name, "result": result})
}

func (h *BotHandler) autoInfo(c *gin.Context) {
	status := h.runtime.Status()
	info := botAutoInfoResponse{
		BotAccount: strings.TrimSpace(status.Channel.SelfID),
		AvatarURL:  assistant.OneBotMemberAvatarURL(status.Channel.SelfID),
	}
	if data, err := h.runtime.CallOneBotAPI(c.Request.Context(), "get_login_info", map[string]any{}); err == nil {
		if userID := firstNonEmptyWebUI(stringFromAnyWebUI(data["user_id"]), stringFromAnyWebUI(data["self_id"])); userID != "" {
			info.BotAccount = userID
			info.AvatarURL = assistant.OneBotMemberAvatarURL(userID)
		}
		info.Nickname = firstNonEmptyWebUI(stringFromAnyWebUI(data["nickname"]), stringFromAnyWebUI(data["user_name"]), stringFromAnyWebUI(data["name"]))
	}
	if info.BotAccount != "" && info.Nickname == "" {
		if data, err := h.runtime.CallOneBotAPI(c.Request.Context(), "get_stranger_info", map[string]any{"user_id": oneBotIDParam(info.BotAccount), "no_cache": true}); err == nil {
			info.Nickname = firstNonEmptyWebUI(stringFromAnyWebUI(data["nickname"]), stringFromAnyWebUI(data["user_name"]), stringFromAnyWebUI(data["name"]))
		}
	}
	if data, err := h.runtime.CallOneBotAPI(c.Request.Context(), "get_group_list", map[string]any{"no_cache": true}); err == nil {
		info.Groups = autoGroupsFromOneBotData(data)
	}
	for _, event := range status.RecentEvents {
		if info.RecentGroupID == "" && strings.TrimSpace(event.GroupID) != "" {
			info.RecentGroupID = strings.TrimSpace(event.GroupID)
		}
		if info.RecentUserID == "" && strings.TrimSpace(event.UserID) != "" && strings.TrimSpace(event.UserID) != info.BotAccount {
			info.RecentUserID = strings.TrimSpace(event.UserID)
		}
		if info.RecentGroupID != "" && info.RecentUserID != "" {
			break
		}
	}
	c.JSON(http.StatusOK, info)
}

func autoGroupsFromOneBotData(data map[string]any) []botAutoGroupInfo {
	for _, key := range []string{"items", "list", "groups"} {
		if groups := autoGroupsFromAny(data[key]); len(groups) > 0 {
			return groups
		}
	}
	if groups := autoGroupsFromAny(data["data"]); len(groups) > 0 {
		return groups
	}
	return autoGroupsFromAny(data)
}

func autoGroupsFromAny(value any) []botAutoGroupInfo {
	switch typed := value.(type) {
	case []any:
		groups := make([]botAutoGroupInfo, 0, len(typed))
		for _, item := range typed {
			if group := autoGroupFromAny(item); group.GroupID != "" {
				groups = append(groups, group)
			}
		}
		return groups
	case []map[string]any:
		groups := make([]botAutoGroupInfo, 0, len(typed))
		for _, item := range typed {
			if group := autoGroupFromMap(item); group.GroupID != "" {
				groups = append(groups, group)
			}
		}
		return groups
	case map[string]any:
		if group := autoGroupFromMap(typed); group.GroupID != "" {
			return []botAutoGroupInfo{group}
		}
	}
	return nil
}

func autoGroupFromAny(value any) botAutoGroupInfo {
	if item, ok := value.(map[string]any); ok {
		return autoGroupFromMap(item)
	}
	return botAutoGroupInfo{}
}

func autoGroupFromMap(item map[string]any) botAutoGroupInfo {
	return botAutoGroupInfo{
		GroupID:        firstNonEmptyWebUI(stringFromAnyWebUI(item["group_id"]), stringFromAnyWebUI(item["id"])),
		GroupName:      firstNonEmptyWebUI(stringFromAnyWebUI(item["group_name"]), stringFromAnyWebUI(item["name"])),
		MemberCount:    intFromAnyWebUI(item["member_count"]),
		MaxMemberCount: intFromAnyWebUI(item["max_member_count"]),
	}
}

func (h *BotHandler) callGroupTestOneBot(c *gin.Context) {
	var payload groupTestOneBotPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.onebot", err, "", nil)
		return
	}
	action := strings.TrimSpace(payload.Action)
	if _, ok := groupTestOneBotReadActions[action]; !ok {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.onebot", fmt.Errorf("OneBot action %q is not allowed", action), action, nil)
		return
	}
	if payload.Params == nil {
		payload.Params = map[string]any{}
	}
	result, err := h.runtime.CallOneBotAPI(c.Request.Context(), action, payload.Params)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.onebot", err, action, nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"action": action, "result": result})
}

func (h *BotHandler) listTasks(c *gin.Context) {
	if h.sqlite == nil {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.tasks.list", fmt.Errorf("task store is unavailable"), "", nil)
		return
	}
	items, _, err := h.sqlite.LoadReminders(c.Request.Context())
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.tasks.list", err, "", nil)
		return
	}
	out := make([]botTaskPayload, 0, len(items))
	for _, item := range items {
		out = append(out, botTaskPayload{
			ID:                    item.ID,
			Kind:                  botTaskKind(item),
			Platform:              item.Platform,
			ProfileID:             item.ProfileID,
			OwnerID:               item.OwnerID,
			GroupID:               item.GroupID,
			UserID:                item.UserID,
			Message:               item.Message,
			Status:                botTaskStatus(item),
			TriggerAt:             item.TriggerAt,
			IntervalSeconds:       item.IntervalSeconds,
			LastRunAt:             item.LastRunAt,
			CancelledAt:           item.CancelledAt,
			LastError:             item.LastError,
			ConsecutiveFailures:   item.ConsecutiveFailures,
			PendingDelivery:       strings.TrimSpace(item.PendingDelivery) != "",
			PendingSince:          item.PendingSince,
			Repository:            item.Repository,
			RepositoryBranch:      item.RepositoryBranch,
			WatchCommits:          item.WatchCommits,
			WatchPullRequests:     item.WatchPullRequests,
			WatchIssues:           item.WatchIssues,
			WatchReleases:         item.WatchReleases,
			WatchStars:            item.WatchStars,
			LastCommitSHA:         item.LastCommitSHA,
			LastPullRequestCursor: item.LastPullRequestCursor,
			LastIssueCursor:       item.LastIssueCursor,
			LastReleaseTag:        item.LastReleaseTag,
			LastStarCount:         item.LastStarCount,
			FeedURL:               item.FeedURL,
			FeedSource:            item.FeedSource,
			FeedHandle:            item.FeedHandle,
			FeedJudgePrompt:       item.FeedJudgePrompt,
			LastFeedItemID:        item.LastFeedItemID,
			LastFeedPublishedAt:   item.LastFeedPublishedAt,
			CreatedAt:             item.CreatedAt,
			ConsumesQuota:         taskConsumesQuota(item),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	c.JSON(http.StatusOK, botTasksResponse{Items: out})
}

func (h *BotHandler) createRepositoryWatch(c *gin.Context) {
	manager, ok := h.runtime.(repositoryWatchRuntime)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.repository_watch.create", fmt.Errorf("repository watch runtime is unavailable"), "", nil)
		return
	}
	var payload repositoryWatchCreatePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.create", err, "", nil)
		return
	}
	profile, set, err := h.repositoryWatchProfile(payload.ProfileID)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.create", err, payload.Repository, nil)
		return
	}
	destination := strings.ToLower(strings.TrimSpace(payload.Destination))
	if destination == "" {
		destination = "private"
	}
	if destination != "private" && destination != "group" {
		h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.create", fmt.Errorf("destination 必须是 private 或 group"), payload.Repository, nil)
		return
	}
	targets := repositoryWatchTargetsFromPayload(payload.NotificationTargets, profile, set)
	notificationEnabled := payload.NotificationEnabled == nil || *payload.NotificationEnabled
	if len(targets) == 0 && (notificationEnabled || strings.TrimSpace(payload.GroupID) != "" || strings.TrimSpace(payload.UserID) != "") {
		groupID, userID := "", ""
		if destination == "group" {
			groupID = strings.TrimSpace(payload.GroupID)
		} else {
			userID = strings.TrimSpace(payload.UserID)
		}
		if groupID == "" && userID == "" {
			h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.create", fmt.Errorf("启用通知时至少填写一个群聊或私聊发送对象"), payload.Repository, nil)
			return
		}
		targets = []assistant.ReminderDeliveryTarget{{Platform: profile.Platform, ProfileID: profile.ID, ContextNamespace: repositoryWatchContextNamespace(set, profile.ID), GroupID: groupID, UserID: userID}}
	}
	groupID, userID := "", ""
	if len(targets) > 0 {
		groupID, userID = targets[0].GroupID, targets[0].UserID
	}
	interval := time.Duration(payload.IntervalSeconds) * time.Second
	item, err := manager.CreateRepositoryWatch(c.Request.Context(), assistant.RepositoryWatchCreateInput{
		Repository: payload.Repository, Branch: payload.Branch, Interval: interval,
		WatchCommits: payload.WatchCommits, WatchPullRequests: payload.WatchPullRequests,
		WatchIssues: payload.WatchIssues, WatchReleases: payload.WatchReleases, WatchStars: payload.WatchStars,
		StarNotifyMode: payload.StarNotifyMode, StarNotifyThreshold: payload.StarNotifyThreshold, StarNotifyMilestones: payload.StarNotifyMilestones,
		Platform: profile.Platform, ProfileID: profile.ID, OwnerID: "webui:" + strings.TrimSpace(profile.ID), UserID: userID, GroupID: groupID,
		ContextNamespace:    repositoryWatchContextNamespace(set, profile.ID),
		NotificationEnabled: notificationEnabled, NotificationTargets: targets,
	})
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.create", err, payload.Repository, nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.repository_watch.create", "仓库更新订阅已创建", item.ID, map[string]any{"repository": item.Repository, "profile_id": item.ProfileID, "group_id": item.GroupID})
	c.JSON(http.StatusCreated, botTaskFromReminder(item))
}

func (h *BotHandler) updateRepositoryWatch(c *gin.Context) {
	manager, ok := h.runtime.(repositoryWatchRuntime)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.repository_watch.update", fmt.Errorf("repository watch runtime is unavailable"), c.Param("id"), nil)
		return
	}
	var payload repositoryWatchUpdatePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.update", err, c.Param("id"), nil)
		return
	}
	ownerID, err := h.repositoryWatchOwner(c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "assistant.repository_watch.update", err, c.Param("id"), nil)
		return
	}
	destination := strings.ToLower(strings.TrimSpace(payload.Destination))
	deliveryRequested := strings.TrimSpace(payload.ProfileID) != "" || destination != "" || strings.TrimSpace(payload.GroupID) != "" || strings.TrimSpace(payload.UserID) != "" || payload.NotificationEnabled != nil || payload.NotificationTargets != nil
	updateInput := assistant.RepositoryWatchUpdateInput{
		Repository: payload.Repository, Interval: time.Duration(payload.IntervalSeconds) * time.Second,
		WatchCommits: payload.WatchCommits, WatchPullRequests: payload.WatchPullRequests,
		WatchIssues: payload.WatchIssues, WatchReleases: payload.WatchReleases, WatchStars: payload.WatchStars,
		StarNotifyMode: payload.StarNotifyMode, StarNotifyThreshold: payload.StarNotifyThreshold, StarNotifyMilestones: payload.StarNotifyMilestones,
	}
	if deliveryRequested {
		profile, set, profileErr := h.repositoryWatchProfile(payload.ProfileID)
		if profileErr != nil {
			h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.update", profileErr, c.Param("id"), nil)
			return
		}
		targets := repositoryWatchTargetsFromPayload(payload.NotificationTargets, profile, set)
		if len(targets) == 0 && payload.NotificationEnabled != nil && *payload.NotificationEnabled {
			h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.update", fmt.Errorf("启用通知时至少填写一个群聊或私聊对象"), c.Param("id"), nil)
			return
		}
		if len(targets) > 0 {
			destination = "targets"
		}
		if payload.NotificationEnabled != nil && !*payload.NotificationEnabled && len(targets) == 0 {
			destination = "none"
		}
		if destination != "private" && destination != "group" && destination != "targets" && destination != "none" {
			h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.update", fmt.Errorf("destination 必须是 private 或 group"), c.Param("id"), nil)
			return
		}
		groupID, userID := "", ""
		if destination == "none" {
			// Notification is intentionally disabled; retain no primary target.
		} else if destination == "targets" {
			groupID, userID = targets[0].GroupID, targets[0].UserID
		} else if destination == "group" {
			groupID = strings.TrimSpace(payload.GroupID)
			if groupID == "" {
				h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.update", fmt.Errorf("群聊通知必须填写群号或 Chat ID"), c.Param("id"), nil)
				return
			}
		} else {
			userID = strings.TrimSpace(payload.UserID)
			if userID == "" {
				h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.update", fmt.Errorf("私聊通知必须填写发送对象 ID"), c.Param("id"), nil)
				return
			}
		}
		updateInput.Delivery = true
		updateInput.Platform = profile.Platform
		updateInput.ProfileID = profile.ID
		updateInput.ContextNamespace = repositoryWatchContextNamespace(set, profile.ID)
		updateInput.OwnerID = "webui:" + strings.TrimSpace(profile.ID)
		updateInput.GroupID = groupID
		updateInput.UserID = userID
		updateInput.NotificationEnabled = payload.NotificationEnabled
		updateInput.NotificationTargets = targets
	}
	var branch *string
	if payload.Branch != nil {
		value := strings.TrimSpace(*payload.Branch)
		branch = &value
	}
	updateInput.Branch = branch
	item, err := manager.UpdateRepositoryWatch(c.Request.Context(), ownerID, c.Param("id"), updateInput)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.update", err, c.Param("id"), nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.repository_watch.update", "仓库更新订阅已更新", item.ID, map[string]any{"repository": item.Repository})
	c.JSON(http.StatusOK, botTaskFromReminder(item))
}

func (h *BotHandler) cancelRepositoryWatch(c *gin.Context) {
	manager, ok := h.runtime.(repositoryWatchRuntime)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.repository_watch.cancel", fmt.Errorf("repository watch runtime is unavailable"), c.Param("id"), nil)
		return
	}
	ownerID, err := h.repositoryWatchOwner(c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "assistant.repository_watch.cancel", err, c.Param("id"), nil)
		return
	}
	item, err := manager.CancelRepositoryWatch(ownerID, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.cancel", err, c.Param("id"), nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.repository_watch.cancel", "仓库更新订阅已取消", item.ID, nil)
	c.JSON(http.StatusOK, botTaskFromReminder(item))
}

func (h *BotHandler) runRepositoryWatch(c *gin.Context) {
	manager, ok := h.runtime.(repositoryWatchRuntime)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.repository_watch.run", fmt.Errorf("repository watch runtime is unavailable"), c.Param("id"), nil)
		return
	}
	ownerID, err := h.repositoryWatchOwner(c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "assistant.repository_watch.run", err, c.Param("id"), nil)
		return
	}
	item, err := manager.RunRepositoryWatchNow(ownerID, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.repository_watch.run", err, c.Param("id"), nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.repository_watch.run", "仓库更新订阅已安排立即检查", item.ID, map[string]any{"repository": item.Repository})
	c.JSON(http.StatusOK, botTaskFromReminder(item))
}

func (h *BotHandler) deleteRepositoryWatch(c *gin.Context) {
	manager, ok := h.runtime.(repositoryWatchRuntime)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.repository_watch.delete", fmt.Errorf("repository watch runtime is unavailable"), c.Param("id"), nil)
		return
	}
	ownerID, err := h.repositoryWatchOwner(c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusNotFound, "assistant.repository_watch.delete", err, c.Param("id"), nil)
		return
	}
	removed, err := manager.DeleteRepositoryWatch(ownerID, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.repository_watch.delete", err, c.Param("id"), nil)
		return
	}
	if !removed {
		h.writeError(c, http.StatusNotFound, "assistant.repository_watch.delete", fmt.Errorf("仓库更新订阅不存在"), c.Param("id"), nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.repository_watch.delete", "仓库更新订阅已删除", c.Param("id"), nil)
	c.Status(http.StatusNoContent)
}

func (h *BotHandler) createRSSWatch(c *gin.Context) {
	manager, ok := h.runtime.(rssWatchRuntime)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.rss_watch.create", fmt.Errorf("rss watch runtime is unavailable"), "", nil)
		return
	}
	var payload rssWatchCreatePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.rss_watch.create", err, "", nil)
		return
	}
	profile, set, err := h.repositoryWatchProfile(payload.ProfileID)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.rss_watch.create", err, payload.FeedURL, nil)
		return
	}
	destination := strings.ToLower(strings.TrimSpace(payload.Destination))
	if destination == "" {
		destination = "private"
	}
	if destination != "private" && destination != "group" {
		h.writeError(c, http.StatusBadRequest, "assistant.rss_watch.create", fmt.Errorf("destination 必须是 private 或 group"), payload.FeedURL, nil)
		return
	}
	groupID, userID := "", ""
	if destination == "group" {
		groupID = strings.TrimSpace(payload.GroupID)
		if groupID == "" {
			h.writeError(c, http.StatusBadRequest, "assistant.rss_watch.create", fmt.Errorf("群聊通知必须填写群号或 Chat ID"), payload.FeedURL, nil)
			return
		}
	} else {
		userID = strings.TrimSpace(payload.UserID)
		if userID == "" {
			h.writeError(c, http.StatusBadRequest, "assistant.rss_watch.create", fmt.Errorf("私聊通知必须填写发送对象 ID"), payload.FeedURL, nil)
			return
		}
	}
	item, err := manager.CreateRSSWatch(c.Request.Context(), assistant.RSSWatchCreateInput{
		FeedURL: payload.FeedURL, TwitterHandle: payload.TwitterHandle, JudgePrompt: payload.JudgePrompt,
		Interval: time.Duration(payload.IntervalSeconds) * time.Second, Platform: profile.Platform, ProfileID: profile.ID,
		OwnerID: "webui:" + strings.TrimSpace(profile.ID), GroupID: groupID, UserID: userID,
		ContextNamespace: repositoryWatchContextNamespace(set, profile.ID),
	})
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.rss_watch.create", err, firstNonEmptyWeb(payload.TwitterHandle, payload.FeedURL), nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.rss_watch.create", "RSS 订阅已创建", item.ID, map[string]any{"feed_url": item.FeedURL, "profile_id": item.ProfileID})
	c.JSON(http.StatusCreated, botTaskFromReminder(item))
}

func (h *BotHandler) updateRSSWatch(c *gin.Context) {
	manager, ok := h.runtime.(rssWatchRuntime)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.rss_watch.update", fmt.Errorf("rss watch runtime is unavailable"), c.Param("id"), nil)
		return
	}
	var payload rssWatchUpdatePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.rss_watch.update", err, c.Param("id"), nil)
		return
	}
	ownerID, err := h.taskOwner(c.Param("id"), assistant.ReminderKindRSSWatch, "RSS 订阅")
	if err != nil {
		h.writeError(c, http.StatusNotFound, "assistant.rss_watch.update", err, c.Param("id"), nil)
		return
	}
	item, err := manager.UpdateRSSWatch(c.Request.Context(), ownerID, c.Param("id"), assistant.RSSWatchUpdateInput{
		FeedURL: payload.FeedURL, TwitterHandle: payload.TwitterHandle, JudgePrompt: payload.JudgePrompt,
		Interval: time.Duration(payload.IntervalSeconds) * time.Second,
	})
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.rss_watch.update", err, c.Param("id"), nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.rss_watch.update", "RSS 订阅已更新", item.ID, map[string]any{"feed_url": item.FeedURL})
	c.JSON(http.StatusOK, botTaskFromReminder(item))
}

func (h *BotHandler) cancelRSSWatch(c *gin.Context) {
	manager, ok := h.runtime.(rssWatchRuntime)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.rss_watch.cancel", fmt.Errorf("rss watch runtime is unavailable"), c.Param("id"), nil)
		return
	}
	ownerID, err := h.taskOwner(c.Param("id"), assistant.ReminderKindRSSWatch, "RSS 订阅")
	if err != nil {
		h.writeError(c, http.StatusNotFound, "assistant.rss_watch.cancel", err, c.Param("id"), nil)
		return
	}
	item, err := manager.CancelRSSWatch(ownerID, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.rss_watch.cancel", err, c.Param("id"), nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.rss_watch.cancel", "RSS 订阅已取消", item.ID, nil)
	c.JSON(http.StatusOK, botTaskFromReminder(item))
}

func (h *BotHandler) deleteRSSWatch(c *gin.Context) {
	manager, ok := h.runtime.(rssWatchRuntime)
	if !ok {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.rss_watch.delete", fmt.Errorf("rss watch runtime is unavailable"), c.Param("id"), nil)
		return
	}
	ownerID, err := h.taskOwner(c.Param("id"), assistant.ReminderKindRSSWatch, "RSS 订阅")
	if err != nil {
		h.writeError(c, http.StatusNotFound, "assistant.rss_watch.delete", err, c.Param("id"), nil)
		return
	}
	removed, err := manager.DeleteRSSWatch(ownerID, c.Param("id"))
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "assistant.rss_watch.delete", err, c.Param("id"), nil)
		return
	}
	if !removed {
		h.writeError(c, http.StatusNotFound, "assistant.rss_watch.delete", fmt.Errorf("RSS 订阅不存在"), c.Param("id"), nil)
		return
	}
	recordRequestOperation(c, h.logs, "assistant.rss_watch.delete", "RSS 订阅已删除", c.Param("id"), nil)
	c.Status(http.StatusNoContent)
}

func firstNonEmptyWeb(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (h *BotHandler) repositoryWatchProfile(profileID string) (assistant.BotConfig, assistant.ProfileSet, error) {
	set := h.profiles.Profiles().WithDefaults()
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		profileID = set.ActiveID
	}
	for _, profile := range set.Profiles {
		if profile.ID == profileID {
			return profile.WithDefaults(), set, nil
		}
	}
	return assistant.BotConfig{}, set, fmt.Errorf("机器人配置 %s 不存在", profileID)
}

func repositoryWatchContextNamespace(set assistant.ProfileSet, profileID string) string {
	if set.PlatformContextsIsolated() {
		return strings.TrimSpace(profileID)
	}
	return ""
}

func (h *BotHandler) repositoryWatchOwner(id string) (string, error) {
	return h.taskOwner(id, assistant.ReminderKindRepositoryWatch, "仓库更新订阅")
}

func (h *BotHandler) taskOwner(id string, kind assistant.ReminderKind, label string) (string, error) {
	if h.sqlite == nil {
		return "", fmt.Errorf("task store is unavailable")
	}
	items, _, err := h.sqlite.LoadReminders(h.ctx)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if item.ID == strings.TrimSpace(id) && item.Kind == kind {
			return item.OwnerID, nil
		}
	}
	return "", fmt.Errorf("%s %s 不存在", label, id)
}

func botTaskFromReminder(item assistant.Reminder) botTaskPayload {
	return botTaskPayload{
		ID: item.ID, Kind: botTaskKind(item), Platform: item.Platform, ProfileID: item.ProfileID,
		OwnerID: item.OwnerID, GroupID: item.GroupID, UserID: item.UserID, Message: item.Message,
		Status: botTaskStatus(item), TriggerAt: item.TriggerAt, IntervalSeconds: item.IntervalSeconds,
		LastRunAt: item.LastRunAt, CancelledAt: item.CancelledAt, LastError: item.LastError,
		ConsecutiveFailures: item.ConsecutiveFailures, PendingDelivery: strings.TrimSpace(item.PendingDelivery) != "",
		PendingSince: item.PendingSince, Repository: item.Repository, RepositoryBranch: item.RepositoryBranch,
		WatchCommits: item.WatchCommits, WatchPullRequests: item.WatchPullRequests,
		WatchReleases: item.WatchReleases, WatchStars: item.WatchStars,
		StarNotifyMode: item.StarNotifyMode, StarNotifyThreshold: item.StarNotifyThreshold, StarNotifyMilestones: append([]int(nil), item.StarNotifyMilestones...),
		LastCommitSHA: item.LastCommitSHA, LastPullRequestCursor: item.LastPullRequestCursor,
		LastReleaseTag: item.LastReleaseTag, LastStarCount: item.LastStarCount, LastNotifiedStarCount: item.LastNotifiedStarCount,
		CreatedAt: item.CreatedAt, ConsumesQuota: taskConsumesQuota(item),
		FeedURL: item.FeedURL, FeedSource: item.FeedSource, FeedHandle: item.FeedHandle,
		FeedJudgePrompt: item.FeedJudgePrompt, LastFeedItemID: item.LastFeedItemID, LastFeedPublishedAt: item.LastFeedPublishedAt,
		NotificationEnabled: item.NotificationEnabled, NotificationTargets: reminderDeliveryTargetsForWeb(item),
	}
}

func reminderDeliveryTargetsForWeb(item assistant.Reminder) []assistant.ReminderDeliveryTarget {
	return assistant.ReminderDeliveryTargets(item.NotificationTargetsJSON)
}

func repositoryWatchTargetsFromPayload(values []repositoryWatchTargetPayload, profile assistant.BotConfig, set assistant.ProfileSet) []assistant.ReminderDeliveryTarget {
	targets := make([]assistant.ReminderDeliveryTarget, 0, len(values))
	for _, value := range values {
		destination := strings.ToLower(strings.TrimSpace(value.Destination))
		target := assistant.ReminderDeliveryTarget{Platform: profile.Platform, ProfileID: profile.ID, ContextNamespace: repositoryWatchContextNamespace(set, profile.ID)}
		if destination == "group" {
			target.GroupID = strings.TrimSpace(value.GroupID)
		} else {
			target.UserID = strings.TrimSpace(value.UserID)
		}
		if target.GroupID != "" || target.UserID != "" {
			targets = append(targets, target)
		}
	}
	return targets
}

func botTaskKind(item assistant.Reminder) string {
	if item.Kind == assistant.ReminderKindRSSWatch && item.IntervalSeconds > 0 {
		return "rss_watch"
	}
	if item.Kind == assistant.ReminderKindRepositoryWatch && item.IntervalSeconds > 0 {
		return "repository_watch"
	}
	if item.Kind == assistant.ReminderKindQuery && item.IntervalSeconds > 0 {
		return "schedule"
	}
	return "reminder"
}

func botTaskStatus(item assistant.Reminder) string {
	if !item.CancelledAt.IsZero() {
		return "cancelled"
	}
	if item.ConsecutiveFailures > 0 {
		return "retrying"
	}
	if (item.Kind == assistant.ReminderKindQuery || item.Kind == assistant.ReminderKindRepositoryWatch || item.Kind == assistant.ReminderKindRSSWatch) && item.IntervalSeconds > 0 {
		return "active"
	}
	if !item.LastRunAt.IsZero() {
		return "used"
	}
	return "active"
}

func taskConsumesQuota(item assistant.Reminder) bool {
	if item.Kind == assistant.ReminderKindRepositoryWatch {
		return false
	}
	if item.Kind == assistant.ReminderKindRSSWatch {
		return !strings.HasPrefix(item.OwnerID, "webui") && item.CancelledAt.IsZero()
	}
	if item.Kind == assistant.ReminderKindQuery && item.IntervalSeconds > 0 {
		return item.CancelledAt.IsZero()
	}
	return item.LastRunAt.IsZero() && item.CancelledAt.IsZero()
}

func (h *BotHandler) recallGroupTestMessage(c *gin.Context) {
	var payload groupTestRecallPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.recall", err, "", nil)
		return
	}
	messageID := strings.TrimSpace(payload.MessageID)
	if messageID == "" {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.recall", fmt.Errorf("message_id is required"), "", nil)
		return
	}
	result, err := h.runtime.CallOneBotAPI(c.Request.Context(), "delete_msg", map[string]any{"message_id": oneBotIDParam(messageID)})
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.recall", err, messageID, map[string]any{"message_id": messageID})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.group_test.recall", "群测试消息已撤回", messageID, map[string]any{"message_id": messageID})
	c.JSON(http.StatusOK, groupTestRecallResponse{MessageID: messageID, Recalled: true, Result: result})
}

func (h *BotHandler) parseGroupTestFile(c *gin.Context) {
	var payload groupTestFilePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.file", err, "", nil)
		return
	}
	groupID := strings.TrimSpace(payload.GroupID)
	fileID := strings.TrimSpace(payload.FileID)
	name := strings.TrimSpace(payload.Name)
	localPath := strings.TrimSpace(payload.LocalPath)
	if name == "" || (localPath == "" && (groupID == "" || fileID == "")) {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.file", fmt.Errorf("name and either local_path or group_id plus file_id are required"), groupID, nil)
		return
	}
	if localPath != "" && !filepath.IsAbs(localPath) {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.file", fmt.Errorf("local_path must be absolute"), name, nil)
		return
	}
	if groupID != "" {
		if _, err := strconv.ParseInt(groupID, 10, 64); err != nil {
			h.writeError(c, http.StatusBadRequest, "assistant.group_test.file", fmt.Errorf("invalid group_id %q", groupID), groupID, nil)
			return
		}
	}
	segmentData := map[string]string{
		"name":    name,
		"file_id": fileID,
		"busid":   strings.TrimSpace(payload.BusID),
	}
	if localPath != "" {
		segmentData["path"] = localPath
	}
	logTarget := fileID
	if logTarget == "" {
		logTarget = name
	}
	testCfg := h.runtime.Config()
	plugin := assistant.NewFileParserPlugin(nil)
	resp, err := plugin.Handle(c.Request.Context(), assistant.PluginRequest{
		Channel: runtimeAPICallChannel{runtime: h.runtime},
		// 带上当前 profile 身份：MultiChannel 只有在单 binding 时才会兜底，
		// 多机器人下裸事件会因为找不到通道而直接失败。
		Event: assistant.MessageEvent{
			Kind:      assistant.EventKindGroup,
			GroupID:   groupID,
			Platform:  testCfg.Platform,
			ProfileID: testCfg.ID,
			Segments:  []assistant.MessageSegment{{Type: "file", Data: segmentData}},
		},
		Text: "群文件解析测试",
	})
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.file", err, logTarget, map[string]any{"group_id": groupID, "file_id": fileID})
		return
	}
	if resp == nil {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.file", fmt.Errorf("file parser returned no result"), logTarget, map[string]any{"group_id": groupID, "file_id": fileID})
		return
	}
	contextText := strings.TrimSpace(resp.Context)
	if contextText == "" && len(resp.Tasks) > 0 {
		runner, ok := h.runtime.(pluginTaskRunner)
		if !ok {
			h.writeError(c, http.StatusServiceUnavailable, "assistant.group_test.file", fmt.Errorf("plugin task runner is unavailable"), logTarget, map[string]any{"group_id": groupID, "file_id": fileID})
			return
		}
		results := make([]string, 0, len(resp.Tasks))
		for _, task := range resp.Tasks {
			result, taskErr := runner.RunPluginTask(c.Request.Context(), task)
			if taskErr != nil {
				h.writeError(c, http.StatusBadRequest, "assistant.group_test.file", taskErr, logTarget, map[string]any{"group_id": groupID, "file_id": fileID})
				return
			}
			if text := strings.TrimSpace(result.Reply); text != "" {
				results = append(results, text)
			}
		}
		contextText = strings.Join(results, "\n\n")
	}
	if contextText == "" {
		h.writeError(c, http.StatusBadRequest, "assistant.group_test.file", fmt.Errorf("file parser returned no result"), logTarget, map[string]any{"group_id": groupID, "file_id": fileID})
		return
	}
	recordRequestOperation(c, h.logs, "assistant.group_test.file", "群文件解析测试完成", logTarget, map[string]any{"group_id": groupID, "file_id": fileID, "name": name})
	c.JSON(http.StatusOK, groupTestFileResponse{GroupID: groupID, FileID: fileID, Name: name, Context: contextText})
}

func oneBotIDParam(value string) any {
	value = strings.TrimSpace(value)
	if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
		return parsed
	}
	return value
}

func firstNonEmptyWebUI(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func stringFromAnyWebUI(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func intFromAnyWebUI(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

type runtimeAPICallChannel struct {
	runtime BotRuntime
}

func (c runtimeAPICallChannel) Connect(context.Context, assistant.EventHandler) error { return nil }
func (c runtimeAPICallChannel) Send(context.Context, assistant.OutgoingMessage) error { return nil }
func (c runtimeAPICallChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	return c.runtime.CallOneBotAPI(ctx, action, params)
}
func (c runtimeAPICallChannel) Status() assistant.ChannelStatus { return c.runtime.Status().Channel }
func (c runtimeAPICallChannel) Close() error                    { return nil }
