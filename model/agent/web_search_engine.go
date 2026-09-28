// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 搜索引擎模式：web_search 不走搜索 API，而是用一次性沙盒浏览器打开搜索引擎的结果页，
// 从页面上取结果。对模型来说仍然是同一个 web_search，证据门控、claims 和来源校验
// 都照旧生效——以前关掉联网搜索、只在提示词里叫模型「用 browser_render 打开 Google」，
// 模型经常干脆不查，门控也认不出浏览器那条路。

const (
	WebSearchProviderSearchEngine = "search_engine"

	defaultSearchEngineTimeoutMS = 25_000
	// searchEnginePageTextChars 是结果页正文带给模型的上限。标题和链接已经在 results 里，
	// 正文只为补上摘要，太长只会挤掉工具输出预算。
	searchEnginePageTextChars = 3000
	// searchEngineRedirectTimeout 是还原一条加密跳转链接的等待上限，几条并发一起等。
	searchEngineRedirectTimeout = 4 * time.Second
)

// DefaultSearchEngines 是搜索引擎模式的默认顺序。Google 结果最好但最容易弹验证，
// 被拦了就按顺序换下一家。
var DefaultSearchEngines = []string{"google", "bing", "duckduckgo", "baidu"}

// errWebSearchBlocked 表示搜索引擎返回了人机验证页。同一次搜索里换关键词也还是会被拦，
// 所以这一家在本次调用里直接跳过。
var errWebSearchBlocked = errors.New("search engine returned a verification page")

type searchEngine struct {
	searchURL  string
	queryParam string
	// ownHosts 是引擎自己的域名：结果页上指向这些域名的链接是导航、设置、广告，不是结果。
	ownHosts []string
	// blockedMarkers 出现在最终地址或正文里，说明落到了人机验证页。
	blockedMarkers []string
}

var searchEngines = map[string]searchEngine{
	"google": {
		searchURL:      "https://www.google.com/search",
		queryParam:     "q",
		ownHosts:       []string{"google.com", "google.com.hk", "googleusercontent.com", "gstatic.com", "googleadservices.com"},
		blockedMarkers: []string{"/sorry/", "unusual traffic", "异常流量", "not a robot", "我不是机器人"},
	},
	"bing": {
		searchURL:      "https://www.bing.com/search",
		queryParam:     "q",
		ownHosts:       []string{"bing.com", "bing.net", "go.microsoft.com", "microsofttranslator.com"},
		blockedMarkers: []string{"/challenge", "verify you are human", "请解决以下难题"},
	},
	// html.duckduckgo.com 每次都弹验证，用普通入口。
	"duckduckgo": {
		searchURL:      "https://duckduckgo.com/",
		queryParam:     "q",
		ownHosts:       []string{"duckduckgo.com", "duck.com"},
		blockedMarkers: []string{"bots use duckduckgo", "anomaly-modal", "please complete the following challenge"},
	},
	"baidu": {
		searchURL:      "https://www.baidu.com/s",
		queryParam:     "wd",
		ownHosts:       []string{"baidu.com", "bdstatic.com", "bdimg.com", "hao123.com"},
		blockedMarkers: []string{"wappass.baidu.com", "百度安全验证", "安全验证"},
	},
}

// SearchEngineQueryPlaceholder 是自定义搜索地址里查询词的占位符。
const SearchEngineQueryPlaceholder = "{query}"

// customSearchEngineBlockedMarkers 是自定义引擎通用的人机验证标记，只在地址和标题里认，
// 以及一条结果都没取到时在正文里认（见 searchEnginePageBlocked）。
var customSearchEngineBlockedMarkers = []string{"captcha", "/challenge", "verify you are human", "unusual traffic", "人机验证", "安全验证"}

// CustomSearchEngineURL 报告 raw 是不是一条可用的自定义搜索地址：HTTPS（或本机调试的
// localhost HTTP），并带着 {query} 占位符。
func CustomSearchEngineURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.Contains(raw, SearchEngineQueryPlaceholder) &&
		validateWebSearchURL(strings.ReplaceAll(raw, SearchEngineQueryPlaceholder, "q")) == nil
}

