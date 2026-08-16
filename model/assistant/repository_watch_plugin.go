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
)

type RepositoryWatchPlugin struct {
	client  *http.Client
	baseURL string
}

type repositoryWatchSnapshot struct {
	CommitSHA         string
	PullRequestCursor string
	ReleaseTag        string
	StarCount         int
	HasStarCount      bool
}

type repositoryWatchSelection struct {
	Commits      bool
	PullRequests bool
	Releases     bool
	Stars        bool
}

type repositoryWatchChange struct {
	Repository   string                       `json:"repository"`
	Branch       string                       `json:"branch,omitempty"`
	Commits      []repositoryWatchCommit      `json:"commits,omitempty"`
	CommitDiff   *repositoryWatchDiff         `json:"commit_diff,omitempty"`
	PullRequests []repositoryWatchPullRequest `json:"pull_requests,omitempty"`
	Releases     []repositoryWatchRelease     `json:"releases,omitempty"`
	Stars        *repositoryWatchStarChange   `json:"stars,omitempty"`
	Truncated    bool                         `json:"commits_truncated,omitempty"`
	Snapshot     repositoryWatchSnapshot      `json:"-"`
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
	Files          []repositoryWatchDiffFile `json:"files,omitempty"`
	FilesTruncated bool                      `json:"files_truncated,omitempty"`
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
	Previous int    `json:"previous"`
	Current  int    `json:"current"`
	Delta    int    `json:"delta"`
	URL      string `json:"url,omitempty"`
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
		Description: "在 WebUI 监控公开或私有 GitHub 仓库的 Commit、PR、Release 与 Star；检测到动态后由 LLM 汇总并通知指定群聊或私聊对象。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:https", "task:persistent", "message:send", "llm:generate"},
		Settings: []PluginSettingSpec{
			{
				Key:         repositoryWatchSettingToken,
				Label:       "GitHub Token",
				Description: "公开仓库高频订阅也建议配置，可将 API 主额度从共享出口 IP 的 60 次/小时提高到通常 5,000 次/小时。私有仓库需授予 Contents: read；启用 PR 时还需 Pull requests: read。保存后不会回显。",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
			},
			{
				Key:         repositoryWatchSettingTimeout,
				Label:       "检查超时",
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
				Label:       "摘要动态上限",
				Description: "单次提醒交给 LLM 总结的最新 Commit 或 PR 数量；游标仍会推进到最新动态。",
				Type:        PluginSettingTypeNumber,
				Default:     12,
				Min:         settingRange(1),
				Max:         settingRange(30),
				Step:        1,
				Unit:        "条",
			},
		},
	}
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
	if !selection.Commits && !selection.PullRequests && !selection.Releases && !selection.Stars {
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
		stars, snapshot, err := p.fetchStars(ctx, repository, cursor, settings)
		if err != nil {
			errs = append(errs, err)
		} else {
			change.Stars = stars
			change.Snapshot.StarCount = snapshot
			change.Snapshot.HasStarCount = true
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
	limit := settings.Int(repositoryWatchSettingLimit, 12)
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
	limit := settings.Int(repositoryWatchSettingLimit, 12)
	result := make([]repositoryWatchPullRequest, 0, min(limit, len(filtered)))
	for _, item := range filtered {
		if !repositoryWatchPullAfterCursor(item.UpdatedAt, item.Number, cursor) {
			continue
		}
		if len(result) >= limit {
			continue
		}
		status := "updated"
		switch {
		case item.MergedAt != nil && !item.MergedAt.IsZero():
			status = "merged"
		case strings.EqualFold(item.State, "closed"):
			status = "closed"
		case item.CreatedAt.Equal(item.UpdatedAt):
			status = "opened"
		}
		files, filesTruncated, err := p.fetchPullRequestFiles(ctx, repository, item.Number, settings)
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
			UpdatedAt:      item.UpdatedAt,
			Files:          files,
			FilesTruncated: filesTruncated,
		})
	}
	return result, latest, nil
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

func (p *RepositoryWatchPlugin) fetchPullRequestFiles(ctx context.Context, repository string, number int, settings SettingValues) ([]repositoryWatchDiffFile, bool, error) {
	var payload []repositoryWatchDiffFilePayload
	path := fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100", repository, number)
	if err := p.getJSON(ctx, path, settings, &payload); err != nil {
		return nil, false, fmt.Errorf("读取 %s PR #%d diff: %w", repository, number, err)
	}
	files, truncated := repositoryWatchDiffFiles(payload, 30)
	return files, truncated, nil
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

func (p *RepositoryWatchPlugin) fetchStars(ctx context.Context, repository string, cursor repositoryWatchSnapshot, settings SettingValues) (*repositoryWatchStarChange, int, error) {
	var payload struct {
		StargazersCount int    `json:"stargazers_count"`
		HTMLURL         string `json:"html_url"`
	}
	if err := p.getJSON(ctx, "/repos/"+repository, settings, &payload); err != nil {
		return nil, 0, fmt.Errorf("读取 %s stars: %w", repository, err)
	}
	if !cursor.HasStarCount || cursor.StarCount == payload.StargazersCount {
		return nil, payload.StargazersCount, nil
	}
	return &repositoryWatchStarChange{
		Previous: cursor.StarCount,
		Current:  payload.StargazersCount,
		Delta:    payload.StargazersCount - cursor.StarCount,
		URL:      strings.TrimSpace(payload.HTMLURL),
	}, payload.StargazersCount, nil
}

func (p *RepositoryWatchPlugin) getJSON(ctx context.Context, path string, settings SettingValues, target any) error {
	timeout := time.Duration(settings.Int(repositoryWatchSettingTimeout, 20)) * time.Second
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Diana-Repository-Watch")
	token := strings.TrimSpace(settings.String(repositoryWatchSettingToken, ""))
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
