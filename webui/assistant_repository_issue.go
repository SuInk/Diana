// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

func (h *BotHandler) listRepositoryIssueDrafts(c *gin.Context) {
	if h.sqlite == nil {
		h.writeError(c, http.StatusServiceUnavailable, "repository_issue_drafts", fmt.Errorf("草稿存储不可用"), "", nil)
		return
	}
	status := strings.ToLower(strings.TrimSpace(c.DefaultQuery("status", "all")))
	if status != "all" && status != "pending" && status != "created" && status != "cancelled" && status != "expired" {
		h.writeError(c, http.StatusBadRequest, "repository_issue_drafts", fmt.Errorf("无效的草稿状态"), status, nil)
		return
	}
	// 过期不是存储里的状态，而是待审批草稿过了有效期。查的时候仍按 pending 取，
	// 再按时间分到「待审批」和「已过期」两边。
	query := status
	if status == "expired" {
		query = "pending"
	}
	items, err := h.sqlite.ListRepositoryIssueDrafts(c.Request.Context(), "", query)
	if err != nil {
		h.writeError(c, http.StatusInternalServerError, "repository_issue_drafts", err, status, nil)
		return
	}
	now := time.Now()
	drafts := make([]assistant.RepositoryIssueDraft, 0, len(items))
	for _, draft := range items {
		switch status {
		case "expired":
			if !draft.Expired(now) {
				continue
			}
		case "pending":
			if draft.Expired(now) {
				continue
			}
		}
		drafts = append(drafts, draft)
	}
	c.JSON(http.StatusOK, gin.H{"drafts": drafts})
}

// restoreRepositoryIssueDraft 让过期或已取消的草稿回到待审批，并换一个确认码。
func (h *BotHandler) restoreRepositoryIssueDraft(c *gin.Context) {
	h.withRepositoryIssueDraft(c, "repository_issue_draft_restore", "Issue 草稿已还原", func(plugin *assistant.RepositoryPublishPlugin, id string) (assistant.RepositoryIssueDraft, error) {
		return plugin.RestoreDraft(c.Request.Context(), id)
	})
}

// editRepositoryIssueDraft 改写草稿的标题、正文和标签。
func (h *BotHandler) editRepositoryIssueDraft(c *gin.Context) {
	var payload struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "repository_issue_draft_edit", err, strings.TrimSpace(c.Param("id")), nil)
		return
	}
	h.withRepositoryIssueDraft(c, "repository_issue_draft_edit", "Issue 草稿已修改", func(plugin *assistant.RepositoryPublishPlugin, id string) (assistant.RepositoryIssueDraft, error) {
		return plugin.EditDraftFromWeb(c.Request.Context(), id, payload.Title, payload.Body, payload.Labels)
	})
}

// deleteRepositoryIssueDraft 删掉草稿记录。已经写进 GitHub 的 Issue 不受影响。
func (h *BotHandler) deleteRepositoryIssueDraft(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		h.writeError(c, http.StatusBadRequest, "repository_issue_draft_delete", fmt.Errorf("缺少草稿 ID"), "", nil)
		return
	}
	plugin, _, ok := h.repositoryPublishPluginFor(c, "repository_issue_draft_delete", id)
	if !ok {
		return
	}
	if err := plugin.DeleteDraft(c.Request.Context(), id); err != nil {
		h.writeError(c, http.StatusBadRequest, "repository_issue_draft_delete", err, id, nil)
		return
	}
	recordRequestOperation(c, h.logs, "repository_issue_draft_delete", "Issue 草稿已删除", id, map[string]any{"draft_id": id})
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// withRepositoryIssueDraft 把「取插件 → 执行 → 记审计 → 回草稿」这套壳子共用起来。
func (h *BotHandler) withRepositoryIssueDraft(c *gin.Context, action, message string, run func(*assistant.RepositoryPublishPlugin, string) (assistant.RepositoryIssueDraft, error)) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		h.writeError(c, http.StatusBadRequest, action, fmt.Errorf("缺少草稿 ID"), "", nil)
		return
	}
	plugin, _, ok := h.repositoryPublishPluginFor(c, action, id)
	if !ok {
		return
	}
	draft, err := run(plugin, id)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, action, err, id, nil)
		return
	}
	recordRequestOperation(c, h.logs, action, message, draft.Repository, map[string]any{
		"draft_id":   draft.ID,
		"repository": draft.Repository,
		"expires_at": draft.ExpiresAt,
	})
	c.JSON(http.StatusOK, gin.H{"draft": draft})
}

