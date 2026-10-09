// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

// 一步只能调一个工具，逐页 browser_render 时模型常读完一页就收尾；通用 agent（opencode、
// Codex）靠一次发多个调用并行读页。这里让 browser_render 一次接几个网址并行打开。
const browserRenderMaxURLs = 3

type browserRenderBatch struct {
	Pages []json.RawMessage `json:"pages"`
}

type browserRenderFailure struct {
	RequestedURL string `json:"requested_url"`
	Error        string `json:"error"`
}

func browserRenderURLs(input map[string]any) []string {
	var supplied []string
	if url := stringFromInput(input, "url"); url != "" {
		supplied = append(supplied, url)
	}
	switch values := input["urls"].(type) {
	case []string:
		supplied = append(supplied, values...)
	case []any:
		for _, value := range values {
			if url, ok := value.(string); ok {
				supplied = append(supplied, url)
			}
		}
	}
	urls := make([]string, 0, browserRenderMaxURLs)
	seen := map[string]bool{}
	for _, url := range supplied {
		url = strings.TrimSpace(url)
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		urls = append(urls, url)
		if len(urls) == browserRenderMaxURLs {
			break
		}
	}
	return urls
}

func (t *BrowserRenderTool) runBatch(ctx context.Context, urls []string, find string, budget int) (string, error) {
	pages := make([]json.RawMessage, len(urls))
	failed := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for index, url := range urls {
		wg.Add(1)
		go func(index int, url string) {
			defer recoverGoroutinePanic("browser_render_batch")
			defer wg.Done()
			var raw []byte
			page, err := t.renderer.Render(ctx, url)
			if err == nil {
				var output string
				output, err = browserRenderOutputWithBudget(page, find, max(budget/len(urls), 1))
				raw = []byte(output)
			}
			if err != nil {
				raw, _ = json.Marshal(browserRenderFailure{RequestedURL: url, Error: compactBrowserError(err.Error())})
				mu.Lock()
				failed++
				mu.Unlock()
			}
			pages[index] = raw
		}(index, url)
	}
	wg.Wait()
	if failed == len(urls) {
		return "", errors.New("browser_render 这批网址全部打开失败：" + string(mustJSON(pages)))
	}
	return string(mustJSON(browserRenderBatch{Pages: pages})), nil
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

// browserRenderPages 兼容单页和批量两种输出，读不到正文的页面也原样返回，由调用方判断。
func browserRenderPages(output string) []browserRenderPayload {
	var batch browserRenderBatch
	if json.Unmarshal([]byte(output), &batch) == nil && batch.Pages != nil {
		pages := make([]browserRenderPayload, 0, len(batch.Pages))
		for _, raw := range batch.Pages {
			var page browserRenderPayload
			if json.Unmarshal(raw, &page) == nil {
				pages = append(pages, page)
			}
		}
		return pages
	}
	var page browserRenderPayload
	if json.Unmarshal([]byte(output), &page) != nil {
		return nil
	}
	return []browserRenderPayload{page}
}