// customSearchEngine 按自定义地址临时拼一个引擎：结果页上指回这个站自己的链接不算结果。
func customSearchEngine(template string) (searchEngine, bool) {
	parsed, err := url.Parse(strings.ReplaceAll(template, SearchEngineQueryPlaceholder, "q"))
	if err != nil || parsed.Hostname() == "" {
		return searchEngine{}, false
	}
	return searchEngine{
		searchURL:      template,
		ownHosts:       []string{strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")},
		blockedMarkers: customSearchEngineBlockedMarkers,
	}, true
}

// KnownSearchEngine 报告 name 是不是内置支持的搜索引擎。
func KnownSearchEngine(name string) bool {
	_, ok := searchEngines[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

type searchEngineResult struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

func (t *WebSearchTool) runSearchEngine(ctx context.Context, provider webSearchProviderConfig, query string) (string, error) {
	if t.renderer == nil {
		return "", errors.New("search engine mode needs the sandboxed browser")
	}
	target, engine, err := searchEngineRequestURL(provider, query)
	if err != nil {
		return "", err
	}

	page, err := t.renderer.Render(ctx, target)
	if err != nil {
		return "", err
	}
	results := searchEngineResults(engine, page.Links, provider.MaxResults)
	if searchEnginePageBlocked(engine, page, len(results) > 0) {
		return "", errWebSearchBlocked
	}
	if len(results) == 0 {
		return "", fmt.Errorf("%s returned no search results: %w", provider.Tool, errWebSearchNoResults)
	}
	results = dedupeSearchEngineResults(t.resolveSearchEngineRedirects(ctx, results))
	// 输出里的每个 URL 都会被当成候选来源：结果页地址不放进来，正文里的网址也去掉协议头
	// ——那是结果下面印的显示网址（常常只有域名），真正的链接已经在 results 里。
	formatted, err := json.MarshalIndent(map[string]any{
		"engine":    provider.Tool,
		"results":   results,
		"page_text": webSearchURLPattern.ReplaceAllStringFunc(truncateText(searchEngineResultText(page.Text, results), searchEnginePageTextChars), stripURLScheme),
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(formatted), nil
}

// searchEnginePageBlocked 判断是不是落到了人机验证页。地址和标题里的标记直接算数；
// 正文里的只在一条结果都没取到时才算——搜「异常流量」本身的结果页正文里也会有这几个字。
// searchEngineRequestURL 拼出这一次要打开的结果页地址。内置引擎把查询词放进自己的参数，
// 自定义地址把 {query} 换成转义后的查询词。
func searchEngineRequestURL(provider webSearchProviderConfig, query string) (string, searchEngine, error) {
	if engine, ok := searchEngines[provider.Tool]; ok {
		searchURL, err := url.Parse(provider.URL)
		if err != nil {
			return "", searchEngine{}, err
		}
		values := searchURL.Query()
		values.Set(engine.queryParam, query)
		searchURL.RawQuery = values.Encode()
		return searchURL.String(), engine, nil
	}
	engine, ok := customSearchEngine(provider.URL)
	if !ok || !strings.Contains(provider.URL, SearchEngineQueryPlaceholder) {
		return "", searchEngine{}, fmt.Errorf("unknown search engine %q", provider.Tool)
	}
	return strings.ReplaceAll(provider.URL, SearchEngineQueryPlaceholder, url.QueryEscape(query)), engine, nil
}

func searchEnginePageBlocked(engine searchEngine, page RenderedPage, hasResults bool) bool {
	head := strings.ToLower(page.URL + "\n" + page.Title)
	body := strings.ToLower(truncateText(page.Text, 2000))
	for _, marker := range engine.blockedMarkers {
		marker = strings.ToLower(marker)
		if strings.Contains(head, marker) || (!hasResults && strings.Contains(body, marker)) {
			return true
		}
	}
	return false
}

func searchEngineResults(engine searchEngine, links []RenderedLink, limit int) []searchEngineResult {
	if limit <= 0 {
		limit = defaultWebSearchMaxResults
	}
	seen := map[string]bool{}
	var results []searchEngineResult
	for _, link := range links {
		target := unwrapSearchEngineLink(link.URL)
		parsed, err := url.Parse(target)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
			continue
		}
		if searchEngineOwnHost(engine, parsed) {
			continue
		}
		title := firstRenderedLine(link.Text)
		if len([]rune(title)) < 2 || searchEngineDisplayURL(title, parsed) {
			continue
		}
		key, normalized := canonicalWebSearchURL(target)
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, searchEngineResult{Title: title, URL: normalized})
		if len(results) >= limit {
			break
		}
	}
	return results
}

// unwrapSearchEngineLink 把参数里明文带着目标地址的跳转链接还原成目标地址。
// Google 的 /goto?url= 和百度的 /link?url= 是加密串，交给 resolveSearchEngineRedirects。
func unwrapSearchEngineLink(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := strings.ToLower(parsed.Hostname())
	query := parsed.Query()
	switch {
	case strings.Contains(host, "google.") && parsed.Path == "/url":
		for _, key := range []string{"q", "url"} {
			if target := query.Get(key); strings.HasPrefix(target, "http") {
				return target
			}
		}
	case strings.HasSuffix(host, "bing.com") && strings.HasPrefix(parsed.Path, "/ck/a"):
		if encoded := strings.TrimPrefix(query.Get("u"), "a1"); encoded != "" {
			if decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encoded, "=")); err == nil && strings.HasPrefix(string(decoded), "http") {
				return string(decoded)
			}
		}
	case strings.HasSuffix(host, "duckduckgo.com") && strings.HasPrefix(parsed.Path, "/l/"):
		if target := query.Get("uddg"); strings.HasPrefix(target, "http") {
			return target
		}
	}
	return raw
}

func searchEngineOwnHost(engine searchEngine, parsed *url.URL) bool {
	if searchEngineOpaqueRedirect(parsed) {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, own := range engine.ownHosts {
		if host == own || strings.HasSuffix(host, "."+own) {
			return true
		}
	}
	return false
}

func firstRenderedLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// searchEngineOpaqueRedirect 报告链接是不是只能靠请求一次才知道去哪的加密跳转。
func searchEngineOpaqueRedirect(parsed *url.URL) bool {
	host := strings.ToLower(parsed.Hostname())
	switch {
	case strings.Contains(host, "google.") && parsed.Path == "/goto":
		return true
	case (host == "www.baidu.com" || host == "baidu.com") && parsed.Path == "/link":
		return true
	}
	return false
}

// resolveSearchEngineRedirects 对加密跳转链接各发一次不跟随跳转的 GET，用 Location 换掉原链接。
// 这些跳转只做 302，不需要浏览器；解不出来就留着原链接，模型仍然可以用浏览器打开它。
func (t *WebSearchTool) resolveSearchEngineRedirects(ctx context.Context, results []searchEngineResult) []searchEngineResult {
	base := t.httpClient()
	client := &http.Client{
		Transport: base.Transport,
		Jar:       base.Jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	ctx, cancel := context.WithTimeout(ctx, searchEngineRedirectTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for index := range results {
		parsed, err := url.Parse(results[index].URL)
		if err != nil || !searchEngineOpaqueRedirect(parsed) {
			continue
		}
		wg.Add(1)
		go func(result *searchEngineResult) {
			defer wg.Done()
			defer recoverGoroutinePanic("web_search_redirect")
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, result.URL, nil)
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36")
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
			if resp.StatusCode < 300 || resp.StatusCode >= 400 {
				return
			}
			location, err := resp.Location()
			if err != nil || (location.Scheme != "http" && location.Scheme != "https") || searchEngineOpaqueRedirect(location) {
				return
			}
			_, result.URL = canonicalWebSearchURL(location.String())
		}(&results[index])
	}
	wg.Wait()
	return results
}

// dedupeSearchEngineResults 去掉还原之后才看出是同一个地址的结果，保留排在前面的那条。
func dedupeSearchEngineResults(results []searchEngineResult) []searchEngineResult {
	seen := map[string]bool{}
	out := results[:0]
	for _, result := range results {
		key, _ := canonicalWebSearchURL(result.URL)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, result)
	}
	return out
}

func stripURLScheme(raw string) string {
	if index := strings.Index(raw, "://"); index >= 0 {
		return raw[index+3:]
	}
	return raw
}

// searchEngineDisplayURL 认出结果下面印的显示网址（「example.com › docs」或裸域名）。
// DuckDuckGo 把它单独做成一个链接，排在真正的标题前面，不跳过的话标题就成了网址。
func searchEngineDisplayURL(title string, target *url.URL) bool {
	lower := strings.ToLower(title)
	if strings.Contains(title, "›") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return true
	}
	host := strings.TrimPrefix(strings.ToLower(target.Hostname()), "www.")
	return strings.TrimPrefix(lower, "www.") == host
}

// searchEngineResultText 从第一条结果的标题开始截正文：前面是搜索框、分类标签、时间筛选
// 这些页面框架，Google 上能占掉一千多字，截在前面摘要就进不了输出预算。
func searchEngineResultText(text string, results []searchEngineResult) string {
	start := -1
	for _, result := range results {
		if index := strings.Index(text, result.Title); index >= 0 && (start < 0 || index < start) {
			start = index
		}
	}
	if start <= 0 {
		return text
	}
	return text[start:]
}
