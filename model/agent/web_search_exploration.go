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
	"unicode"
	"unicode/utf8"
)

const (
	defaultWebSearchMaxQueries       = 4
	defaultWebSearchMaxProviderCalls = 8
	maximumWebSearchMaxQueries       = 8
	maximumWebSearchMaxProviderCalls = 16
	maximumWebSearchQueryRunes       = 512
)

var (
	errWebSearchNoResults = errors.New("web search returned no results")

	webSearchSecretPattern = regexp.MustCompile(`(?i)(?:\bsk-[a-z0-9_-]{12,}\b|\b(?:api[_-]?key|access[_-]?token|token|password)\s*[:=]\s*\S+)`)
	webSearchEmailPattern  = regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b`)
	webSearchPhonePattern  = regexp.MustCompile(`\b(?:\+?86[- ]?)?1[3-9][0-9]{9}\b`)
	webSearchPrivateHost   = regexp.MustCompile(`(?i)\b(?:localhost|(?:[a-z0-9-]+\.)+(?:internal|local)|10(?:\.[0-9]{1,3}){3}|192\.168(?:\.[0-9]{1,3}){2}|172\.(?:1[6-9]|2[0-9]|3[01])(?:\.[0-9]{1,3}){2})\b`)
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
	appendCandidate := func(query, strategy string) {
		query = sanitizeExternalSearchQuery(normalizeWebSearchQuery(query))
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
	for _, query := range append([]string(nil), supplied...) {
		appendCandidate(relaxWebSearchQuotes(query), "quotes_relaxed")
		appendCandidate(normalizeWebSearchSeparators(query), "separators_normalized")
		appendCandidate(relaxWebSearchParentheticalConstraints(normalizeWebSearchSeparators(relaxWebSearchQuotes(query))), "constraints_relaxed")
	}
	if len(candidates) == 0 {
		return nil, errors.New("query is empty after privacy filtering")
	}
	return candidates, nil
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

func relaxWebSearchQuotes(query string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '\'', '`', '“', '”', '‘', '’', '「', '」', '『', '』':
			return ' '
		default:
			return r
		}
	}, query)
}

func normalizeWebSearchSeparators(query string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '—', '–', '－', '，', '、', '；', ';', '|':
			return ' '
		default:
			return r
		}
	}, query)
}

func relaxWebSearchParentheticalConstraints(query string) string {
	var builder strings.Builder
	depth := 0
	for _, r := range query {
		switch r {
		case '(', '[', '{', '（', '【', '〔':
			depth++
			if depth == 1 {
				builder.WriteRune(' ')
			}
		case ')', ']', '}', '）', '】', '〕':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				builder.WriteRune(r)
			}
		}
	}
	return builder.String()
}

func sanitizeExternalSearchQuery(query string) string {
	query = webSearchSecretPattern.ReplaceAllString(query, "[secret omitted]")
	query = webSearchPrivateHost.ReplaceAllString(query, "[internal host omitted]")
	// 邮箱和电话一律脱敏。以前这里先扫 public/official/公开/官网/公告 判断查询是不是
	// 「公开语境」，命中就放行——那是拿关键词猜用户在问什么，而判错的代价是把私人邮箱
	// 或手机号原样发给外部搜索引擎。查公开联系方式本来也不需要把号码当搜索词，
	// 少这一条不影响检索，判错却不可撤回。
	query = webSearchEmailPattern.ReplaceAllString(query, "[email omitted]")
	query = webSearchPhonePattern.ReplaceAllString(query, "[phone omitted]")
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
