// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	repositoryWatchPluginID = "official.repository-watch"

	repositoryWatchSettingToken   = "github_token"
	repositoryWatchSettingTimeout = "timeout_seconds"
	repositoryWatchSettingLimit   = "summary_commit_limit"

	defaultGitHubAPIURL            = "https://api.github.com"
	repositoryWatchNoReleaseCursor = "__none__"
	repositoryWatchNoPullCursor    = "__none__"
	// repositoryWatchDefaultLimit 是每类动态默认列出的条数，同时用作抓取上限。
	repositoryWatchDefaultLimit  = 10
	repositoryWatchNoIssueCursor = "__none__"
)

type RepositoryWatchPlugin struct {
	client  *http.Client
	baseURL string
}

type repositoryWatchSnapshot struct {
	CommitSHA         string
	PullRequestCursor string
	IssueCursor       string
	ReleaseTag        string
	StarCount         int
	HasStarCount      bool
	StarCheckedAt     time.Time
}

type repositoryWatchSelection struct {
	Commits      bool
	PullRequests bool
	Issues       bool
	Releases     bool
	Stars        bool
}

type repositoryWatchChange struct {
	Repository   string                       `json:"repository"`
	Branch       string                       `json:"branch,omitempty"`
	Commits      []repositoryWatchCommit      `json:"commits,omitempty"`
	CommitDiff   *repositoryWatchDiff         `json:"commit_diff,omitempty"`
	PullRequests []repositoryWatchPullRequest `json:"pull_requests,omitempty"`
	Issues       []repositoryWatchIssue       `json:"issues,omitempty"`
	Releases     []repositoryWatchRelease     `json:"releases,omitempty"`
	Stars        *repositoryWatchStarChange   `json:"stars,omitempty"`
	Truncated    bool                         `json:"commits_truncated,omitempty"`
	// OmittedCommits 是超出「摘要动态上限」而没有列出的提交数，只影响通知末尾那句提示。
	OmittedCommits int                     `json:"omitted_commits,omitempty"`
	Snapshot       repositoryWatchSnapshot `json:"-"`
}

type repositoryWatchCommit struct {
	SHA      string    `json:"sha"`
	Title    string    `json:"title"`
	Author   string    `json:"author,omitempty"`
	URL      string    `json:"url,omitempty"`
	PushedAt time.Time `json:"pushed_at,omitempty"`
}

type repositoryWatchRelease struct {
	Tag         string    `json:"tag"`
	Name        string    `json:"name,omitempty"`
	Body        string    `json:"body,omitempty"`
	URL         string    `json:"url,omitempty"`
	PublishedAt time.Time `json:"published_at,omitempty"`
}

type repositoryWatchPullRequest struct {
	Number         int                       `json:"number"`
	Title          string                    `json:"title"`
	Author         string                    `json:"author,omitempty"`
	Status         string                    `json:"status"`
	URL            string                    `json:"url,omitempty"`
	BaseBranch     string                    `json:"base_branch,omitempty"`
	HeadBranch     string                    `json:"head_branch,omitempty"`
	MergeCommitSHA string                    `json:"merge_commit_sha,omitempty"`
	UpdatedAt      time.Time                 `json:"updated_at,omitempty"`
	OccurredAt     time.Time                 `json:"occurred_at,omitempty"`
	Files          []repositoryWatchDiffFile `json:"files,omitempty"`
	FilesTruncated bool                      `json:"files_truncated,omitempty"`
	// Commits 是本次更新里新推上来的提交；PR 只说「有更新」看不出改了什么，
	// 点进去才知道，通知里直接列出来省一次跳转。
	Commits        []repositoryWatchPullCommit `json:"commits,omitempty"`
	OmittedCommits int                         `json:"omitted_commits,omitempty"`
	// RewrittenCommits 是这轮被变基或强推重写、但内容并非新写的提交数。
	RewrittenCommits int `json:"rewritten_commits,omitempty"`
}

// repositoryWatchPullCommit 是 PR 里的一条提交，只留通知要用的字段。
type repositoryWatchPullCommit struct {
	SHA         string    `json:"sha"`
	Title       string    `json:"title"`
	Author      string    `json:"author,omitempty"`
	CommittedAt time.Time `json:"committed_at,omitempty"`
}

type repositoryWatchIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	Author    string    `json:"author,omitempty"`
	Status    string    `json:"status"`
	URL       string    `json:"url,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	ClosedAt  time.Time `json:"closed_at,omitempty"`
}

type repositoryWatchDiff struct {
	Base           string                    `json:"base,omitempty"`
	Head           string                    `json:"head,omitempty"`
	TotalCommits   int                       `json:"total_commits,omitempty"`
	AheadBy        int                       `json:"ahead_by,omitempty"`
	Files          []repositoryWatchDiffFile `json:"files,omitempty"`
	FilesTruncated bool                      `json:"files_truncated,omitempty"`
}

type repositoryWatchDiffFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status,omitempty"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
	Changes   int    `json:"changes,omitempty"`
	Patch     string `json:"patch,omitempty"`
}

type repositoryWatchDiffFilePayload struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Changes   int    `json:"changes"`
	Patch     string `json:"patch"`
}

type repositoryWatchStarChange struct {
	Previous   int                        `json:"previous"`
	Current    int                        `json:"current"`
	Delta      int                        `json:"delta"`
	URL        string                     `json:"url,omitempty"`
	AddedUsers []repositoryWatchStargazer `json:"added_users,omitempty"`
	DetectedAt time.Time                  `json:"detected_at,omitempty"`
}

type repositoryWatchStargazer struct {
	Login     string    `json:"login"`
	URL       string    `json:"url,omitempty"`
	StarredAt time.Time `json:"starred_at,omitempty"`
}

func NewRepositoryWatchPlugin(client *http.Client) *RepositoryWatchPlugin {
	return newRepositoryWatchPlugin(client, defaultGitHubAPIURL)
}

func newRepositoryWatchPlugin(client *http.Client, baseURL string) *RepositoryWatchPlugin {
	if client == nil {
		client = &http.Client{}
	}
	return &RepositoryWatchPlugin{client: client, baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/")}
}

func (p *RepositoryWatchPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          repositoryWatchPluginID,
		Name:        "仓库订阅",
		Version:     "0.2.0",
		Description: "在 WebUI 监控公开或私有 GitHub 仓库的 Commit、PR、Issue、Release 与 Star；检测到动态后生成事实摘要并通知指定群聊或私聊对象。",
		Official:    true,
		BuiltIn:     true,
		CanAskAgent: true,
		Permissions: []string{"network:https", "task:persistent", "message:send", "llm:generate"},
		Settings: []PluginSettingSpec{
			{
				Key:         pluginSettingAskAgent,
				Label:       "允许机器人跟评",
				Description: "事实清单发出去之后，让机器人像群成员那样再顺口说一句反应。它只是感想，不承载「改了什么」——那些以清单为准。关闭后只发送确定性的变更明细。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         repositoryWatchSettingToken,
				Label:       "GitHub Token",
				Description: "Fine-grained token 只访问选定仓库和明确权限，适合按仓库最小授权；Classic token 权限范围更大，适合查看未加入白名单的仓库、跨账号或组织访问及更多旧版 API。公开仓库也可匿名读取，但配置 Token 后请求额度更高。保存后不会回显。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         repositoryCredentialSettingList,
				Label:       "GitHub 凭据列表",
				Description: "由凭据编辑器维护；每条凭据有自己的名字和认证方式，仓库可在「仓库管理」里各自选用。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         repositoryCredentialSettingTokens,
				Label:       "GitHub 凭据密钥",
				Description: "由凭据编辑器维护；每条凭据的 Token 独立保存且不会回显。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         repositoryCredentialSettingConfigured,
				Label:       "已配置 Token 的凭据",
				Description: "由凭据编辑器维护。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         repositoryCredentialSettingBindings,
				Label:       "仓库使用的凭据",
				Description: "由仓库管理维护；未指定的仓库使用公共 Token。",
				Type:        PluginSettingTypeString,
				Default:     "",
			},
			{
				Key:         repositoryWatchSettingTimeout,
				Label:       "仓库检查超时",
				Description: "单次仓库动态检查的最长等待时间。",
				Type:        PluginSettingTypeNumber,
				Default:     20,
				Min:         settingRange(5),
				Max:         settingRange(60),
				Step:        1,
				Unit:        "秒",
			},
			{
				Key:         repositoryWatchSettingLimit,
				Label:       "每类动态展示条数",
				Description: "一次检查里 Commit、PR、Issue、Release 各自最多列出多少条。超出的提交会在末尾注明还剩多少条未列出，游标仍会推进到最新动态。",
				Type:        PluginSettingTypeNumber,
				Default:     repositoryWatchDefaultLimit,
				Min:         settingRange(1),
				Max:         settingRange(30),
				Step:        1,
				Unit:        "条",
			},
			{
				Key:         repositoryWatchSettingTemplateHeader,
				Label:       "推送模板",
				Description: "整条通知的组装格式，可用 {repository} {summary} {body}。{body} 是五类动态的事实清单，排版固定为「类型 + 标识 + 标题」加「谁于何时做了什么 · 链接」两行。单独一行写 <botbr> 表示从这里分成下一条消息发送，删掉那一行则合并成一条。留空使用默认格式；占位符所在行替换后为空会整行删除。",
				Type:        PluginSettingTypeText,
				Rows:        10,
				Default:     "",
			},
		},
	}
}

// MergeSecretSetting 按凭据 ID 合并 Token：界面只提交本次真的输入了的那几条，没提交
// 的沿用已存值，提交空串表示删除。和「用户 GitHub Token」用的是同一套做法。
func (p *RepositoryWatchPlugin) MergeSecretSetting(key, previous, submitted string) (string, error) {
	if key != repositoryCredentialSettingTokens {
		return submitted, nil
	}
	current := parseRepositoryCredentialTokens(previous)
	var updates map[string]*string
	if err := json.Unmarshal([]byte(submitted), &updates); err != nil {
		return "", fmt.Errorf("chatbot: invalid credential token update")
	}
	for rawID, token := range updates {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return "", fmt.Errorf("chatbot: invalid credential token update")
		}
		if token == nil || strings.TrimSpace(*token) == "" {
			delete(current, id)
			continue
		}
		current[id] = strings.TrimSpace(*token)
	}
	if len(current) == 0 {
		return "", nil
	}
	body, err := json.Marshal(current)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (*RepositoryWatchPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func normalizeGitHubRepository(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("repository 不能为空，请使用 owner/repo 或 GitHub 仓库链接")
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		if !strings.EqualFold(parsed.Hostname(), "github.com") {
			return "", fmt.Errorf("当前只支持 github.com 仓库")
		}
		raw = strings.Trim(parsed.Path, "/")
	}
	raw = strings.TrimSuffix(strings.TrimSpace(raw), ".git")
	parts := strings.Split(raw, "/")
	if len(parts) != 2 || strings.EqualFold(parts[0], "github.com") || !validGitHubPathPart(parts[0]) || !validGitHubPathPart(parts[1]) {
		return "", fmt.Errorf("repository 格式不正确，请使用 owner/repo 或完整 GitHub 仓库链接")
	}
	return parts[0] + "/" + parts[1], nil
}

func validGitHubPathPart(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func (p *RepositoryWatchPlugin) snapshot(ctx context.Context, repository, branch string, watchCommits, watchReleases bool, settings SettingValues) (repositoryWatchSnapshot, error) {
	return p.snapshotSelected(ctx, repository, branch, repositoryWatchSelection{Commits: watchCommits, Releases: watchReleases}, settings)
}

func (p *RepositoryWatchPlugin) snapshotSelected(ctx context.Context, repository, branch string, selection repositoryWatchSelection, settings SettingValues) (repositoryWatchSnapshot, error) {
	change, err := p.checkSelected(ctx, repository, branch, repositoryWatchSnapshot{}, selection, settings)
	if err != nil {
		return repositoryWatchSnapshot{}, err
	}
	return change.Snapshot, nil
}

func (p *RepositoryWatchPlugin) check(ctx context.Context, repository, branch, commitCursor, releaseCursor string, watchCommits, watchReleases bool, settings SettingValues) (repositoryWatchChange, error) {
	return p.checkSelected(ctx, repository, branch, repositoryWatchSnapshot{CommitSHA: commitCursor, ReleaseTag: releaseCursor}, repositoryWatchSelection{Commits: watchCommits, Releases: watchReleases}, settings)
}

func (p *RepositoryWatchPlugin) checkSelected(ctx context.Context, repository, branch string, cursor repositoryWatchSnapshot, selection repositoryWatchSelection, settings SettingValues) (repositoryWatchChange, error) {
	if p == nil || p.client == nil {
		return repositoryWatchChange{}, fmt.Errorf("repository watch: plugin is not configured")
	}
	if !selection.Commits && !selection.PullRequests && !selection.Issues && !selection.Releases && !selection.Stars {
		return repositoryWatchChange{}, fmt.Errorf("repository watch: at least one update type must be enabled")
	}
	change := repositoryWatchChange{Repository: repository, Branch: branch}
	var errs []error
	if selection.Commits {
		commits, snapshot, truncated, err := p.fetchCommits(ctx, repository, branch, cursor.CommitSHA, settings)
		if err != nil {
			errs = append(errs, err)
		} else {
			change.Commits = commits
			change.Snapshot.CommitSHA = snapshot
			change.Truncated = truncated
		}
	}
	if selection.PullRequests {
		pullRequests, snapshot, err := p.fetchPullRequests(ctx, repository, branch, cursor.PullRequestCursor, settings)
		if err != nil {
			errs = append(errs, err)
		} else {
			change.PullRequests = pullRequests
			change.Snapshot.PullRequestCursor = snapshot
		}
	}
	if selection.Commits && selection.PullRequests && len(change.Commits) > 0 && len(change.PullRequests) > 0 {
		change.Commits = p.foldMergedPullRequestCommits(ctx, repository, change.Commits, change.PullRequests, settings)
	}
	if selection.Issues {
		issues, snapshot, err := p.fetchIssues(ctx, repository, cursor.IssueCursor, settings)
		if err != nil {
			errs = append(errs, err)
		} else {
			change.Issues = issues
			change.Snapshot.IssueCursor = snapshot
		}
	}
	if selection.Releases {
		releases, snapshot, err := p.fetchReleases(ctx, repository, cursor.ReleaseTag, settings)
		if err != nil {
			errs = append(errs, err)
		} else {
			change.Releases = releases
			change.Snapshot.ReleaseTag = snapshot
		}
	}
	if selection.Stars {
		stars, count, checkedAt, err := p.fetchStars(ctx, repository, cursor, settings)
		if err != nil {
			errs = append(errs, err)
		} else {
			change.Stars = stars
			change.Snapshot.StarCount = count
			change.Snapshot.HasStarCount = true
			change.Snapshot.StarCheckedAt = checkedAt
		}
	}
	if len(errs) > 0 {
		return repositoryWatchChange{}, errors.Join(errs...)
	}
	if len(change.PullRequests) > 0 && len(change.Commits) > 0 {
		change.Commits = commitsWithoutPullRequestMerges(change.Commits, change.PullRequests)
	}
	if len(change.Commits) > 0 && strings.TrimSpace(cursor.CommitSHA) != "" && strings.TrimSpace(change.Snapshot.CommitSHA) != "" {
		diff, err := p.fetchCommitDiff(ctx, repository, cursor.CommitSHA, change.Snapshot.CommitSHA, settings)
		if err != nil {
			diff, err = p.fetchSingleCommitDiff(ctx, repository, change.Snapshot.CommitSHA, settings)
			if err != nil {
				return repositoryWatchChange{}, err
			}
		}
		change.CommitDiff = diff
	}
	return change, nil
}

func (p *RepositoryWatchPlugin) fetchCommits(ctx context.Context, repository, branch, cursor string, settings SettingValues) ([]repositoryWatchCommit, string, bool, error) {
	query := url.Values{"per_page": {"100"}}
	if strings.TrimSpace(branch) != "" {
		query.Set("sha", strings.TrimSpace(branch))
	}
	var payload []struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Message string `json:"message"`
			Author  struct {
				Name string    `json:"name"`
				Date time.Time `json:"date"`
			} `json:"author"`
		} `json:"commit"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
	}
	if err := p.getJSON(ctx, "/repos/"+repository+"/commits?"+query.Encode(), settings, &payload); err != nil {
		return nil, "", false, fmt.Errorf("读取 %s commits: %w", repository, err)
	}
	if len(payload) == 0 {
		return nil, "", false, fmt.Errorf("仓库 %s 没有可监控的 commit", repository)
	}
	latest := strings.TrimSpace(payload[0].SHA)
	if strings.TrimSpace(cursor) == "" {
		return nil, latest, false, nil
	}
	limit := settings.Int(repositoryWatchSettingLimit, repositoryWatchDefaultLimit)
	commits := make([]repositoryWatchCommit, 0, min(limit, len(payload)))
	newCommitCount := 0
	for _, item := range payload {
		if item.SHA == cursor {
			break
		}
		newCommitCount++
		if len(commits) >= limit {
			continue
		}
		author := strings.TrimSpace(item.Commit.Author.Name)
		if item.Author != nil && strings.TrimSpace(item.Author.Login) != "" {
			author = strings.TrimSpace(item.Author.Login)
		}
		commits = append(commits, repositoryWatchCommit{
			SHA:      item.SHA,
			Title:    firstLine(item.Commit.Message),
			Author:   author,
			URL:      item.HTMLURL,
			PushedAt: item.Commit.Author.Date,
		})
	}
	return commits, latest, newCommitCount > limit, nil
}

