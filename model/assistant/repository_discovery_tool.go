// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// 仓库发现：github 工具里 repo_search 与 repo 两个只读操作。
//
// 给用户推荐仓库和查 Issue 不是一回事。Issue 有编号，对不对一目了然；「哪个库
// 好用」没有这种锚点，只能看它有多少人在用、有多少人拿去改、还有没有人维护。
// 模型手里只剩名字和简介时，会把三年没人动的玩具项目和被几万人用着的库说得
// 一样可靠。所以这两个操作返回的重点不是简介，而是 star 数、fork 数和最近一次
// 推送时间，外加久未更新与已归档两个标记。

const (
	repositoryDiscoveryDefaultLimit = 5
	repositoryDiscoveryMaxLimit     = 10
	repositoryDiscoveryMaxTopics    = 8
	repositoryDiscoveryQueryLimit   = 200
	// repositoryDiscoveryStaleAfter 是「久未更新」的门槛。一年没有任何推送的仓库
	// 不一定不能用，但推荐时必须说出来，不能让用户自己点进仓库页才发现。
	repositoryDiscoveryStaleAfter = 365 * 24 * time.Hour
)

// 语言限定符由后端拼装，所以只放行单个语言名。C++、C#、Objective-C 这类写法要能过，
// 空格一律不收——真需要带空格的语言名，用关键词搜就够了。
var repositoryDiscoveryLanguagePattern = regexp.MustCompile(`^[A-Za-z0-9+#.\-]{1,32}$`)

// repositoryProfileView 是一个仓库的影响力画像。
type repositoryProfileView struct {
	Repository  string   `json:"repository"`
	URL         string   `json:"url"`
	Description string   `json:"description,omitempty"`
	Language    string   `json:"language,omitempty"`
	Topics      []string `json:"topics,omitempty"`
	License     string   `json:"license,omitempty"`
	Stars       int      `json:"stars"`
	Forks       int      `json:"forks"`
	OpenIssues  int      `json:"open_issues"`
	// PushedAt 是最近一次有代码推上去的时间，PushedAgo 是它距今多久的中文说法。
	// 只给 ISO 时间的话模型得自己算日期差，算错就会把停更两年的仓库说成「最近更新」。
	PushedAt  time.Time `json:"pushed_at,omitempty"`
	PushedAgo string    `json:"pushed_ago,omitempty"`
	Stale     bool      `json:"stale,omitempty"`
	Archived  bool      `json:"archived,omitempty"`
	// Fork 为 true 表示这个仓库自己就是别人的分叉，推荐前先确认上游是不是更合适。
	Fork bool `json:"fork,omitempty"`
}

