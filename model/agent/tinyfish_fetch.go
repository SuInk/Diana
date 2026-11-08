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
	"net/url"
	"strings"
	"sync"
	"time"
)

// 读网页走 TinyFish 的 fetch 接口，照参考 bot（mizorewww/pi-extension-telegram-agent
// 的 src/tools/search.ts）的做法：一次 HTTP 请求拿到整页 markdown，不起浏览器。
//
// 本地有头 Chrome 读一页要起进程、等 DOM 和网络稳住，生产 11-05～07 的 browser_render
// 中位 10.5 秒；同一批网址走 fetch 接口实测 1.4～7.4 秒。整页按 URL 缓存 15 分钟，
// 模型对同一页换 find 再读不再重新抓取。fetch 失败（被拦、超时、空正文）时由调用方
// 退回本地浏览器。
//
// 和参考 bot 唯一的不同是请求时开了 links：browser_render 的说明承诺返回站内链接，
// 模型要从里面取真实网址继续读，不能让它去猜。
const (
	TinyFishFetchURL = "https://api.fetch.tinyfish.ai"

	// 和浏览器渲染默认超时同一档：fetch 失败退回浏览器时，一次读取最多花两份。
	tinyFishFetchTimeout      = 25 * time.Second
	tinyFishFetchMaxBodyBytes = 1 << 20
	tinyFishFetchMaxChars     = 200_000
	tinyFishPageCacheTTL      = 15 * time.Minute
	tinyFishPageCacheEntries  = 32
)

// errTinyFishFetch 包住 fetch 接口自己报的失败，调用方据此退回浏览器。
var errTinyFishFetch = errors.New("TinyFish fetch failed")

type tinyFishCachedPage struct {
	page      RenderedPage
	expiresAt time.Time
}

type tinyFishPageCache struct {
	mu      sync.Mutex
	entries map[string]tinyFishCachedPage
	order   []string
}

var tinyFishPages = &tinyFishPageCache{entries: map[string]tinyFishCachedPage{}}

func (c *tinyFishPageCache) get(key string, now time.Time) (RenderedPage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !now.Before(entry.expiresAt) {
		return RenderedPage{}, false
	}
	return entry.page, true
}

func (c *tinyFishPageCache) put(key string, page RenderedPage, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = tinyFishCachedPage{page: page, expiresAt: now.Add(tinyFishPageCacheTTL)}
	for len(c.order) > tinyFishPageCacheEntries {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
}

// fetchTinyFishPage 用 TinyFish 读一页公网网页，返回和浏览器渲染同一形状的结果。
// endpoint 为空时用 TinyFishFetchURL。maxChars 是交给模型的正文上限，整页另存在
// FullText 里给 find 用。
func fetchTinyFishPage(ctx context.Context, apiKey, endpoint, rawURL string, maxChars int) (RenderedPage, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return RenderedPage{}, fmt.Errorf("%w: API key is missing", errTinyFishFetch)
	}
	if err := validateSandboxedBrowserURL(ctx, rawURL); err != nil {
		return RenderedPage{}, err
	}
	started := time.Now()
	if page, ok := tinyFishPages.get(rawURL, started); ok {
		return withTinyFishTextLimit(page, maxChars), nil
	}
	if strings.TrimSpace(endpoint) == "" {
		endpoint = TinyFishFetchURL
	}
	body, err := json.Marshal(map[string]any{"urls": []string{rawURL}, "format": "markdown", "links": true, "image_links": false})
	if err != nil {
		return RenderedPage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, tinyFishFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return RenderedPage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return RenderedPage{}, fmt.Errorf("%w: timeout", errTinyFishFetch)
		}
		return RenderedPage{}, fmt.Errorf("%w: network", errTinyFishFetch)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RenderedPage{}, fmt.Errorf("%w: HTTP %d", errTinyFishFetch, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, tinyFishFetchMaxBodyBytes+1))
	if err != nil {
		return RenderedPage{}, fmt.Errorf("%w: read body", errTinyFishFetch)
	}
	if len(data) > tinyFishFetchMaxBodyBytes {
		return RenderedPage{}, fmt.Errorf("%w: response too large", errTinyFishFetch)
	}
	var decoded struct {
		Results []struct {
			URL         string   `json:"url"`
			FinalURL    string   `json:"final_url"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Text        string   `json:"text"`
			Links       []string `json:"links"`
		} `json:"results"`
		Errors []struct {
			Error string `json:"error"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return RenderedPage{}, fmt.Errorf("%w: invalid JSON", errTinyFishFetch)
	}
	if len(decoded.Results) == 0 || strings.TrimSpace(decoded.Results[0].Text) == "" {
		reason := "empty_content"
		if len(decoded.Errors) > 0 && strings.TrimSpace(decoded.Errors[0].Error) != "" {
			reason = strings.TrimSpace(decoded.Errors[0].Error)
		}
		return RenderedPage{}, fmt.Errorf("%w: %s", errTinyFishFetch, reason)
	}
	result := decoded.Results[0]
	full := strings.TrimSpace(result.Text)
	if runes := []rune(full); len(runes) > tinyFishFetchMaxChars {
		full = string(runes[:tinyFishFetchMaxChars])
	}
	final := rawURL
	if candidate := tinyFishHTTPURL(firstNonEmpty(result.FinalURL, result.URL)); candidate != "" {
		final = candidate
	}
	links := make([]RenderedLink, 0, min(len(result.Links), maxRenderedPageLinks))
	seen := map[string]bool{}
	for _, link := range result.Links {
		link = tinyFishHTTPURL(link)
		if link == "" || seen[link] || len(links) == maxRenderedPageLinks {
			continue
		}
		seen[link] = true
		links = append(links, RenderedLink{URL: link})
	}
	page := RenderedPage{
		SourceType:      "tinyfish_fetch",
		SourceURLs:      []string{final},
		RetrievedAt:     time.Now().UTC().Format(time.RFC3339),
		RequestedURL:    rawURL,
		URL:             final,
		Title:           strings.TrimSpace(strings.Join(strings.Fields(result.Title), " ")),
		Description:     strings.TrimSpace(strings.Join(strings.Fields(result.Description), " ")),
		FullText:        full,
		Links:           links,
		BrowserEngine:   "tinyfish",
		Stable:          true,
		StabilityReason: "fetched_by_api",
		WaitedMS:        time.Since(started).Milliseconds(),
	}
	tinyFishPages.put(rawURL, page, time.Now())
	return withTinyFishTextLimit(page, maxChars), nil
}

// tinyFishHTTPURL 只收绝对的 http(s) 地址，去掉锚点：同一页的 #section 不算新链接。
func tinyFishHTTPURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	parsed.Fragment = ""
	return parsed.String()
}

func withTinyFishTextLimit(page RenderedPage, maxChars int) RenderedPage {
	if maxChars <= 0 {
		maxChars = defaultRenderBrowserTextChars
	}
	// 和浏览器渲染同一个截断函数，模型看到的截断标记一致。
	page.Text = truncateText(page.FullText, maxChars)
	page.Truncated = len([]rune(page.FullText)) > maxChars
	return page
}
