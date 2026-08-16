// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// RepositoryIssueCreateInput is the authenticated WebUI payload for creating
// one Issue through the repository publishing plugin.
type RepositoryIssueCreateInput struct {
	Repository        string   `json:"repository"`
	Title             string   `json:"title"`
	Body              string   `json:"body,omitempty"`
	Labels            []string `json:"labels,omitempty"`
	AllowDuplicate    bool     `json:"allow_duplicate,omitempty"`
	ConfirmationToken string   `json:"confirmation_token,omitempty"`
	CandidateNumber   int      `json:"candidate_number,omitempty"`
}

// RepositoryIssueSummary is the public representation returned to WebUI.
type RepositoryIssueSummary struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	URL       string    `json:"url"`
	Labels    []string  `json:"labels,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// RepositoryIssueCreateResult keeps expected business failures structured so
// WebUI can present duplicate candidates without weakening the create guard.
type RepositoryIssueCreateResult struct {
	OK                   bool                     `json:"ok"`
	Outcome              string                   `json:"outcome,omitempty"`
	Repository           string                   `json:"repository,omitempty"`
	FailureCode          string                   `json:"failure_code,omitempty"`
	Message              string                   `json:"message"`
	Issue                *RepositoryIssueSummary  `json:"issue,omitempty"`
	Candidates           []RepositoryIssueSummary `json:"candidates,omitempty"`
	RequiresConfirmation bool                     `json:"requires_confirmation,omitempty"`
	ConfirmationToken    string                   `json:"confirmation_token,omitempty"`
	Idempotent           bool                     `json:"idempotent,omitempty"`
	Reconciled           bool                     `json:"reconciled,omitempty"`
	Redactions           int                      `json:"redactions,omitempty"`
}

// CreateIssueFromWeb executes the same validation, redaction, duplicate
// detection, idempotency, and reconciliation path used by the chat tool. The
// authenticated WebUI confirmation is the explicit mutation authorization.
func (p *RepositoryPublishPlugin) CreateIssueFromWeb(ctx context.Context, settings SettingValues, input RepositoryIssueCreateInput) RepositoryIssueCreateResult {
	if p == nil || p.client == nil {
		return repositoryIssueCreateResultFromInternal(repositoryIssueResult{Operation: "create"}.fail("plugin_unavailable", "仓库 Issue 发布插件未正确配置。"))
	}
	repository, err := normalizeGitHubRepository(input.Repository)
	if err != nil {
		return repositoryIssueCreateResultFromInternal(repositoryIssueResult{Operation: "create"}.fail("invalid_repository", err.Error()))
	}

	tool := &dianaRepositoryIssuesTool{
		plugin:   p,
		settings: settings,
		event: MessageEvent{
			Platform:  "webui",
			Kind:      EventKindPrivate,
			UserID:    "webui-owner",
			MessageID: repositoryIssueWebMessageID(),
		},
	}
	if code, message := tool.validateWriteAccess(repository, true); code != "" {
		result := repositoryIssueResult{Operation: "create", Repository: repository}.fail(code, message)
		return repositoryIssueCreateResultFromInternal(result)
	}

	arguments := map[string]any{
		"operation":  "create",
		"repository": repository,
		"title":      input.Title,
		"body":       input.Body,
	}
	if len(input.Labels) > 0 {
		arguments["labels"] = input.Labels
	}
	if input.AllowDuplicate {
		if strings.TrimSpace(input.ConfirmationToken) == "" || input.CandidateNumber <= 0 {
			result := repositoryIssueResult{Operation: "create", Repository: repository}.fail("invalid_confirmation", "确认重复创建时必须提供候选 Issue 编号和确认令牌。")
			return repositoryIssueCreateResultFromInternal(result)
		}
		arguments["allow_duplicate"] = true
		arguments["confirmation_token"] = input.ConfirmationToken
		tool.event.RawMessage = fmt.Sprintf("请仍然新建 GitHub Issue 到 %s，不复用候选 #%d", repository, input.CandidateNumber)
	}

	return repositoryIssueCreateResultFromInternal(tool.create(ctx, repository, arguments))
}

func repositoryIssueWebMessageID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return "webui-" + hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("webui-%d", time.Now().UnixNano())
}

func repositoryIssueCreateResultFromInternal(result repositoryIssueResult) RepositoryIssueCreateResult {
	converted := RepositoryIssueCreateResult{
		OK:                   result.OK,
		Outcome:              result.Outcome,
		Repository:           result.Repository,
		FailureCode:          result.FailureCode,
		Message:              result.Message,
		RequiresConfirmation: result.RequiresConfirmation,
		ConfirmationToken:    result.ConfirmationToken,
		Idempotent:           result.Idempotent,
		Reconciled:           result.Reconciled,
		Redactions:           result.Redactions,
	}
	if result.Issue != nil {
		issue := publicRepositoryIssueSummary(*result.Issue)
		converted.Issue = &issue
	}
	if len(result.Items) > 0 {
		converted.Candidates = make([]RepositoryIssueSummary, 0, len(result.Items))
		for _, item := range result.Items {
			converted.Candidates = append(converted.Candidates, publicRepositoryIssueSummary(item))
		}
	}
	return converted
}

func publicRepositoryIssueSummary(issue repositoryIssueSummary) RepositoryIssueSummary {
	return RepositoryIssueSummary{
		Number:    issue.Number,
		Title:     issue.Title,
		State:     issue.State,
		URL:       issue.URL,
		Labels:    append([]string(nil), issue.Labels...),
		UpdatedAt: issue.UpdatedAt,
	}
}
