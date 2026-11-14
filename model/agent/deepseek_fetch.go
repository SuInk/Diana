// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// 读网页也可以走 DeepSeek 工具 API 的 POST /v1/tools/fetch：一次请求拿回整页 Markdown，
// 和 TinyFish fetch 一样不起浏览器。它只回 title 和 content，没有链接和图片列表，
// 这两样从 Markdown 里的 [文字](网址) 和 ![](网址) 抽出来。文档：
// api-docs.deepseek.com/guides/search。
const (
	DeepSeekFetchURL = "https://api.deepseek.com/v1/tools/fetch"

	deepSeekFetchTimeout      = 25 * time.Second
	deepSeekFetchMaxBodyBytes = 2 << 20
)

// errDeepSeekFetch 包住 fetch 接口自己报的失败，调用方据此退回本地浏览器。
var errDeepSeekFetch = errors.New("DeepSeek fetch failed")

var (
	markdownImagePattern = regexp.MustCompile(`!\[[^\]]*\]\(\s*<?(https?://[^\s)>]+)>?`)
	markdownLinkPattern  = regexp.MustCompile(`(^|[^!])\[[^\]]*\]\(\s*<?(https?://[^\s)>]+)>?`)
)

// DeepSeekFetchURLFor 由搜索接口地址推出同一服务的 fetch 地址：配置里存的是搜索地址，
// 用户填了代理或自建网关时，读网页也该走同一个地方。
func DeepSeekFetchURLFor(searchURL string) string {
	searchURL = strings.TrimRight(strings.TrimSpace(searchURL), "/")
	if base, ok := strings.CutSuffix(searchURL, "/search"); ok && base != "" {
		return base + "/fetch"
	}
	return DeepSeekFetchURL
}

// fetchDeepSeekPage 用 DeepSeek 读一页公网网页，返回和浏览器渲染同一形状的结果。
// 整页按 URL 和 TinyFish 共用 15 分钟的缓存。
func fetchDeepSeekPage(ctx context.Context, apiKey, endpoint, rawURL string, maxChars int) (RenderedPage, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return RenderedPage{}, fmt.Errorf("%w: API key is missing", errDeepSeekFetch)
	}
	if err := validateSandboxedBrowserURL(ctx, rawURL); err != nil {
		return RenderedPage{}, err
	}
	started := time.Now()
	cacheKey := "deepseek:" + rawURL
	if page, ok := tinyFishPages.get(cacheKey, started); ok {
		return withTinyFishTextLimit(page, maxChars), nil
	}
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DeepSeekFetchURL
	}
	body, err := json.Marshal(map[string]any{"url": rawURL})
	if err != nil {
		return RenderedPage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, deepSeekFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return RenderedPage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return RenderedPage{}, fmt.Errorf("%w: timeout", errDeepSeekFetch)
		}
		return RenderedPage{}, fmt.Errorf("%w: network", errDeepSeekFetch)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, deepSeekFetchMaxBodyBytes+1))
	if err != nil {
		return RenderedPage{}, fmt.Errorf("%w: read body", errDeepSeekFetch)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 403 是安全策略拒绝、404 是拿不到可用正文，都交给本地浏览器再试。
		var failure struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		return RenderedPage{}, fmt.Errorf("%w: HTTP %d %s", errDeepSeekFetch, resp.StatusCode, strings.TrimSpace(failure.Error.Message))
	}
	if len(data) > deepSeekFetchMaxBodyBytes {
		return RenderedPage{}, fmt.Errorf("%w: response too large", errDeepSeekFetch)
	}
	var decoded struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return RenderedPage{}, fmt.Errorf("%w: invalid JSON", errDeepSeekFetch)
	}
	full := strings.TrimSpace(decoded.Content)
	if full == "" {
		return RenderedPage{}, fmt.Errorf("%w: empty_content", errDeepSeekFetch)
	}
	if runes := []rune(full); len(runes) > tinyFishFetchMaxChars {
		full = string(runes[:tinyFishFetchMaxChars])
	}
	page := RenderedPage{
		SourceType:      "deepseek_fetch",
		SourceURLs:      []string{rawURL},
		RetrievedAt:     time.Now().UTC().Format(time.RFC3339),
		RequestedURL:    rawURL,
		URL:             rawURL,
		Title:           strings.TrimSpace(strings.Join(strings.Fields(decoded.Title), " ")),
		FullText:        full,
		Links:           markdownPageLinks(full),
		Images:          tinyFishImages(markdownMatches(markdownImagePattern, full, 1)),
		BrowserEngine:   "deepseek",
		Stable:          true,
		StabilityReason: "fetched_by_api",
		WaitedMS:        time.Since(started).Milliseconds(),
	}
	tinyFishPages.put(cacheKey, page, time.Now())
	return withTinyFishTextLimit(page, maxChars), nil
}

func markdownMatches(pattern *regexp.Regexp, text string, group int) []string {
	matches := pattern.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match[group])
	}
	return out
}

// markdownPageLinks 按出现顺序收正文里的链接，去锚点、去重，最多 maxRenderedPageLinks 条。
func markdownPageLinks(text string) []RenderedLink {
	links := []RenderedLink{}
	seen := map[string]bool{}
	for _, raw := range markdownMatches(markdownLinkPattern, text, 2) {
		link := tinyFishHTTPURL(raw)
		if link == "" || seen[link] {
			continue
		}
		if len(links) == maxRenderedPageLinks {
			break
		}
		seen[link] = true
		links = append(links, RenderedLink{URL: link})
	}
	return links
}