func (p *RepositoryWatchPlugin) fetchPullRequests(ctx context.Context, repository, branch, cursor string, settings SettingValues) ([]repositoryWatchPullRequest, string, error) {
	query := url.Values{
		"state":     {"all"},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {"100"},
	}
	var payload []struct {
		Number         int        `json:"number"`
		Title          string     `json:"title"`
		State          string     `json:"state"`
		HTMLURL        string     `json:"html_url"`
		CreatedAt      time.Time  `json:"created_at"`
		UpdatedAt      time.Time  `json:"updated_at"`
		MergedAt       *time.Time `json:"merged_at"`
		MergeCommitSHA string     `json:"merge_commit_sha"`
		User           struct {
			Login string `json:"login"`
		} `json:"user"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := p.getJSON(ctx, "/repos/"+repository+"/pulls?"+query.Encode(), settings, &payload); err != nil {
		return nil, "", fmt.Errorf("读取 %s pull requests: %w", repository, err)
	}
	branch = strings.TrimSpace(branch)
	filtered := payload[:0]
	for _, item := range payload {
		if branch == "" || strings.EqualFold(strings.TrimSpace(item.Base.Ref), branch) {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return nil, repositoryWatchNoPullCursor, nil
	}
	latest := repositoryWatchPullCursor(filtered[0].UpdatedAt, filtered[0].Number)
	if strings.TrimSpace(cursor) == "" {
		return nil, latest, nil
	}
	limit := settings.Int(repositoryWatchSettingLimit, repositoryWatchDefaultLimit)
	result := make([]repositoryWatchPullRequest, 0, min(limit, len(filtered)))
	for _, item := range filtered {
		if !repositoryWatchPullAfterCursor(item.UpdatedAt, item.Number, cursor) {
			continue
		}
		if len(result) >= limit {
			continue
		}
		status := "updated"
		occurredAt := item.UpdatedAt
		switch {
		case item.MergedAt != nil && !item.MergedAt.IsZero():
			status = "merged"
			occurredAt = *item.MergedAt
		case strings.EqualFold(item.State, "closed"):
			status = "closed"
		case item.CreatedAt.Equal(item.UpdatedAt):
			status = "opened"
			occurredAt = item.CreatedAt
		}
		files, filesTruncated, err := p.fetchPullRequestFiles(ctx, repository, item.Number, settings)
		if err != nil {
			return nil, "", err
		}
		// 新建的 PR 列出全部提交，更新的只列这轮新推上来的。
		since := time.Time{}
		if status == "updated" {
			since = repositoryWatchPullCursorTime(cursor)
		}
		commits, omittedCommits, rewrittenCommits, err := p.fetchPullRequestCommits(ctx, repository, item.Number, since, limit, settings)
		if err != nil {
			return nil, "", err
		}
		result = append(result, repositoryWatchPullRequest{
			Number:         item.Number,
			Title:          strings.TrimSpace(item.Title),
			Author:         strings.TrimSpace(item.User.Login),
			Status:         status,
			URL:            strings.TrimSpace(item.HTMLURL),
			BaseBranch:     strings.TrimSpace(item.Base.Ref),
			HeadBranch:     strings.TrimSpace(item.Head.Ref),
			MergeCommitSHA: strings.TrimSpace(item.MergeCommitSHA),
			UpdatedAt:      item.UpdatedAt, OccurredAt: occurredAt,
			Files:            files,
			FilesTruncated:   filesTruncated,
			Commits:          commits,
			OmittedCommits:   omittedCommits,
			RewrittenCommits: rewrittenCommits,
		})
	}
	return result, latest, nil
}

// repositoryWatchPullCursorTime 取出游标里的时间部分，也就是上一轮轮询的水位线。
// 游标缺失或格式不对时返回零值，调用方据此退回「不做时间过滤」。
func repositoryWatchPullCursorTime(cursor string) time.Time {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" || cursor == repositoryWatchNoPullCursor {
		return time.Time{}
	}
	separator := strings.LastIndex(cursor, "#")
	if separator <= 0 {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, cursor[:separator])
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func (p *RepositoryWatchPlugin) fetchCommitDiff(ctx context.Context, repository, base, head string, settings SettingValues) (*repositoryWatchDiff, error) {
	var payload struct {
		TotalCommits int                              `json:"total_commits"`
		AheadBy      int                              `json:"ahead_by"`
		Files        []repositoryWatchDiffFilePayload `json:"files"`
	}
	path := "/repos/" + repository + "/compare/" + url.PathEscape(strings.TrimSpace(base)) + "..." + url.PathEscape(strings.TrimSpace(head))
	if err := p.getJSON(ctx, path, settings, &payload); err != nil {
		return nil, fmt.Errorf("读取 %s commit diff: %w", repository, err)
	}
	files, truncated := repositoryWatchDiffFiles(payload.Files, 30)
	return &repositoryWatchDiff{
		Base: strings.TrimSpace(base), Head: strings.TrimSpace(head), TotalCommits: payload.TotalCommits,
		AheadBy: payload.AheadBy, Files: files, FilesTruncated: truncated,
	}, nil
}

func (p *RepositoryWatchPlugin) fetchSingleCommitDiff(ctx context.Context, repository, sha string, settings SettingValues) (*repositoryWatchDiff, error) {
	var payload struct {
		Files []repositoryWatchDiffFilePayload `json:"files"`
	}
	path := "/repos/" + repository + "/commits/" + url.PathEscape(strings.TrimSpace(sha))
	if err := p.getJSON(ctx, path, settings, &payload); err != nil {
		return nil, fmt.Errorf("读取 %s commit %s diff: %w", repository, sha, err)
	}
	files, truncated := repositoryWatchDiffFiles(payload.Files, 30)
	return &repositoryWatchDiff{Head: strings.TrimSpace(sha), TotalCommits: 1, AheadBy: 1, Files: files, FilesTruncated: truncated}, nil
}

// foldMergedPullRequestCommits 去掉那些已经被同一条通知里的「已合并 PR」代表了的
// 提交。PR 合并后它的提交才会落到被监控分支上，于是同一份工作报两次：一次是 PR
// 条目，一次是它带上来的每个提交，外加一条「Merge pull request #N」。PR 条目已经
// 给了标题、作者、分支和链接，逐个提交只是重复。
//
// 拿不到某个 PR 的提交列表时保留原样：宁可多报，不能因为一次 API 失败就把提交吞掉。
func (p *RepositoryWatchPlugin) foldMergedPullRequestCommits(ctx context.Context, repository string, commits []repositoryWatchCommit, pullRequests []repositoryWatchPullRequest, settings SettingValues) []repositoryWatchCommit {
	covered := make(map[string]bool)
	for _, pullRequest := range pullRequests {
		if !strings.EqualFold(strings.TrimSpace(pullRequest.Status), "merged") {
			continue
		}
		// 合并提交本身（squash 时就是那条压缩提交）与 PR 条目完全重复。
		if sha := strings.ToLower(strings.TrimSpace(pullRequest.MergeCommitSHA)); sha != "" {
			covered[sha] = true
		}
		shas, err := p.fetchPullRequestCommitSHAs(ctx, repository, pullRequest.Number, settings)
		if err != nil {
			continue
		}
		for sha := range shas {
			covered[sha] = true
		}
	}
	if len(covered) == 0 {
		return commits
	}
	kept := make([]repositoryWatchCommit, 0, len(commits))
	for _, commit := range commits {
		if covered[strings.ToLower(strings.TrimSpace(commit.SHA))] {
			continue
		}
		kept = append(kept, commit)
	}
	return kept
}

func (p *RepositoryWatchPlugin) fetchPullRequestCommitSHAs(ctx context.Context, repository string, number int, settings SettingValues) (map[string]bool, error) {
	var payload []struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/commits?per_page=100", repository, number)
	if err := p.getJSON(ctx, path, settings, &payload); err != nil {
		return nil, fmt.Errorf("读取 %s PR #%d commits: %w", repository, number, err)
	}
	shas := make(map[string]bool, len(payload))
	for _, item := range payload {
		if sha := strings.ToLower(strings.TrimSpace(item.SHA)); sha != "" {
			shas[sha] = true
		}
	}
	return shas, nil
}

func (p *RepositoryWatchPlugin) fetchPullRequestFiles(ctx context.Context, repository string, number int, settings SettingValues) ([]repositoryWatchDiffFile, bool, error) {
	var payload []repositoryWatchDiffFilePayload
	path := fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100", repository, number)
	if err := p.getJSON(ctx, path, settings, &payload); err != nil {
		return nil, false, fmt.Errorf("读取 %s PR #%d diff: %w", repository, number, err)
	}
	files, truncated := repositoryWatchDiffFiles(payload, 30)
	return files, truncated, nil
}

// fetchPullRequestCommits 取这个 PR 里比 since 更新的提交。since 用的是上一轮轮询的
// 水位线，所以拿到的正好是「上次通知之后新推上来的那些」。
//
// since 为零值（首次看到这个 PR）时返回全部提交，因为整个 PR 都是新的。
//
// 筛选用的是 committer date，强推之后的表现分两种，都不做特殊标记：
//   - rebase、amend 会把 committer date 刷成当前时间（author date 才保留），
//     于是整条分支的提交都晚于水位线，会被重列一遍，看上去像突然多了一批新提交；
//   - 把分支重置到更早的状态再推，提交时间还是旧的，筛不出来，那条更新就没有提交行。
//
// 后一种没办法，也不必要。前一种能靠 author date 认出来：变基只刷 committer date，
// author date 原样保留，所以「committer 新、author 旧」就是被重写的既有提交。它们
// 单独计数，不混进新增列表，免得一次变基看上去像一批新改动。
func (p *RepositoryWatchPlugin) fetchPullRequestCommits(ctx context.Context, repository string, number int, since time.Time, limit int, settings SettingValues) ([]repositoryWatchPullCommit, int, int, error) {
	if limit <= 0 {
		limit = repositoryWatchDefaultLimit
	}
	var payload []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message   string `json:"message"`
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
			Author struct {
				Name string    `json:"name"`
				Date time.Time `json:"date"`
			} `json:"author"`
		} `json:"commit"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/commits?per_page=100", repository, number)
	if err := p.getJSON(ctx, path, settings, &payload); err != nil {
		return nil, 0, 0, fmt.Errorf("读取 %s PR #%d 提交: %w", repository, number, err)
	}
	fresh := make([]repositoryWatchPullCommit, 0, len(payload))
	rewritten := 0
	for _, item := range payload {
		committedAt := item.Commit.Committer.Date
		if !since.IsZero() && !committedAt.IsZero() && !committedAt.After(since) {
			continue
		}
		// committer 时间新、author 时间旧，说明这条提交的内容早就写好了，这轮只是被
		// 变基或强推重写了一遍。照旧当成新提交列出来，读者会以为发生了一批新改动。
		if authoredAt := item.Commit.Author.Date; !since.IsZero() && !authoredAt.IsZero() && !authoredAt.After(since) {
			rewritten++
			continue
		}
		title := strings.TrimSpace(item.Commit.Message)
		if index := strings.IndexAny(title, "\r\n"); index >= 0 {
			title = strings.TrimSpace(title[:index])
		}
		fresh = append(fresh, repositoryWatchPullCommit{
			SHA:         strings.TrimSpace(item.SHA),
			Title:       title,
			Author:      strings.TrimSpace(firstNonEmpty(item.Author.Login, item.Commit.Author.Name)),
			CommittedAt: committedAt,
		})
	}
	// GitHub 按时间正序返回，通知里先看最新的更顺眼。
	for left, right := 0, len(fresh)-1; left < right; left, right = left+1, right-1 {
		fresh[left], fresh[right] = fresh[right], fresh[left]
	}
	if len(fresh) > limit {
		return fresh[:limit], len(fresh) - limit, rewritten, nil
	}
	return fresh, 0, rewritten, nil
}

func repositoryWatchDiffFiles(payload []repositoryWatchDiffFilePayload, limit int) ([]repositoryWatchDiffFile, bool) {
	if limit <= 0 {
		limit = 30
	}
	files := make([]repositoryWatchDiffFile, 0, min(limit, len(payload)))
	for _, item := range payload {
		if len(files) >= limit {
			break
		}
		files = append(files, repositoryWatchDiffFile{
			Filename: strings.TrimSpace(item.Filename), Status: strings.TrimSpace(item.Status),
			Additions: item.Additions, Deletions: item.Deletions, Changes: item.Changes,
			Patch: truncateRunes(strings.TrimSpace(item.Patch), 2000),
		})
	}
	return files, len(payload) > limit
}

func repositoryWatchPullCursor(updatedAt time.Time, number int) string {
	if updatedAt.IsZero() {
		return fmt.Sprintf("0#%d", number)
	}
	return fmt.Sprintf("%s#%d", updatedAt.UTC().Format(time.RFC3339Nano), number)
}

func repositoryWatchPullAfterCursor(updatedAt time.Time, number int, cursor string) bool {
	cursor = strings.TrimSpace(cursor)
	if cursor == repositoryWatchNoPullCursor {
		return true
	}
	separator := strings.LastIndex(cursor, "#")
	if separator <= 0 || separator == len(cursor)-1 {
		return repositoryWatchPullCursor(updatedAt, number) != cursor
	}
	cursorTime, err := time.Parse(time.RFC3339Nano, cursor[:separator])
	if err != nil {
		return repositoryWatchPullCursor(updatedAt, number) != cursor
	}
	var cursorNumber int
	if _, err := fmt.Sscanf(cursor[separator+1:], "%d", &cursorNumber); err != nil {
		return repositoryWatchPullCursor(updatedAt, number) != cursor
	}
	return updatedAt.After(cursorTime) || updatedAt.Equal(cursorTime) && number > cursorNumber
}

func (p *RepositoryWatchPlugin) fetchIssues(ctx context.Context, repository, cursor string, settings SettingValues) ([]repositoryWatchIssue, string, error) {
	query := url.Values{
		"state":     {"all"},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {"100"},
	}
	var payload []struct {
		Number      int        `json:"number"`
		Title       string     `json:"title"`
		Body        string     `json:"body"`
		State       string     `json:"state"`
		StateReason string     `json:"state_reason"`
		HTMLURL     string     `json:"html_url"`
		CreatedAt   time.Time  `json:"created_at"`
		UpdatedAt   time.Time  `json:"updated_at"`
		ClosedAt    *time.Time `json:"closed_at"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
		PullRequest *json.RawMessage `json:"pull_request"`
	}
	if err := p.getJSON(ctx, "/repos/"+repository+"/issues?"+query.Encode(), settings, &payload); err != nil {
		return nil, "", fmt.Errorf("读取 %s issues: %w", repository, err)
	}
	filtered := payload[:0]
	for _, item := range payload {
		if item.PullRequest == nil {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return nil, repositoryWatchNoIssueCursor, nil
	}
	latest := repositoryWatchPullCursor(filtered[0].UpdatedAt, filtered[0].Number)
	if strings.TrimSpace(cursor) == "" {
		return nil, latest, nil
	}
	limit := settings.Int(repositoryWatchSettingLimit, repositoryWatchDefaultLimit)
	result := make([]repositoryWatchIssue, 0, min(limit, len(filtered)))
	for _, item := range filtered {
		if !repositoryWatchPullAfterCursor(item.UpdatedAt, item.Number, cursor) || len(result) >= limit {
			continue
		}
		status := "updated"
		switch {
		case strings.EqualFold(item.State, "closed"):
			status = "closed"
		case strings.EqualFold(item.StateReason, "reopened"):
			status = "reopened"
		case item.CreatedAt.Equal(item.UpdatedAt):
			status = "opened"
		}
		closedAt := time.Time{}
		if item.ClosedAt != nil {
			closedAt = *item.ClosedAt
		}
		result = append(result, repositoryWatchIssue{
			Number: item.Number, Title: strings.TrimSpace(item.Title), Body: truncateRunes(strings.TrimSpace(item.Body), 4000),
			Author: strings.TrimSpace(item.User.Login), Status: status, URL: strings.TrimSpace(item.HTMLURL),
			CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, ClosedAt: closedAt,
		})
	}
	return result, latest, nil
}

func commitsWithoutPullRequestMerges(commits []repositoryWatchCommit, pullRequests []repositoryWatchPullRequest) []repositoryWatchCommit {
	mergeSHAs := make(map[string]struct{}, len(pullRequests))
	for _, pullRequest := range pullRequests {
		if sha := strings.TrimSpace(pullRequest.MergeCommitSHA); sha != "" {
			mergeSHAs[sha] = struct{}{}
		}
	}
	if len(mergeSHAs) == 0 {
		return commits
	}
	filtered := commits[:0]
	for _, commit := range commits {
		if _, mergedPullRequest := mergeSHAs[strings.TrimSpace(commit.SHA)]; !mergedPullRequest {
			filtered = append(filtered, commit)
		}
	}
	return filtered
}

func (p *RepositoryWatchPlugin) fetchReleases(ctx context.Context, repository, cursor string, settings SettingValues) ([]repositoryWatchRelease, string, error) {
	var payload []struct {
		Tag         string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Draft       bool      `json:"draft"`
	}
	if err := p.getJSON(ctx, "/repos/"+repository+"/releases?per_page=50", settings, &payload); err != nil {
		return nil, "", fmt.Errorf("读取 %s releases: %w", repository, err)
	}
	latest := ""
	result := make([]repositoryWatchRelease, 0, 4)
	for _, item := range payload {
		if item.Draft {
			continue
		}
		if latest == "" {
			latest = item.Tag
		}
		if cursor == "" || item.Tag == cursor {
			if item.Tag == cursor {
				break
			}
			continue
		}
		result = append(result, repositoryWatchRelease{
			Tag:         item.Tag,
			Name:        item.Name,
			Body:        truncateRunes(strings.TrimSpace(item.Body), 4000),
			URL:         item.HTMLURL,
			PublishedAt: item.PublishedAt,
		})
	}
	if latest == "" {
		latest = repositoryWatchNoReleaseCursor
	}
	return result, latest, nil
}

func (p *RepositoryWatchPlugin) fetchStars(ctx context.Context, repository string, cursor repositoryWatchSnapshot, settings SettingValues) (*repositoryWatchStarChange, int, time.Time, error) {
	checkedAt := time.Now()
	var payload struct {
		StargazersCount int    `json:"stargazers_count"`
		HTMLURL         string `json:"html_url"`
	}
	if err := p.getJSON(ctx, "/repos/"+repository, settings, &payload); err != nil {
		return nil, 0, time.Time{}, fmt.Errorf("读取 %s stars: %w", repository, err)
	}
	if !cursor.HasStarCount || cursor.StarCount == payload.StargazersCount {
		return nil, payload.StargazersCount, checkedAt, nil
	}
	change := &repositoryWatchStarChange{
		Previous:   cursor.StarCount,
		Current:    payload.StargazersCount,
		Delta:      payload.StargazersCount - cursor.StarCount,
		URL:        strings.TrimSpace(payload.HTMLURL),
		DetectedAt: checkedAt,
	}
	if change.Delta > 0 {
		users, err := p.fetchRecentStargazers(ctx, repository, payload.StargazersCount, change.Delta, cursor.StarCheckedAt, settings)
		if err == nil {
			change.AddedUsers = users
		}
	}
	return change, payload.StargazersCount, checkedAt, nil
}

func (p *RepositoryWatchPlugin) fetchRecentStargazers(ctx context.Context, repository string, total, delta int, since time.Time, settings SettingValues) ([]repositoryWatchStargazer, error) {
	if total <= 0 || delta <= 0 {
		return nil, nil
	}
	const perPage = 100
	lastPage := (total-1)/perPage + 1
	pageCount := (delta-1)/perPage + 1
	if pageCount < 1 {
		pageCount = 1
	}
	if pageCount > 3 {
		pageCount = 3
	}
	firstPage := max(1, lastPage-pageCount)
	users := make([]repositoryWatchStargazer, 0, min(delta, 300))
	for page := firstPage; page <= lastPage; page++ {
		var payload []struct {
			StarredAt time.Time `json:"starred_at"`
			User      struct {
				Login   string `json:"login"`
				HTMLURL string `json:"html_url"`
			} `json:"user"`
		}
		path := fmt.Sprintf("/repos/%s/stargazers?per_page=%d&page=%d", repository, perPage, page)
		if err := p.getJSONAccept(ctx, path, settings, "application/vnd.github.star+json", &payload); err != nil {
			return nil, fmt.Errorf("读取 %s stargazers: %w", repository, err)
		}
		for _, item := range payload {
			if !since.IsZero() && !item.StarredAt.After(since) {
				continue
			}
			if login := strings.TrimSpace(item.User.Login); login != "" {
				users = append(users, repositoryWatchStargazer{Login: login, URL: strings.TrimSpace(item.User.HTMLURL), StarredAt: item.StarredAt})
			}
		}
	}
	if len(users) > delta {
		users = users[len(users)-delta:]
	}
	return users, nil
}

func (p *RepositoryWatchPlugin) getJSON(ctx context.Context, path string, settings SettingValues, target any) error {
	return p.getJSONAccept(ctx, path, settings, "application/vnd.github+json", target)
}

func (p *RepositoryWatchPlugin) getJSONAccept(ctx context.Context, path string, settings SettingValues, accept string, target any) error {
	timeout := time.Duration(settings.Int(repositoryWatchSettingTimeout, 20)) * time.Second
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Diana-Repository-Watch")
	// 仓库单独绑了凭据就用它，否则沿用公共 Token。gh 类型的凭据这里取不到 Token，
	// 订阅轮询是后台任务、不便调用 gh，此时同样退回公共 Token。
	token := strings.TrimSpace(settings.String(repositoryWatchSettingToken, ""))
	if _, credentialToken, ok := repositoryCredentialFor(repositoryFromGitHubAPIPath(path), settings); ok && credentialToken != "" {
		token = credentialToken
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		message := strings.TrimSpace(string(body))
		var apiErr struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &apiErr) == nil && strings.TrimSpace(apiErr.Message) != "" {
			message = strings.TrimSpace(apiErr.Message)
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Errorf("GitHub Token 无效或已过期，请在插件设置中重新配置")
		case http.StatusNotFound:
			if token == "" {
				return fmt.Errorf("仓库不存在或为私有仓库；私有仓库请先在插件设置中配置 GitHub Token")
			}
			return fmt.Errorf("仓库不存在，或 GitHub Token 未获目标仓库访问权限；fine-grained token 需要 Contents: read，启用 PR 时还需要 Pull requests: read")
		case http.StatusForbidden, http.StatusTooManyRequests:
			if strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0" {
				if token == "" {
					return fmt.Errorf("GitHub API 匿名请求额度已耗尽（公开仓库同样受限）。请前往「插件 → 仓库更新订阅 → 设置」配置 GitHub Token，或等待额度恢复")
				}
				return fmt.Errorf("GitHub API Token 请求额度已耗尽。请等待额度恢复，或前往「插件 → 仓库更新订阅 → 设置」更换 Token")
			}
			if resp.StatusCode == http.StatusTooManyRequests || strings.Contains(strings.ToLower(message), "rate limit") {
				return fmt.Errorf("GitHub API 暂时限流。请稍后重试；未配置 Token 时，可前往「插件 → 仓库更新订阅 → 设置」配置")
			}
			if token != "" {
				return fmt.Errorf("GitHub Token 权限不足；私有仓库需要 Contents: read，启用 PR 时还需要 Pull requests: read")
			}
		}
		return fmt.Errorf("GitHub API %s: %s", resp.Status, firstNonEmpty(message, "请求失败"))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(target); err != nil {
		return fmt.Errorf("解析 GitHub API 响应: %w", err)
	}
	return nil
}

func firstLine(value string) string {
	if index := strings.IndexAny(value, "\r\n"); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}