type githubRepositoryProfile struct {
	FullName    string   `json:"full_name"`
	HTMLURL     string   `json:"html_url"`
	Description string   `json:"description"`
	Language    string   `json:"language"`
	Topics      []string `json:"topics"`
	License     *struct {
		SPDXID string `json:"spdx_id"`
	} `json:"license"`
	StargazersCount int       `json:"stargazers_count"`
	ForksCount      int       `json:"forks_count"`
	OpenIssuesCount int       `json:"open_issues_count"`
	PushedAt        time.Time `json:"pushed_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Archived        bool      `json:"archived"`
	Fork            bool      `json:"fork"`
	Private         bool      `json:"private"`
}

// 故意不取 watchers_count：GitHub 在这个字段上回填的是 star 数，不是订阅人数，
// 一起返回只会让模型以为拿到了第三个独立指标。真正的订阅数是 subscribers_count，
// 而仓库搜索接口根本不给，两个操作的字段就没法对齐。

func repositoryDiscoverySortParam(raw string) (string, string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "best_match", "relevance":
		return "", "相关度", true
	case "stars", "star":
		return "stars", "star 数", true
	case "forks", "fork":
		return "forks", "fork 数", true
	case "updated", "recent", "pushed":
		return "updated", "最近更新", true
	}
	return "", "", false
}

type repositoryDiscoveryInput struct {
	query    string
	language string
	sort     string
	sortName string
	limit    int
}

// parseRepositoryDiscoveryInput 解析并本地校验 repo_search 的参数。和 parseSearchInput
// 一样不碰网络：限定符注入必须零请求被拒。
func parseRepositoryDiscoveryInput(input map[string]any) (repositoryDiscoveryInput, int, string, string) {
	parsed := repositoryDiscoveryInput{limit: repositoryDiscoveryDefaultLimit}
	query, redactions := sanitizeRepositoryIssueText(configToolString(input, "query"), repositoryDiscoveryQueryLimit, true)
	if query == "" {
		return parsed, redactions, "invalid_input", "repo_search 必须提供 query。"
	}
	if repositoryIssueSearchQualifierPattern.MatchString(query) || repositoryIssueSearchBooleanPattern.MatchString(query) || strings.ContainsAny(query, "\"`") {
		return parsed, redactions, "invalid_input", "query 只能包含普通关键词。按语言筛选用 language，换排序用 sort，不要往关键词里注入限定符、布尔操作或引号。"
	}
	parsed.query = query

	language := strings.TrimSpace(configToolString(input, "language"))
	if language != "" && !repositoryDiscoveryLanguagePattern.MatchString(language) {
		return parsed, redactions, "invalid_input", "language 只能是单个语言名，例如 go、rust、typescript、c++。"
	}
	parsed.language = language

	sortParam, sortName, ok := repositoryDiscoverySortParam(configToolString(input, "sort"))
	if !ok {
		return parsed, redactions, "invalid_input", "sort 必须是 best_match、stars、forks 或 updated。"
	}
	parsed.sort, parsed.sortName = sortParam, sortName

	if value, present := numberValue(input["limit"]); present {
		if value != float64(int(value)) || int(value) < 1 || int(value) > repositoryDiscoveryMaxLimit {
			return parsed, redactions, "invalid_input", "limit 必须是 1 到 " + itoa(repositoryDiscoveryMaxLimit) + " 之间的整数。"
		}
		parsed.limit = int(value)
	}
	return parsed, redactions, "", ""
}

// searchRepositories 按关键词找公开仓库，返回按影响力可判断的画像列表。
func (t *dianaGitHubTool) searchRepositories(ctx context.Context, input map[string]any) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "repo_search"}
	parsed, redactions, code, message := parseRepositoryDiscoveryInput(input)
	result.Redactions = redactions
	if code != "" {
		return result.fail(code, message)
	}
	// is:public 是硬边界。仓库搜索走的是公共凭据，带上 Token 时 GitHub 会把这个
	// Token 看得见的私有仓库一并搜出来——那是主人的私仓，不该因为群里有人说了句
	// 关键词就露名字。返回结果再按 private 过一遍，两道都不省。
	searchQuery := parsed.query + " is:public"
	if parsed.language != "" {
		searchQuery += " language:" + parsed.language
	}
	values := url.Values{"q": {searchQuery}, "per_page": {itoa(parsed.limit)}}
	if parsed.sort != "" {
		values.Set("sort", parsed.sort)
		values.Set("order", "desc")
	}
	var payload struct {
		Items []githubRepositoryProfile `json:"items"`
	}
	if apiErr := t.doJSON(ctx, http.MethodGet, "/search/repositories?"+values.Encode(), nil, &payload); apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	now := time.Now()
	items := make([]repositoryProfileView, 0, len(payload.Items))
	for _, item := range payload.Items {
		if item.Private || !validRepositoryProfileResponse(item) {
			continue
		}
		items = append(items, repositoryProfileFromGitHub(item, now))
		if len(items) >= parsed.limit {
			break
		}
	}
	result.OK = true
	result.Outcome = "searched"
	result.Repositories = items
	result.Message = fmt.Sprintf("按%s排序找到 %d 个公开仓库。%s", parsed.sortName, len(items), repositoryDiscoveryReadingHint)
	return result
}

