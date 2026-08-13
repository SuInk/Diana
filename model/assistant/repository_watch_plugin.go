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
)

type RepositoryWatchPlugin struct {
	client  *http.Client
	baseURL string
}

type repositoryWatchSnapshot struct {
	CommitSHA  string
	ReleaseTag string
}

type repositoryWatchChange struct {
	Repository string                   `json:"repository"`
	Branch     string                   `json:"branch,omitempty"`
	Commits    []repositoryWatchCommit  `json:"commits,omitempty"`
	Releases   []repositoryWatchRelease `json:"releases,omitempty"`
	Truncated  bool                     `json:"commits_truncated,omitempty"`
	Snapshot   repositoryWatchSnapshot  `json:"-"`
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
		Name:        "仓库更新订阅",
		Version:     "0.1.0",
		Description: "在 WebUI 监控公开或私有 GitHub 仓库的 Commit 与 Release；检测到动态后由 LLM 汇总并通知指定群聊或私聊对象。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:https", "task:persistent", "message:send", "llm:generate"},
		Settings: []PluginSettingSpec{
			{
				Key:         repositoryWatchSettingToken,
				Label:       "GitHub Token",
				Description: "私有仓库必填。支持 fine-grained PAT、classic PAT 和 GitHub App token；fine-grained token 需授予目标仓库 Contents: read。保存后不会回显。",
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
				Label:       "摘要 Commit 上限",
				Description: "单次提醒交给 LLM 总结的最新 commit 数量；游标仍会推进到最新提交。",
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
	change, err := p.check(ctx, repository, branch, "", "", watchCommits, watchReleases, settings)
	if err != nil {
		return repositoryWatchSnapshot{}, err
	}
	return change.Snapshot, nil
}

func (p *RepositoryWatchPlugin) check(ctx context.Context, repository, branch, commitCursor, releaseCursor string, watchCommits, watchReleases bool, settings SettingValues) (repositoryWatchChange, error) {
	if p == nil || p.client == nil {
		return repositoryWatchChange{}, fmt.Errorf("repository watch: plugin is not configured")
	}
	if !watchCommits && !watchReleases {
		return repositoryWatchChange{}, fmt.Errorf("repository watch: at least one update type must be enabled")
	}
	change := repositoryWatchChange{Repository: repository, Branch: branch}
	var errs []error
	if watchCommits {
		commits, snapshot, truncated, err := p.fetchCommits(ctx, repository, branch, commitCursor, settings)
		if err != nil {
			errs = append(errs, err)
		} else {
			change.Commits = commits
			change.Snapshot.CommitSHA = snapshot
			change.Truncated = truncated
		}
	}
	if watchReleases {
		releases, snapshot, err := p.fetchReleases(ctx, repository, releaseCursor, settings)
		if err != nil {
			errs = append(errs, err)
		} else {
			change.Releases = releases
			change.Snapshot.ReleaseTag = snapshot
		}
	}
	if len(errs) > 0 {
		return repositoryWatchChange{}, errors.Join(errs...)
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
	foundCursor := false
	for _, item := range payload {
		if item.SHA == cursor {
			foundCursor = true
			break
		}
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
	return commits, latest, !foundCursor || len(commits) >= limit && len(payload) > limit, nil
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
			return fmt.Errorf("仓库不存在，或 GitHub Token 未获目标仓库访问权限；fine-grained token 需要目标仓库的 Contents: read 权限")
		case http.StatusForbidden:
			if strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0" {
				return fmt.Errorf("GitHub API 请求额度已耗尽，请配置有效 Token 或等待额度恢复")
			}
			if token != "" {
				return fmt.Errorf("GitHub Token 权限不足；私有仓库需要目标仓库的 Contents: read 权限")
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
