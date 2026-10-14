// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// 线上 10-14：Google 前排全是 Gemini 4 的旧爆料，拿到结果就停，Bing 没查，
// 模型据此答「Gemini 4 都还没影」。单个检索词也要两家引擎一起查。
func TestSingleQuerySearchesTwoEnginesAndMergesHits(t *testing.T) {
	var mu sync.Mutex
	var hosts []string
	renderer := PageRendererFunc(func(_ context.Context, raw string) (RenderedPage, error) {
		u, _ := url.Parse(raw)
		mu.Lock()
		hosts = append(hosts, u.Host)
		mu.Unlock()
		links := []RenderedLink{{URL: "https://example.com/shared", Text: "Shared page", Snippet: "shared"}}
		if strings.Contains(u.Host, "bing") {
			links = append(links, RenderedLink{URL: "https://blog.example.com/launch", Text: "Official launch", Snippet: "launched today"})
		} else {
			links = append(links, RenderedLink{URL: "https://news.example.com/leak", Text: "Leak", Snippet: "plans to release"})
		}
		return RenderedPage{URL: raw, Title: "Search", Text: "results", Links: links}, nil
	})
	tool := searchEngineTestTool(t, renderer, "google", "bing")
	raw, err := tool.Run(context.Background(), map[string]any{"query": "Gemini 4"})
	if err != nil {
		t.Fatal(err)
	}
	var result webSearchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(hosts, " ")
	if !strings.Contains(joined, "google") || !strings.Contains(joined, "bing") {
		t.Fatalf("both engines should run: %v", hosts)
	}
	var urls []string
	shared := 0
	for _, hit := range result.Results {
		urls = append(urls, hit.URL)
		if hit.URL == "https://example.com/shared" {
			shared++
		}
	}
	if shared != 1 || !strings.Contains(strings.Join(urls, " "), "blog.example.com/launch") {
		t.Fatalf("hits not merged and deduplicated: %v", urls)
	}
}

func TestWebSearchProviderLanes(t *testing.T) {
	engines := []WebSearchProviderConfig{
		{Name: "google", Type: WebSearchProviderSearchEngine},
		{Name: "bing", Type: WebSearchProviderSearchEngine},
		{Name: "duckduckgo", Type: WebSearchProviderSearchEngine},
		{Name: "baidu", Type: WebSearchProviderSearchEngine},
	}
	lanes := webSearchProviderLanes(engines)
	if len(lanes) != 2 || lanes[0][0].Name != "google" || lanes[0][1].Name != "duckduckgo" || lanes[1][0].Name != "bing" || lanes[1][1].Name != "baidu" {
		t.Fatalf("lanes = %+v", lanes)
	}
	paid := append([]WebSearchProviderConfig{{Name: "tavily", Type: "tavily"}}, engines...)
	if lanes := webSearchProviderLanes(paid); len(lanes) != 1 || len(lanes[0]) != len(paid) {
		t.Fatalf("paid providers must keep their configured order: %+v", lanes)
	}
	if lanes := webSearchProviderLanes(engines[:1]); len(lanes) != 1 {
		t.Fatalf("single engine split: %+v", lanes)
	}
}

func TestWebSearchDomainsBecomeSiteFilter(t *testing.T) {
	candidates, err := webSearchCandidates(map[string]any{
		"query":   "Gemini 4",
		"domains": []any{"https://blog.google/technology", "ai.google.dev", "evil.local", "bad domain", "ai.google.dev"},
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := candidates[0].Query; got != "Gemini 4 site:blog.google OR site:ai.google.dev" {
		t.Fatalf("query = %q", got)
	}
}

func TestSearchEngineRecencyFilters(t *testing.T) {
	now := time.Unix(1_760_000_000, 0)
	cases := map[string]struct {
		days int
		want string
	}{
		"google":     {7, "tbs=qdr%3Aw"},
		"bing":       {30, "filters=ex1%3A%22ez3%22"},
		"duckduckgo": {1, "df=d"},
		"baidu":      {3, "gpc=stf%3D1759740800%2C1760000000%7Cstftype%3D1"},
	}
	for engine, c := range cases {
		got := applySearchEngineRecency(engine, searchEngines[engine].searchURL+"?q=x", c.days, now)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %s missing %s", engine, got, c.want)
		}
	}
	if got := applySearchEngineRecency("custom", "https://s.example/?q=x", 7, now); got != "https://s.example/?q=x" {
		t.Fatalf("custom engine changed: %s", got)
	}
	if got := applySearchEngineRecency("google", "https://www.google.com/search?q=x", 0, now); strings.Contains(got, "tbs") {
		t.Fatalf("zero recency filtered: %s", got)
	}
}

func TestWebSearchRecencyReachesEngineURL(t *testing.T) {
	var mu sync.Mutex
	var requested []string
	renderer := PageRendererFunc(func(_ context.Context, raw string) (RenderedPage, error) {
		mu.Lock()
		requested = append(requested, raw)
		mu.Unlock()
		return RenderedPage{URL: raw, Title: "Search", Text: "r", Links: []RenderedLink{{URL: "https://example.com/a", Text: "A"}}}, nil
	})
	tool := searchEngineTestTool(t, renderer, "google")
	if _, err := tool.Run(context.Background(), map[string]any{"query": "Gemini 4", "recency": float64(30)}); err != nil {
		t.Fatal(err)
	}
	if len(requested) != 1 || !strings.Contains(requested[0], "tbs=qdr%3Am") {
		t.Fatalf("recency not applied: %v", requested)
	}
}
