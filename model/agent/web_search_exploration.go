// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SuInk/diana/internal/secretmask"
)

const (
	defaultWebSearchMaxQueries       = 4
	defaultWebSearchMaxProviderCalls = 8
	maximumWebSearchMaxQueries       = 8
	maximumWebSearchMaxProviderCalls = 16
	maximumWebSearchQueryRunes       = 512
	maximumWebSearchDomains          = 4
	// 生产上 Exa p90 约 3 秒、结果页提前收尾后的 Google p90 约 4 秒：等过这条线
	// 还没结果，多半是卡住或会被拦，提前起下一个来源比干等划算。
	webSearchMaxInFlight = 2
)

// webSearchHedgeDelay 是变量只为测试能调短。
var webSearchHedgeDelay = 4 * time.Second

var (
	errWebSearchNoResults = errors.New("web search returned no results")

	webSearchDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	webSearchPrivateHost   = regexp.MustCompile(`(?i)\b(?:localhost|(?:[a-z0-9-]+\.)+(?:internal|local)|10(?:\.[0-9]{1,3}){3}|192\.168(?:\.[0-9]{1,3}){2}|172\.(?:1[6-9]|2[0-9]|3[01])(?:\.[0-9]{1,3}){2})\b`)

	webSearchOperatorPattern = regexp.MustCompile(`(?i)(^|\s)-?(?:site|inurl|intitle|intext|allintitle|allinurl|filetype|ext|repo|cache|related):`)
)

