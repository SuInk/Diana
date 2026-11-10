// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
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

// 批量读取以前要等最慢的那一页：生产上单页中位 1.3 秒，批量的 p90 却是 13～21 秒——
// 一页退回本地浏览器慢慢读，另外两页早就读完了也陪着等。现在过了
// browserRenderBatchSoftDeadline 且至少有一页读到了就先返回，没读完的在后台接着读；
// 模型随后单独再读那一页时接上同一次读取（见 browserRenderReads），不重开。
var (
	browserRenderBatchSoftDeadline = 6 * time.Second
	// 后台接着读的那一页最多再给这么久，和调用方的超时脱钩。不给太长：一次性浏览器
	// 同时只有 3 个名额，慢页在后台占着，别的回复读网页就得排队。
	browserRenderDetachedTimeout = 30 * time.Second
	browserRenderReadTTL         = 3 * time.Minute
)

const browserRenderReadMaxEntries = 32

type browserRenderPendingPage struct {
	RequestedURL string `json:"requested_url"`
	Pending      bool   `json:"pending"`
	Note         string `json:"note"`
}

const browserRenderPendingNote = "这一页还没读完，已在后台接着读。先用已经读到的页面回答；确实需要这一页时，对这个网址单独再调一次 browser_render，读完了会直接返回。"

type browserRenderRead struct {
	done     chan struct{}
	page     RenderedPage
	err      error
	finished time.Time
}

// browserRenderReads 记批量读取里发起的每一页：读完的结果留 browserRenderReadTTL，
// 还在读的可以被后来的调用接着等。零值可用。
type browserRenderReads struct {
	mu      sync.Mutex
	entries map[string]*browserRenderRead
}

func browserRenderReadKey(engine, url string) string { return engine + "|" + url }

// start 返回这一页的读取：已经有一次在读或刚读成功就沿用，否则用 render 新开一次。
// render 跑在和调用方脱钩的 context 上，调用方先返回了它也读得完。
func (r *browserRenderReads) start(ctx context.Context, engine, url string, render func(context.Context, string) (RenderedPage, error)) *browserRenderRead {
	key := browserRenderReadKey(engine, url)
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = map[string]*browserRenderRead{}
	}
	for k, entry := range r.entries {
		if !entry.finished.IsZero() && now.Sub(entry.finished) > browserRenderReadTTL {
			delete(r.entries, k)
		}
	}
	if entry := r.entries[key]; entry != nil && (entry.finished.IsZero() || entry.err == nil) {
		return entry
	}
	for k, entry := range r.entries {
		if len(r.entries) < browserRenderReadMaxEntries {
			break
		}
		if !entry.finished.IsZero() {
			delete(r.entries, k)
		}
	}
	entry := &browserRenderRead{done: make(chan struct{})}
	r.entries[key] = entry
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), browserRenderDetachedTimeout)
	go func() {
		defer cancel()
		defer func() {
			if recovered := recover(); recovered != nil {
				entry.err = fmt.Errorf("browser_render 读取中断：%v", recovered)
			}
			r.mu.Lock()
			entry.finished = time.Now()
			r.mu.Unlock()
			close(entry.done)
		}()
		entry.page, entry.err = render(detached, url)
	}()
	return entry
}

// await 接上批量读取里发起过的这一页：还在读就等它，刚读成功就直接取。started 为 false
// 表示没有这样的读取（或者它失败了），调用方照常自己读。
func (r *browserRenderReads) await(ctx context.Context, engine, url string) (page RenderedPage, err error, started bool) {
	r.mu.Lock()
	entry := r.entries[browserRenderReadKey(engine, url)]
	if entry != nil && !entry.finished.IsZero() && (entry.err != nil || time.Since(entry.finished) > browserRenderReadTTL) {
		entry = nil
	}
	r.mu.Unlock()
	if entry == nil {
		return RenderedPage{}, nil, false
	}
	select {
	case <-entry.done:
		if entry.err != nil {
			return RenderedPage{}, nil, false
		}
		return entry.page, nil, true
	case <-ctx.Done():
		return RenderedPage{}, ctx.Err(), true
	}
}

func (t *BrowserRenderTool) runBatch(ctx context.Context, urls []string, find string, budget int) (string, error) {
	engine := renderEngineFromContext(ctx)
	reads := make([]*browserRenderRead, len(urls))
	finished := make(chan int, len(urls))
	for index, url := range urls {
		reads[index] = t.reads.start(ctx, engine, url, t.renderer.Render)
		go func(index int) {
			<-reads[index].done
			finished <- index
		}(index)
	}
	soft := time.NewTimer(browserRenderBatchSoftDeadline)
	defer soft.Stop()
	done := make([]bool, len(urls))
	remaining, succeeded, overdue := len(urls), 0, false
	for remaining > 0 && !(overdue && succeeded > 0) {
		select {
		case index := <-finished:
			done[index] = true
			remaining--
			if reads[index].err == nil {
				succeeded++
			}
		case <-soft.C:
			overdue = true
		case <-ctx.Done():
			remaining = 0
			if succeeded == 0 {
				return "", ctx.Err()
			}
		}
	}
	pages := make([]json.RawMessage, len(urls))
	failed := 0
	for index, url := range urls {
		if !done[index] {
			pages[index] = mustJSON(browserRenderPendingPage{RequestedURL: url, Pending: true, Note: browserRenderPendingNote})
			continue
		}
		var raw []byte
		err := reads[index].err
		if err == nil {
			var output string
			output, err = browserRenderOutputWithBudget(reads[index].page, find, max(budget/len(urls), 1))
			raw = []byte(output)
		}
		if err != nil {
			raw, _ = json.Marshal(browserRenderFailure{RequestedURL: url, Error: compactBrowserError(err.Error())})
			failed++
		}
		pages[index] = raw
	}
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