// repositoryProfile 读单个仓库的画像。用户已经点名某个仓库时用它，不必先搜一遍。
func (t *dianaGitHubTool) repositoryProfile(ctx context.Context, repository string) repositoryIssueResult {
	result := repositoryIssueResult{Operation: "repo", Repository: repository}
	var profile githubRepositoryProfile
	if apiErr := t.doJSON(ctx, http.MethodGet, "/repos/"+repository, nil, &profile); apiErr != nil {
		return result.fail(apiErr.Code, t.failureMessage(apiErr.Code))
	}
	if !validRepositoryProfileResponse(profile) {
		return result.fail("invalid_response", "GitHub 返回的仓库信息与仓库地址对不上，结果已拒绝。")
	}
	view := repositoryProfileFromGitHub(profile, time.Now())
	result.OK = true
	result.Outcome = "fetched"
	result.RepositoryProfile = &view
	result.Message = fmt.Sprintf("%s：%d star、%d fork，最近一次推送在 %s。%s",
		view.Repository, view.Stars, view.Forks, repositoryDiscoveryPushedText(view), repositoryDiscoveryReadingHint)
	return result
}

// repositoryDiscoveryReadingHint 跟着每条结果回去，提醒模型这些数字是用来说给用户听的，
// 不是查完就扔的中间态。
const repositoryDiscoveryReadingHint = "推荐仓库时把 stars、forks 和 pushed_ago 一起写给用户：" +
	"star 说明多少人在用，fork 说明多少人真拿去改，pushed_ago 说明还有没有人维护；" +
	"stale 或 archived 为 true 时必须明说它已经长期没有更新，不要当作可用推荐；" +
	"fork 为 true 说明它本身是别人的分叉，推荐前先看看上游是不是更合适。"

func repositoryDiscoveryPushedText(view repositoryProfileView) string {
	if view.PushedAgo == "" {
		return "未知时间"
	}
	return view.PushedAgo
}

// validRepositoryProfileResponse 拒绝 full_name 和 html_url 对不上的返回，口径和
// Issue 那边的 validRepositoryIssueCanonicalURL 一致：链接是要发给用户点的，
// 不能把搜索结果里夹带的任意地址原样递出去。
func validRepositoryProfileResponse(profile githubRepositoryProfile) bool {
	fullName, err := normalizeGitHubRepository(strings.TrimSpace(profile.FullName))
	if err != nil {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(profile.HTMLURL))
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		!strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "github.com") ||
		!strings.EqualFold(strings.Trim(parsed.Path, "/"), fullName) {
		return false
	}
	return true
}

func repositoryProfileFromGitHub(profile githubRepositoryProfile, now time.Time) repositoryProfileView {
	view := repositoryProfileView{
		Repository:  strings.TrimSpace(profile.FullName),
		URL:         strings.TrimSpace(profile.HTMLURL),
		Description: strings.TrimSpace(profile.Description),
		Language:    strings.TrimSpace(profile.Language),
		Stars:       profile.StargazersCount,
		Forks:       profile.ForksCount,
		OpenIssues:  profile.OpenIssuesCount,
		Archived:    profile.Archived,
		Fork:        profile.Fork,
	}
	if profile.License != nil {
		view.License = strings.TrimSpace(profile.License.SPDXID)
	}
	for _, topic := range profile.Topics {
		if topic = strings.TrimSpace(topic); topic == "" {
			continue
		}
		if len(view.Topics) >= repositoryDiscoveryMaxTopics {
			break
		}
		view.Topics = append(view.Topics, topic)
	}
	// 「最近更新」取 pushed_at 而不是 updated_at：后者改个简介、加个 topic 就会往前跳，
	// 拿它判断维护状态会把早就停更的仓库说成活的。只有 pushed_at 缺失时才回落。
	pushed := profile.PushedAt
	if pushed.IsZero() {
		pushed = profile.UpdatedAt
	}
	if !pushed.IsZero() {
		view.PushedAt = pushed.UTC()
		view.PushedAgo = humanizeChineseDuration(now.Sub(pushed)) + "前"
		view.Stale = now.Sub(pushed) >= repositoryDiscoveryStaleAfter
	}
	return view
}
