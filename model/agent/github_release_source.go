package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

// Only public GitHub Release URLs use this read-only source. No repository
// credentials, private-repository access, or search-engine index is involved.
func githubReleaseAPIURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil || u.RawQuery != "" {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] != "releases" {
		return "", false
	}
	for _, part := range parts {
		if part == "." || part == ".." || part == "" {
			return "", false
		}
	}
	base := "https://api.github.com/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/releases/"
	if len(parts) == 3 || (len(parts) == 4 && parts[3] == "latest") {
		return base + "latest", true
	}
	if len(parts) >= 5 && parts[3] == "tag" {
		return base + "tags/" + url.PathEscape(strings.Join(parts[4:], "/")), true
	}
	return "", false
}

type githubReleaseRecord struct {
	Tag         string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
}

func fetchGitHubRelease(ctx context.Context, client *http.Client, requested, apiURL string, maxChars int) (RenderedPage, error) {
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return RenderedPage{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("User-Agent", "Diana-release-reader")
	resp, err := client.Do(req)
	if err != nil {
		return RenderedPage{}, fmt.Errorf("GitHub Release API unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return RenderedPage{}, fmt.Errorf("GitHub Release API HTTP %d; this does not establish whether a release exists", resp.StatusCode)
	}
	const limit = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return RenderedPage{}, err
	}
	if len(raw) > limit {
		return RenderedPage{}, fmt.Errorf("GitHub Release API response exceeds limit")
	}
	var release githubReleaseRecord
	if err = json.Unmarshal(raw, &release); err != nil {
		return RenderedPage{}, fmt.Errorf("invalid GitHub Release response: %w", err)
	}
	latest := strings.HasSuffix(apiURL, "/latest")
	if release.Draft || (latest && release.Prerelease) || strings.TrimSpace(release.Tag) == "" || release.PublishedAt == "" {
		return RenderedPage{}, fmt.Errorf("GitHub Release API returned incomplete or ineligible release data")
	}
	published, err := time.Parse(time.RFC3339, release.PublishedAt)
	if err != nil {
		return RenderedPage{}, fmt.Errorf("invalid GitHub Release publication date")
	}
	actualAPI, valid := githubReleaseAPIURL(release.HTMLURL)
	// Accept only the exact repository/tag returned by the official endpoint.
	prefix := strings.Split(apiURL, "/releases/")[0] + "/releases/tags/"
	if !valid || actualAPI != prefix+url.PathEscape(release.Tag) || (!latest && actualAPI != apiURL) {
		return RenderedPage{}, fmt.Errorf("GitHub Release API returned an unexpected release URL")
	}
	heading := "已核验指定发布"
	notice := "内容来自本次读取的 GitHub 官方 Release API，不是搜索快照或浏览器 DOM。查询时间不等于发布时间；指定标签存在不代表它是最新版本。"
	if latest {
		heading = "GitHub latest 指定的最新正式发布"
		notice = "内容来自本次读取的 GitHub 官方 latest Release API，不是搜索快照或浏览器 DOM。仅覆盖最新正式发布，不包含全部历史或预发布版本；查询时间不等于发布时间。"
	}
	text := fmt.Sprintf("%s\n版本：%s\n名称：%s\n发布时间：%s\n预发布：%t\n发布页面：%s\n官方 API：%s\n\n%s", heading, release.Tag, release.Name, published.UTC().Format(time.RFC3339), release.Prerelease, release.HTMLURL, apiURL, release.Body)
	if maxChars <= 0 {
		maxChars = defaultHeadlessBrowserTextChars
	}
	truncated := len([]rune(text)) > maxChars
	return RenderedPage{RequestedURL: requested, URL: release.HTMLURL, Title: firstNonEmptyString(release.Name, release.Tag), Text: truncateRunes(text, maxChars), Truncated: truncated, SourceType: "github_release_api", SourceURLs: []string{apiURL, release.HTMLURL}, SourceNotice: notice, RetrievedAt: time.Now().UTC().Format(time.RFC3339), Stable: true, StabilityReason: "official_api_snapshot", WaitedMS: time.Since(started).Milliseconds()}, nil
}

func readGitHubRelease(ctx context.Context, requested, apiURL string, maxChars int) (RenderedPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := netguard.ValidatePublicURLStrict(ctx, apiURL); err != nil {
		return RenderedPage{}, err
	}
	client := netguard.NewPublicHTTPClient(6 * time.Second)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Host, "api.github.com") {
			return fmt.Errorf("unexpected GitHub API redirect")
		}
		return netguard.ValidatePublicURLStrict(req.Context(), req.URL.String())
	}
	return fetchGitHubRelease(ctx, client, requested, apiURL, maxChars)
}