// publishRepositoryIssueDraft 由后台直接把草稿写进 GitHub，不走群里的确认码。
func (h *BotHandler) publishRepositoryIssueDraft(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		h.writeError(c, http.StatusBadRequest, "repository_issue_draft_publish", fmt.Errorf("缺少草稿 ID"), "", nil)
		return
	}
	plugin, settings, ok := h.repositoryPublishPluginFor(c, "repository_issue_draft_publish", id)
	if !ok {
		return
	}
	result, err := plugin.PublishDraftFromWeb(c.Request.Context(), settings, id)
	if err != nil {
		h.writeError(c, http.StatusBadRequest, "repository_issue_draft_publish", err, id, nil)
		return
	}
	if !result.OK {
		h.writeError(c, repositoryIssueCreateFailureStatus(result.FailureCode), "repository_issue_draft_publish", fmt.Errorf("%s", result.Message), result.Repository, map[string]any{
			"draft_id":     id,
			"repository":   result.Repository,
			"failure_code": result.FailureCode,
		})
		return
	}
	metadata := map[string]any{"draft_id": id, "repository": result.Repository, "outcome": result.Outcome}
	target := result.Repository
	if result.Issue != nil {
		metadata["issue_number"] = result.Issue.Number
		metadata["issue_url"] = result.Issue.URL
		target = result.Issue.URL
	}
	recordRequestOperation(c, h.logs, "repository_issue_draft_publish", "Issue 草稿已由后台发布", target, metadata)
	c.JSON(http.StatusOK, result)
}

// repositoryPublishPluginFor 取出当前 profile 下启用的仓库发布插件。
func (h *BotHandler) repositoryPublishPluginFor(c *gin.Context, action, target string) (*assistant.RepositoryPublishPlugin, assistant.SettingValues, bool) {
	if h.runtime == nil || h.runtime.Plugins() == nil {
		h.writeError(c, http.StatusServiceUnavailable, action, fmt.Errorf("插件管理器不可用"), target, nil)
		return nil, nil, false
	}
	profileID, ok := h.pluginProfileScope(c)
	if !ok {
		return nil, nil, false
	}
	pluginValue, settings, enabled := h.runtime.Plugins().PluginWithSettingsForProfile(assistant.RepositoryPublishPluginID, profileID)
	plugin, ok := pluginValue.(*assistant.RepositoryPublishPlugin)
	if !enabled || !ok {
		h.writeError(c, http.StatusServiceUnavailable, action, fmt.Errorf("仓库 Issue 发布插件未启用"), target, nil)
		return nil, nil, false
	}
	return plugin, settings, true
}

func (h *BotHandler) createRepositoryIssue(c *gin.Context) {
	var payload assistant.RepositoryIssueCreateInput
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "repository_issue_create", err, "", nil)
		return
	}
	if h.runtime == nil || h.runtime.Plugins() == nil {
		h.writeError(c, http.StatusServiceUnavailable, "repository_issue_create", fmt.Errorf("插件管理器不可用"), payload.Repository, nil)
		return
	}
	profileID, ok := h.pluginProfileScope(c)
	if !ok {
		return
	}
	pluginValue, settings, enabled := h.runtime.Plugins().PluginWithSettingsForProfile(assistant.RepositoryPublishPluginID, profileID)
	plugin, ok := pluginValue.(*assistant.RepositoryPublishPlugin)
	if !enabled || !ok {
		h.writeError(c, http.StatusServiceUnavailable, "repository_issue_create", fmt.Errorf("仓库 Issue 发布插件未启用"), payload.Repository, nil)
		return
	}

	result := plugin.CreateIssueFromWeb(c.Request.Context(), settings, payload)
	if !result.OK {
		if result.RequiresConfirmation {
			c.JSON(http.StatusOK, result)
			return
		}
		h.writeError(c, repositoryIssueCreateFailureStatus(result.FailureCode), "repository_issue_create", fmt.Errorf("%s", result.Message), result.Repository, map[string]any{
			"repository":   result.Repository,
			"failure_code": result.FailureCode,
			"redactions":   result.Redactions,
		})
		return
	}

	status := http.StatusCreated
	if result.Outcome != "created" {
		status = http.StatusOK
	}
	metadata := map[string]any{
		"repository": result.Repository,
		"outcome":    result.Outcome,
		"idempotent": result.Idempotent,
		"reconciled": result.Reconciled,
		"redactions": result.Redactions,
	}
	target := result.Repository
	if result.Issue != nil {
		metadata["issue_number"] = result.Issue.Number
		metadata["issue_url"] = result.Issue.URL
		target = result.Issue.URL
	}
	recordRequestOperation(c, h.logs, "repository_issue_create", "GitHub Issue 已创建", target, metadata)
	c.JSON(status, result)
}

func repositoryIssueCreateFailureStatus(code string) int {
	switch code {
	case "repository_not_allowed", "permission_denied":
		return http.StatusForbidden
	case "rate_limited":
		return http.StatusTooManyRequests
	case "timeout":
		return http.StatusGatewayTimeout
	case "unauthorized":
		return http.StatusBadGateway
	case "network_error", "github_unavailable", "gh_unavailable", "gh_auth_required", "invalid_response", "idempotency_scan_incomplete":
		return http.StatusBadGateway
	default:
		return http.StatusBadRequest
	}
}
