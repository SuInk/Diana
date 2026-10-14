// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	githubAPIBase         = "https://api.github.com"
	githubBodyMaxRunes    = 600
	githubLabelsMaxCount  = 6
	githubTopicsMaxCount  = 6
	githubDescriptionRuns = 300
)

type githubTargetKind int

const (
	githubTargetRepo githubTargetKind = iota + 1
	githubTargetIssue
	githubTargetPull
)

type githubTarget struct {
	Kind   githubTargetKind
	Owner  string
	Repo   string
	Number int
}

// githubReservedOwners 是 github.com 根路径下不属于用户/组织的页面，
// 例如 github.com/settings/profile 不能当成 settings 用户的 profile 仓库。
var githubReservedOwners = map[string]bool{
	"about": true, "apps": true, "collections": true, "contact": true, "enterprise": true,
	"events": true, "explore": true, "features": true, "issues": true, "login": true,
	"marketplace": true, "new": true, "notifications": true, "orgs": true, "pricing": true,
	"pulls": true, "search": true, "security": true, "settings": true, "sponsors": true,
	"topics": true, "trending": true, "users": true,
}

var githubNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func isGitHubURL(raw string) bool {
	return urlMatchesDomain(raw, "github.com")
}

// parseGitHubURL 把 github.com 链接归类为仓库、Issue 或 PR；用户主页、
// 组织页和站内功能页返回 false，由通用标题抓取接手。
func parseGitHubURL(raw string) (githubTarget, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !hostMatchesDomain(strings.ToLower(parsed.Hostname()), "github.com") {
		return githubTarget{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || githubReservedOwners[strings.ToLower(parts[0])] {
		return githubTarget{}, false
	}
	owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
	if !githubNamePattern.MatchString(owner) || !githubNamePattern.MatchString(repo) {
		return githubTarget{}, false
	}
	target := githubTarget{Kind: githubTargetRepo, Owner: owner, Repo: repo}
	if len(parts) >= 4 && (parts[2] == "issues" || parts[2] == "pull") {
		if number, err := strconv.Atoi(parts[3]); err == nil && number > 0 {
			target.Number = number
			target.Kind = githubTargetIssue
			if parts[2] == "pull" {
				target.Kind = githubTargetPull
			}
		}
	}
	return target, true
}

func (t githubTarget) resourceKey() string {
	key := "github:" + strings.ToLower(t.Owner+"/"+t.Repo)
	switch t.Kind {
	case githubTargetIssue:
		return key + "#issue-" + strconv.Itoa(t.Number)
	case githubTargetPull:
		return key + "#pull-" + strconv.Itoa(t.Number)
	}
	return key
}

type githubUser struct {
	Login string `json:"login"`
}

type githubRepoInfo struct {
	FullName        string   `json:"full_name"`
	Description     string   `json:"description"`
	Language        string   `json:"language"`
	StargazersCount int      `json:"stargazers_count"`
	ForksCount      int      `json:"forks_count"`
	OpenIssuesCount int      `json:"open_issues_count"`
	Archived        bool     `json:"archived"`
	Fork            bool     `json:"fork"`
	PushedAt        string   `json:"pushed_at"`
	Topics          []string `json:"topics"`
	License         *struct {
		SPDXID string `json:"spdx_id"`
		Name   string `json:"name"`
	} `json:"license"`
}

type githubIssueInfo struct {
	Number      int        `json:"number"`
	Title       string     `json:"title"`
	State       string     `json:"state"`
	StateReason string     `json:"state_reason"`
	Body        string     `json:"body"`
	Comments    int        `json:"comments"`
	User        githubUser `json:"user"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
	CreatedAt string `json:"created_at"`
}

type githubPullInfo struct {
	githubIssueInfo
	Merged       bool `json:"merged"`
	Draft        bool `json:"draft"`
	Additions    int  `json:"additions"`
	Deletions    int  `json:"deletions"`
	ChangedFiles int  `json:"changed_files"`
	Head         struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func githubAPIHeaders() map[string]string {
	return map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"User-Agent":           resolverUserAgent,
	}
}

func (p *ResolverPlugin) resolveGitHubMedia(ctx context.Context, req PluginRequest, raw string) resolverSocialResult {
	result := resolverSocialResult{Handled: true}
	target, ok := parseGitHubURL(raw)
	if !ok {
		// 用户主页、组织页之类不归这里管，交回通用标题抓取。
		return resolverSocialResult{}
	}
	text, ok := fetchGitHubText(ctx, target)
	if !ok {
		result.Failed = true
		result.Context = fmt.Sprintf("[GitHub] 读取 %s/%s 失败：仓库不存在、是私有的，或匿名访问触发了 API 限流。", target.Owner, target.Repo)
		recordResolverMediaLog(ctx, req, raw, "github", false, "github api unavailable")
		return result
	}
	result.Context = text
	result.ResourceKeys = []string{target.resourceKey()}
	recordResolverMediaLog(ctx, req, raw, "github", true, "")
	return result
}

func fetchGitHubText(ctx context.Context, target githubTarget) (string, bool) {
	base := fmt.Sprintf("%s/repos/%s/%s", githubAPIBase, url.PathEscape(target.Owner), url.PathEscape(target.Repo))
	headers := githubAPIHeaders()
	switch target.Kind {
	case githubTargetIssue:
		var issue githubIssueInfo
		if !fetchResolverJSON(ctx, fmt.Sprintf("%s/issues/%d", base, target.Number), headers, &issue) || issue.Title == "" {
			return "", false
		}
		return formatGitHubIssue(target, issue), true
	case githubTargetPull:
		var pull githubPullInfo
		if !fetchResolverJSON(ctx, fmt.Sprintf("%s/pulls/%d", base, target.Number), headers, &pull) || pull.Title == "" {
			return "", false
		}
		return formatGitHubPull(target, pull), true
	}
	var repo githubRepoInfo
	if !fetchResolverJSON(ctx, base, headers, &repo) || repo.FullName == "" {
		return "", false
	}
	return formatGitHubRepo(repo), true
}

func formatGitHubRepo(repo githubRepoInfo) string {
	var lines []string
	title := "[GitHub] " + repo.FullName
	if repo.Archived {
		title += "（已归档）"
	}
	if repo.Fork {
		title += "（Fork）"
	}
	lines = append(lines, title)
	if description := strings.TrimSpace(repo.Description); description != "" {
		lines = append(lines, truncateRunes(description, githubDescriptionRuns))
	}
	stats := []string{fmt.Sprintf("★ %d", repo.StargazersCount), fmt.Sprintf("Fork %d", repo.ForksCount), fmt.Sprintf("开放 Issue/PR %d", repo.OpenIssuesCount)}
	if repo.Language != "" {
		stats = append(stats, repo.Language)
	}
	if repo.License != nil {
		stats = append(stats, firstNonEmpty(repo.License.SPDXID, repo.License.Name))
	}
	lines = append(lines, strings.Join(stats, " · "))
	if topics := limitStrings(repo.Topics, githubTopicsMaxCount); len(topics) > 0 {
		lines = append(lines, "话题："+strings.Join(topics, "、"))
	}
	if day := githubDay(repo.PushedAt); day != "" {
		lines = append(lines, "最近推送："+day)
	}
	return strings.Join(lines, "\n")
}

func formatGitHubIssue(target githubTarget, issue githubIssueInfo) string {
	state := "开放"
	if issue.State == "closed" {
		state = "已关闭"
		if issue.StateReason == "not_planned" {
			state = "已关闭（不计划处理）"
		}
	}
	return githubThreadText("Issue", target, issue, state, "")
}

func formatGitHubPull(target githubTarget, pull githubPullInfo) string {
	state := "开放"
	switch {
	case pull.Merged:
		state = "已合并"
	case pull.State == "closed":
		state = "已关闭"
	case pull.Draft:
		state = "草稿"
	}
	extra := fmt.Sprintf("%s → %s · %d 个文件 +%d −%d", pull.Head.Ref, pull.Base.Ref, pull.ChangedFiles, pull.Additions, pull.Deletions)
	return githubThreadText("PR", target, pull.githubIssueInfo, state, extra)
}

func githubThreadText(label string, target githubTarget, issue githubIssueInfo, state, extra string) string {
	lines := []string{fmt.Sprintf("[GitHub %s] %s/%s#%d %s", label, target.Owner, target.Repo, target.Number, strings.TrimSpace(issue.Title))}
	meta := []string{state}
	if issue.User.Login != "" {
		meta = append(meta, "作者 "+issue.User.Login)
	}
	meta = append(meta, fmt.Sprintf("%d 条评论", issue.Comments))
	if day := githubDay(issue.CreatedAt); day != "" {
		meta = append(meta, day)
	}
	lines = append(lines, strings.Join(meta, " · "))
	if extra != "" {
		lines = append(lines, extra)
	}
	var labels []string
	for _, item := range issue.Labels {
		if name := strings.TrimSpace(item.Name); name != "" {
			labels = append(labels, name)
		}
	}
	if labels = limitStrings(labels, githubLabelsMaxCount); len(labels) > 0 {
		lines = append(lines, "标签："+strings.Join(labels, "、"))
	}
	if body := strings.TrimSpace(issue.Body); body != "" {
		lines = append(lines, truncateRunes(compactWhitespace(body), githubBodyMaxRunes))
	}
	return strings.Join(lines, "\n")
}

// githubDay 取 RFC3339 时间戳的日期部分。
func githubDay(timestamp string) string {
	if len(timestamp) >= 10 {
		return timestamp[:10]
	}
	return ""
}
