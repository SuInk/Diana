package webui

import (
	"fmt"
	"net/http"

	"github.com/SuInk/diana/model/assistant"

	"github.com/gin-gonic/gin"
)

func (h *QQBotHandler) createRepositoryIssue(c *gin.Context) {
	var payload assistant.RepositoryIssueCreateInput
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.writeError(c, http.StatusBadRequest, "assistant.repository_issue.create", err, "", nil)
		return
	}
	if h.runtime == nil || h.runtime.Plugins() == nil {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.repository_issue.create", fmt.Errorf("插件管理器不可用"), payload.Repository, nil)
		return
	}
	pluginValue, settings, enabled := h.runtime.Plugins().PluginWithSettings(assistant.RepositoryPublishPluginID, nil)
	plugin, ok := pluginValue.(*assistant.RepositoryPublishPlugin)
	if !enabled || !ok {
		h.writeError(c, http.StatusServiceUnavailable, "assistant.repository_issue.create", fmt.Errorf("仓库 Issue 发布插件未启用"), payload.Repository, nil)
		return
	}

	result := plugin.CreateIssueFromWeb(c.Request.Context(), settings, payload)
	if !result.OK {
		if result.RequiresConfirmation {
			c.JSON(http.StatusOK, result)
			return
		}
		h.writeError(c, repositoryIssueCreateFailureStatus(result.FailureCode), "assistant.repository_issue.create", fmt.Errorf("%s", result.Message), result.Repository, map[string]any{
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
	recordRequestOperation(c, h.logs, "assistant.repository_issue.create", "GitHub Issue 已创建", target, metadata)
	c.JSON(status, result)
}

func repositoryIssueCreateFailureStatus(code string) int {
	switch code {
	case "repository_not_allowed":
		return http.StatusForbidden
	case "rate_limited":
		return http.StatusTooManyRequests
	case "timeout":
		return http.StatusGatewayTimeout
	case "unauthorized", "permission_denied":
		return http.StatusBadGateway
	case "network_error", "github_unavailable", "invalid_response", "idempotency_scan_incomplete":
		return http.StatusBadGateway
	default:
		return http.StatusBadRequest
	}
}