type webSearchQueryCandidate struct {
	Query           string `json:"query"`
	Strategy        string `json:"strategy"`
	Hash            string `json:"hash"`
	Language        string `json:"language"`
	ConstraintCount int    `json:"constraint_count"`
	Status          string `json:"status"`
	Outcome         string `json:"outcome,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type webSearchProviderState struct {
	Provider string `json:"provider"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Outcome  string `json:"outcome,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Attempts int    `json:"attempts"`
}

type webSearchBudget struct {
	MaxQueries       int   `json:"max_queries"`
	QueriesUsed      int   `json:"queries_used"`
	MaxProviderCalls int   `json:"max_provider_calls"`
	ProviderCalls    int   `json:"provider_calls"`
	DeadlineMS       int64 `json:"deadline_ms"`
}

type webSearchResult struct {
	Truncated     bool                      `json:"truncated,omitempty"`
	Results       []WebSearchHit            `json:"results,omitempty"`
	Searches      []webSearchQueryResponse  `json:"searches,omitempty"`
	RetrievedAt   string                    `json:"retrieved_at"`
	SourceStage   string                    `json:"source_stage"`
	SourceNotice  string                    `json:"source_notice,omitempty"`
	Status        string                    `json:"status"`
	StopReason    string                    `json:"stop_reason"`
	Strategy      string                    `json:"strategy"`
	Query         string                    `json:"query"`
	SelectedQuery string                    `json:"selected_query,omitempty"`
	Provider      string                    `json:"provider,omitempty"`
	ProviderType  string                    `json:"provider_type,omitempty"`
	FallbackUsed  bool                      `json:"fallback_used"`
	Queries       []webSearchQueryCandidate `json:"queries"`
	Providers     []webSearchProviderState  `json:"providers"`
	Attempts      []webSearchAttempt        `json:"attempts"`
	Budget        webSearchBudget           `json:"budget"`
	Sources       []string                  `json:"sources,omitempty"`
	Content       string                    `json:"content,omitempty"`
}

func webSearchCandidates(input map[string]any, limit int) ([]webSearchQueryCandidate, error) {
	if limit <= 0 {
		limit = defaultWebSearchMaxQueries
	}
	if limit > maximumWebSearchMaxQueries {
		limit = maximumWebSearchMaxQueries
	}
	var supplied []string
	if query := stringFromInput(input, "query"); query != "" {
		supplied = append(supplied, query)
	}
	switch values := input["queries"].(type) {
	case []string:
		supplied = append(supplied, values...)
	case []any:
		for _, value := range values {
			if query, ok := value.(string); ok {
				supplied = append(supplied, query)
			}
		}
	}
	if len(supplied) == 0 {
		return nil, errors.New("query or queries is required")
	}

	candidates := make([]webSearchQueryCandidate, 0, limit)
	seen := map[string]bool{}
	siteFilter := webSearchSiteFilter(input["domains"])
	appendCandidate := func(query, strategy string) {
		query = sanitizeExternalSearchQuery(normalizeWebSearchQuery(query))
		if query != "" && siteFilter != "" {
			query += " " + siteFilter
		}
		if query == "" || len(candidates) >= limit {
			return
		}
		key := strings.ToLower(query)
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, webSearchQueryCandidate{
			Query:           query,
			Strategy:        strategy,
			Hash:            webSearchQueryHash(query),
			Language:        webSearchQueryLanguage(query),
			ConstraintCount: webSearchConstraintCount(query),
			Status:          "not_executed",
		})
	}
	for index, query := range supplied {
		strategy := "model_alternative"
		if index == 0 {
			strategy = "primary"
		}
		appendCandidate(query, strategy)
	}
	// 线上三轮回放：flash-low 问「Gemini 4」时一大半只搜一次「Gemini 4 release date」，
	// 前排全是发布前的爆料，它就答「还没影」；提示词里要求补一条只写名称的检索词，它
	// 一次都没照做。这里在工具侧补上：改的是发出去的检索词，不评判答案。
	for _, query := range supplied {
		if stripped := stripSpeculativeSearchTerms(query); stripped != "" {
			appendCandidate(stripped, "entity_only")
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("query is empty after privacy filtering")
	}
	return candidates, nil
}

// webSearchSiteFilter 把模型给的 domains 转成搜索引擎通用的 site: 运算符，对应
// Codex web.run 的 domains 过滤。只收裸域名：路径、协议和空格都丢掉，不让这个
// 字段变成夹带任意查询词的旁路。
func webSearchSiteFilter(raw any) string {
	var values []string
	switch v := raw.(type) {
	case []string:
		values = v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				values = append(values, s)
			}
		}
	}
	var sites []string
	seen := map[string]bool{}
	for _, value := range values {
		host := strings.ToLower(strings.TrimSpace(value))
		host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		host, _, _ = strings.Cut(host, "/")
		if !webSearchDomainPattern.MatchString(host) || webSearchPrivateHost.MatchString(host) || seen[host] || len(sites) >= maximumWebSearchDomains {
			continue
		}
		seen[host] = true
		sites = append(sites, "site:"+host)
	}
	return strings.Join(sites, " OR ")
}

// speculativeSearchTerms 是会把结果带向「什么时候出、传闻、爆料」的词。只写名称的
// 检索词更容易排到官方发布页。单独的 release、launch 不算：「Diana release」多半是
// 在找更新日志。
var speculativeSearchTerms = regexp.MustCompile(`(?i)\b(release\s+date|launch\s+date|when\s+will|rumou?rs?|leaks?|leaked|expected|upcoming)\b|发布时间|发布日期|什么时候出|何时发布|何时推出|上线时间|爆料|传闻|泄露|预计`)

// stripSpeculativeSearchTerms 去掉 query 里偏预测的词，返回只剩实体名的检索词；没有
// 这类词、或去掉后什么都不剩时返回空。
func stripSpeculativeSearchTerms(query string) string {
	if !speculativeSearchTerms.MatchString(query) {
		return ""
	}
	stripped := normalizeWebSearchQuery(speculativeSearchTerms.ReplaceAllString(query, " "))
	if len([]rune(stripped)) < 2 {
		return ""
	}
	return stripped
}

func normalizeWebSearchQuery(query string) string {
	query = strings.Map(func(r rune) rune {
		switch r {
		case '\u00a0', '\u2007', '\u202f', '\u3000':
			return ' '
		default:
			return r
		}
	}, query)
	query = strings.Join(strings.Fields(query), " ")
	return truncateRunes(strings.TrimSpace(query), maximumWebSearchQueryRunes)
}

func sanitizeExternalSearchQuery(query string) string {
	for _, pattern := range []*regexp.Regexp{secretmask.PrivateKeyPattern, secretmask.TokenPattern, secretmask.BearerPattern, secretmask.AuthorizationPattern, secretmask.CredentialPattern} {
		query = pattern.ReplaceAllString(query, "[secret omitted]")
	}
	query = webSearchPrivateHost.ReplaceAllString(query, "[internal host omitted]")
	// 邮箱和电话一律脱敏。以前这里先扫 public/official/公开/官网/公告 判断查询是不是
	// 「公开语境」，命中就放行——那是拿关键词猜用户在问什么，而判错的代价是把私人邮箱
	// 或手机号原样发给外部搜索引擎。查公开联系方式本来也不需要把号码当搜索词，
	// 少这一条不影响检索，判错却不可撤回。
	query = secretmask.EmailPattern.ReplaceAllString(query, "[email omitted]")
	query = secretmask.PhonePattern.ReplaceAllString(query, "[phone omitted]")
	return normalizeWebSearchQuery(query)
}

func webSearchQueryHash(query string) string {
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:8])
}

func webSearchQueryLanguage(query string) string {
	var han, latin bool
	for _, r := range query {
		han = han || unicode.Is(unicode.Han, r)
		latin = latin || unicode.In(r, unicode.Latin)
	}
	switch {
	case han && latin:
		return "mixed"
	case han:
		return "zh"
	case latin:
		return "en"
	default:
		return "other"
	}
}

func webSearchConstraintCount(query string) int {
	count := 0
	for _, r := range query {
		switch r {
		case '"', '\'', '“', '”', '‘', '’', ':', '：', '(', ')', '[', ']', '（', '）', '【', '】':
			count++
		}
	}
	return count
}

func webSearchResultSources(content string) []string {
	seen := map[string]bool{}
	var sources []string
	for _, raw := range webSearchURLPattern.FindAllString(content, -1) {
		raw = strings.TrimRight(raw, `.,;:!?)]}'"，。；：！？）】`)
		key, normalized := canonicalWebSearchURL(raw)
		if raw == "" || seen[key] {
			continue
		}
		seen[key] = true
		sources = append(sources, normalized)
	}
	return sources
}

func canonicalWebSearchURL(raw string) (string, string) {
	raw = unwrapSearchEngineLink(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return strings.ToLower(raw), raw
	}
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Host = strings.ToLower(parsed.Host)
	normalized := parsed.String()
	return strings.ToLower(normalized), normalized
}

func classifyWebSearchError(err error, providerCtxErr, runCtxErr error) string {
	switch {
	case errors.Is(err, errWebSearchNoResults):
		return "no_results"
	case errors.Is(runCtxErr, context.DeadlineExceeded), errors.Is(providerCtxErr, context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "provider_error"
	}
}

func webSearchQueryLength(query string) int {
	return utf8.RuneCountInString(query)
}
