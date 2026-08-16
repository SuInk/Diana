// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// FetchRenderedPage 通过 CDP 在新标签页渲染网页并提取标题、描述和正文摘要，结束后关闭标签页。
// 始终新开标签页而不复用现有标签，避免自动抓取动到用户正在看的页面。
func FetchRenderedPage(ctx context.Context, cdpURL string, pageURL string, timeout time.Duration, maxTextChars int) (RenderedPage, error) {
	if err := validateBrowserURL(pageURL); err != nil {
		return RenderedPage{}, err
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if maxTextChars <= 0 {
		maxTextChars = 4000
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	baseURL := strings.TrimRight(strings.TrimSpace(cdpURL), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:9222"
	}
	target, err := newBrowserTarget(ctx, baseURL, pageURL)
	if err != nil {
		return RenderedPage{}, err
	}
	defer closeBrowserTarget(baseURL, target.ID)
	if target.WebSocketDebuggerURL == "" {
		return RenderedPage{}, fmt.Errorf("browser target has no websocket debugger URL")
	}

	client, err := newCDPClient(ctx, target.WebSocketDebuggerURL, timeout)
	if err != nil {
		return RenderedPage{}, err
	}
	defer client.Close()
	_ = client.call(ctx, "Page.enable", map[string]any{}, nil)
	_ = client.call(ctx, "Runtime.enable", map[string]any{}, nil)
	// /json/new 返回时页面往往还停在 about:blank，必须轮询等真实文档加载完，
	// 否则 evaluate 会跑在初始空文档里拿到一片空白。
	waitForDocumentReady(ctx, client)
	// load 之后再等一小段：知乎/小红书这类页面在 load 后才由 JS 补齐正文和元数据。
	_, _ = client.evaluate(ctx, `new Promise(resolve => setTimeout(resolve, 1200))`)

	expr := fmt.Sprintf(`(() => {
const pick = (selector) => {
  const el = document.querySelector(selector);
  return el ? (el.getAttribute("content") || "") : "";
};
const text = (document.body ? (document.body.innerText || "") : "").trim();
return {
  url: location.href,
  title: (pick('meta[property="og:title"]') || document.title || "").trim(),
  description: (pick('meta[property="og:description"]') || pick('meta[name="description"]') || "").trim(),
  text: text.length > %d ? text.slice(0, %d) : text
};
})()`, maxTextChars, maxTextChars)
	raw, err := client.evaluate(ctx, expr)
	if err != nil {
		return RenderedPage{}, err
	}
	var page RenderedPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return RenderedPage{}, err
	}
	return page, nil
}

// waitForDocumentReady 轮询等待目标文档离开 about:blank 且加载完成；
// 出错或超时不阻断流程，后续提取拿到什么算什么。
func waitForDocumentReady(ctx context.Context, client *cdpClient) {
	for i := 0; i < 50; i++ {
		raw, err := client.evaluate(ctx, `(() => ({href: location.href, ready: document.readyState}))()`)
		if err != nil {
			return
		}
		var state struct {
			Href  string `json:"href"`
			Ready string `json:"ready"`
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			return
		}
		if state.Href != "" && state.Href != "about:blank" && state.Ready == "complete" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// closeBrowserTarget 关闭抓取用的标签页，失败只影响标签残留，不影响抓取结果。
func closeBrowserTarget(baseURL string, targetID string) {
	if targetID == "" {
		return
	}
	// 独立短超时：调用方 ctx 可能已经取消，但标签页仍应尽力关闭。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/json/close/"+targetID, nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}
