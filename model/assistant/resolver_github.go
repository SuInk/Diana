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
	githubAPIBase        = "https://api.github.com"
	githubBodyMaxRunes   = 600
	githubLabelsMaxCount = 6
	githubTopicsMaxCount = 8
	// 参考实现最多带 3 条评论，每条 120 字。
	githubCommentsMaxCount = 3
	githubCommentMaxRunes  = 120
	githubDescriptionRuns  = 300
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
	FullName         string   `json:"full_name"`
	HTMLURL          string   `json:"html_url"`
	Homepage         string   `json:"homepage"`
	Description      string   `json:"description"`
	Language         string   `json:"language"`
	StargazersCount  int      `json:"stargazers_count"`
	ForksCount       int      `json:"forks_count"`
	OpenIssuesCount  int      `json:"open_issues_count"`
	SubscribersCount int      `json:"subscribers_count"`
	Archived         bool     `json:"archived"`
	Fork             bool     `json:"fork"`
	PushedAt         string   `json:"pushed_at"`
	Topics           []string `json:"topics"`
	License          *struct {
		SPDXID string `json:"spdx_id"`
		Name   string `json:"name"`
	} `json:"license"`
}

type githubIssueInfo struct {
	Number      int        `json:"number"`
	HTMLURL     string     `json:"html_url"`
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

type githubComment struct {
	Body string     `json:"body"`
	User githubUser `json:"user"`
}

type githubPullInfo struct {
	githubIssueInfo
	Merged       bool `json:"merged"`
	Draft        bool `json:"draft"`
	Commits      int  `json:"commits"`
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
		"User-Agent":           resolverUserAgent(),
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
	result.Context = githubContextLabel + text.Display
	if text.Body != "" {
		// 群里看到的卡片不贴正文，模型跟评时仍需要知道 Issue/PR 在说什么。
		result.Context += "\n正文：" + truncateRunes(compactWhitespace(text.Body), githubBodyMaxRunes)
	}
	result.ForwardMessages = []OutgoingMessage{{Text: text.Display}}
	result.ResourceKeys = []string{target.resourceKey()}
	recordResolverMediaLog(ctx, req, raw, "github", true, "")
	return result
}

// githubText 是一次 GitHub 解析的结果：Display 发到群里，Body 只进模型上下文。
type githubText struct {
	Display string
	Body    string
}

func fetchGitHubText(ctx context.Context, target githubTarget) (githubText, bool) {
	base := fmt.Sprintf("%s/repos/%s/%s", githubAPIBase, url.PathEscape(target.Owner), url.PathEscape(target.Repo))
	headers := githubAPIHeaders()
	switch target.Kind {
	case githubTargetIssue, githubTargetPull:
		var comments []githubComment
		fetchComments := func() []githubComment {
			// 评论是锦上添花，取不到（限流、没有评论）就不显示。
			if !fetchResolverJSON(ctx, fmt.Sprintf("%s/issues/%d/comments?per_page=%d", base, target.Number, githubCommentsMaxCount), headers, &comments) {
				return nil
			}
			return comments
		}
		if target.Kind == githubTargetIssue {
			var issue githubIssueInfo
			if !fetchResolverJSON(ctx, fmt.Sprintf("%s/issues/%d", base, target.Number), headers, &issue) || issue.Title == "" {
				return githubText{}, false
			}
			if issue.Comments > 0 {
				comments = fetchComments()
			}
			return githubText{Display: formatGitHubIssue(target, issue, comments), Body: issue.Body}, true
		}
		var pull githubPullInfo
		if !fetchResolverJSON(ctx, fmt.Sprintf("%s/pulls/%d", base, target.Number), headers, &pull) || pull.Title == "" {
			return githubText{}, false
		}
		if pull.Comments > 0 {
			comments = fetchComments()
		}
		return githubText{Display: formatGitHubPull(target, pull, comments), Body: pull.Body}, true
	}
	var repo githubRepoInfo
	if !fetchResolverJSON(ctx, base, headers, &repo) || repo.FullName == "" {
		return githubText{}, false
	}
	return githubText{Display: formatGitHubRepo(repo)}, true
}

// GitHub 的文字排版照搬 ErisPulse-GitHubParser（wsu2059q/ErisPulse-GitHubParser）
// 的 TextTemplates：首行标题，一行 · 分隔的统计，一行 | 分隔的信息，最后附链接。
// 来源标签 [GitHub] 只放在给模型的上下文里。
const githubContextLabel = "[GitHub] "

func formatGitHubRepo(repo githubRepoInfo) string {
	lines := []string{repo.FullName}
	description := strings.TrimSpace(repo.Description)
	if description == "" {
		description = "暂无描述"
	}
	lines = append(lines, truncateRunes(description, githubDescriptionRuns))
	lines = append(lines, fmt.Sprintf("★ Stars %s · ⑂ Forks %s · Watchers %s · Issues %s",
		githubCount(repo.StargazersCount), githubCount(repo.ForksCount), githubCount(repo.SubscribersCount), githubCount(repo.OpenIssuesCount)))
	language := firstNonEmpty(strings.TrimSpace(repo.Language), "未指定")
	license := "无"
	if repo.License != nil {
		license = firstNonEmpty(strings.TrimSpace(repo.License.SPDXID), strings.TrimSpace(repo.License.Name), license)
	}
	info := []string{"语言: " + language, "许可证: " + license}
	if homepage := strings.TrimSpace(repo.Homepage); homepage != "" {
		info = append(info, "主页: "+homepage)
	}
	info = append(info, "最近推送: "+firstNonEmpty(githubDay(repo.PushedAt), "未知"))
	lines = append(lines, strings.Join(info, " | "))
	if topics := limitStrings(repo.Topics, githubTopicsMaxCount); len(topics) > 0 {
		lines = append(lines, strings.Join(topics, ", "))
	}
	if repo.HTMLURL != "" {
		lines = append(lines, repo.HTMLURL)
	}
	return strings.Join(lines, "\n")
}

func formatGitHubIssue(target githubTarget, issue githubIssueInfo, comments []githubComment) string {
	state := "开启"
	if issue.State == "closed" {
		state = "已关闭"
	}
	return githubThreadText(target, issue, state, "", comments)
}

func formatGitHubPull(target githubTarget, pull githubPullInfo, comments []githubComment) string {
	state := "开启"
	switch {
	case pull.Merged:
		state = "已合并"
	case pull.State == "closed":
		state = "已关闭"
	}
	extra := fmt.Sprintf(" · 提交: %d · +%d/-%d · %d 文件", pull.Commits, pull.Additions, pull.Deletions, pull.ChangedFiles)
	return githubThreadText(target, pull.githubIssueInfo, state, extra, comments)
}

func githubThreadText(target githubTarget, issue githubIssueInfo, state, extra string, comments []githubComment) string {
	lines := []string{
		fmt.Sprintf("%s/%s #%d [%s] %s", target.Owner, target.Repo, target.Number, state, strings.TrimSpace(issue.Title)),
		fmt.Sprintf("作者: %s · 评论: %s", issue.User.Login, githubCount(issue.Comments)) + extra,
	}
	var labels []string
	for _, item := range issue.Labels {
		if name := strings.TrimSpace(item.Name); name != "" {
			labels = append(labels, name)
		}
	}
	if labels = limitStrings(labels, githubLabelsMaxCount); len(labels) > 0 {
		lines = append(lines, strings.Join(labels, ", "))
	}
	for index, comment := range comments {
		if index >= githubCommentsMaxCount {
			break
		}
		// 照参考实现只去掉首尾空白，评论里的换行原样保留。
		if body := strings.TrimSpace(strings.ReplaceAll(comment.Body, "\r\n", "\n")); body != "" {
			lines = append(lines, fmt.Sprintf("  %s: %s", comment.User.Login, githubTruncate(body, githubCommentMaxRunes)))
		}
	}
	if issue.HTMLURL != "" {
		lines = append(lines, issue.HTMLURL)
	}
	return strings.Join(lines, "\n")
}

// githubCount 照参考实现的 _fc：一万以上写成 x.xw，一千以上写成 x.xk。
func githubCount(value int) string {
	switch {
	case value >= 10000:
		return fmt.Sprintf("%.1fw", float64(value)/10000)
	case value >= 1000:
		return fmt.Sprintf("%.1fk", float64(value)/1000)
	}
	return fmt.Sprintf("%d", value)
}

// githubTruncate 照参考实现的 _trunc：超过上限时保留 limit-1 个字再加省略号。
func githubTruncate(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-1]) + "…"
}

// githubDay 取 RFC3339 时间戳的日期部分。
func githubDay(timestamp string) string {
	if len(timestamp) >= 10 {
		return timestamp[:10]
	}
	return ""
}
