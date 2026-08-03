package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ChangelogEntry 是 GitHub 提交日志里的一条记录。
type ChangelogEntry struct {
	SHA     string    `json:"sha"`
	Short   string    `json:"short"`
	Message string    `json:"message"`
	Author  string    `json:"author,omitempty"`
	Date    time.Time `json:"date,omitempty"`
	URL     string    `json:"url,omitempty"`
}

// ReleaseEntry 是 GitHub Release 的一条记录。
type ReleaseEntry struct {
	Tag               string    `json:"tag"`
	Name              string    `json:"name,omitempty"`
	Notes             string    `json:"notes,omitempty"`
	Prerelease        bool      `json:"prerelease,omitempty"`
	Date              time.Time `json:"date,omitempty"`
	URL               string    `json:"url,omitempty"`
	ChecksumAvailable bool      `json:"checksum_available"`
	ChecksumURL       string    `json:"checksum_url,omitempty"`
}

const releaseNotesMaxRunes = 600

// fetchGitHubReleases 拉取仓库最近的 Release 列表；没有 Release 时返回空切片。
func fetchGitHubReleases(ctx context.Context, client *http.Client, apiBase, owner, repo string, limit int) ([]ReleaseEntry, error) {
	if limit <= 0 || limit > 30 {
		limit = 10
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=%d", strings.TrimRight(apiBase, "/"), owner, repo, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "diana-webui")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("GitHub API 限流，请稍后再试")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub API HTTP %d", resp.StatusCode)
	}
	var raw []struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		Prerelease  bool      `json:"prerelease"`
		Draft       bool      `json:"draft"`
		PublishedAt time.Time `json:"published_at"`
		HTMLURL     string    `json:"html_url"`
		Assets      []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	entries := make([]ReleaseEntry, 0, len(raw))
	for _, item := range raw {
		if item.Draft {
			continue
		}
		notes := strings.TrimSpace(item.Body)
		if runes := []rune(notes); len(runes) > releaseNotesMaxRunes {
			notes = string(runes[:releaseNotesMaxRunes]) + "…"
		}
		checksumURL := ""
		for _, asset := range item.Assets {
			if asset.Name == "SHA256SUMS" {
				checksumURL = asset.BrowserDownloadURL
				break
			}
		}
		entries = append(entries, ReleaseEntry{
			Tag:               item.TagName,
			Name:              strings.TrimSpace(item.Name),
			Notes:             notes,
			Prerelease:        item.Prerelease,
			Date:              item.PublishedAt,
			URL:               item.HTMLURL,
			ChecksumAvailable: checksumURL != "",
			ChecksumURL:       checksumURL,
		})
	}
	return entries, nil
}

// changelogCache 缓存 GitHub 更新日志响应，避免频繁触碰未鉴权接口的限流。
type changelogCache struct {
	mu        sync.Mutex
	key       string
	payload   map[string]any
	fetchedAt time.Time
}

const changelogCacheTTL = 5 * time.Minute

var githubRemotePattern = regexp.MustCompile(`(?:github\.com[:/])([\w.-]+)/([\w.-]+?)(?:\.git)?$`)

// githubRepoFromRemote 从 git remote URL 提取 GitHub owner/repo。
func githubRepoFromRemote(remoteURL string) (string, string, bool) {
	matches := githubRemotePattern.FindStringSubmatch(strings.TrimSpace(remoteURL))
	if len(matches) != 3 {
		return "", "", false
	}
	return matches[1], matches[2], true
}

// fetchGitHubChangelog 拉取指定分支最近的提交列表；apiBase 供测试注入假服务。
func fetchGitHubChangelog(ctx context.Context, client *http.Client, apiBase, owner, repo, branch string, limit int) ([]ChangelogEntry, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/commits?per_page=%d", strings.TrimRight(apiBase, "/"), owner, repo, limit)
	if branch = strings.TrimSpace(branch); branch != "" {
		endpoint += "&sha=" + branch
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "diana-webui")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("GitHub API 限流，请稍后再试")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub API HTTP %d", resp.StatusCode)
	}
	var raw []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string    `json:"name"`
				Date time.Time `json:"date"`
			} `json:"author"`
		} `json:"commit"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	entries := make([]ChangelogEntry, 0, len(raw))
	for _, item := range raw {
		short := item.SHA
		if len(short) > 7 {
			short = short[:7]
		}
		message := item.Commit.Message
		if idx := strings.IndexByte(message, '\n'); idx >= 0 {
			message = message[:idx]
		}
		entries = append(entries, ChangelogEntry{
			SHA:     item.SHA,
			Short:   short,
			Message: strings.TrimSpace(message),
			Author:  item.Commit.Author.Name,
			Date:    item.Commit.Author.Date,
			URL:     item.HTMLURL,
		})
	}
	return entries, nil
}
