package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// WebSearchHit is a discovery result, not proof that its page was read.
type WebSearchHit struct {
	// Ref 是这条候选的短编号，browser_render 可以用它代替 URL，见 searchRef。
	Ref         string `json:"ref,omitempty"`
	Title       string `json:"title,omitempty"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	Query       string `json:"query,omitempty"`
	Provider    string `json:"provider,omitempty"`
	// AlsoFrom 是同一网址还在哪些「搜索源: 查询」里出现过，合并去重时记下。
	AlsoFrom []string `json:"also_from,omitempty"`
	// SnippetOffset、SnippetChars 只在分页把这条的摘要切开时出现：这一段从摘要的
	// 第几个字起、摘要一共多少字。
	SnippetOffset int `json:"snippet_offset,omitempty"`
	SnippetChars  int `json:"snippet_chars,omitempty"`
}

type webSearchQueryResponse struct {
	Query    string `json:"query"`
	Status   string `json:"status"`
	Provider string `json:"provider,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Run 搜索并按整条分页交给模型（见 web_search_page.go）。offset 非零是翻页：同样的
// 参数刚搜过就直接取那次的完整结果，不重新搜。
func (t *WebSearchTool) Run(ctx context.Context, input map[string]any) (string, error) {
	key := webSearchPageKey(input)
	offset := intFromInput(input, "offset", 0)
	result, cached := t.pages.get(key)
	if !cached || offset <= 0 {
		var err error
		if result, err = t.search(ctx, input); err != nil {
			return "", err
		}
		t.pages.put(key, result)
	}
	return webSearchPage(result, webSearchPageArgs(input), offset, readingPageBudget(ctx))
}

// search executes model-supplied queries independently. Free search engines are
// split into two lanes that run side by side, each with its own ordered
// fallback; the batch shares a deadline and total provider-call budget.
func (t *WebSearchTool) search(ctx context.Context, input map[string]any) (webSearchResult, error) {
	candidates, err := webSearchCandidates(input, t.maxQueries)
	if err != nil {
		return webSearchResult{}, err
	}
	ctx = withWebSearchRecency(ctx, intFromInput(input, "recency", 0))
	providers := t.providers
	if len(providers) == 0 {
		if providers, err = t.loadProviders(); err != nil {
			return webSearchResult{}, fmt.Errorf("web search configuration is invalid: %w", err)
		}
	}
	lanes := webSearchProviderLanes(providers)
	if len(candidates) == 1 && len(lanes) == 1 {
		return t.runSearchQuery(ctx, map[string]any{"query": candidates[0].Query})
	}
	timeout := t.timeout
	if timeout <= 0 {
		timeout = defaultWebSearchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	calls := t.maxProviderCalls
	if calls <= 0 {
		// Give each query/lane one attempt without increasing small-batch budgets.
		calls = max(defaultWebSearchMaxProviderCalls, len(candidates)*len(lanes))
	}
	calls = min(calls, maximumWebSearchMaxProviderCalls)
	type searchJob struct {
		query     int
		providers []WebSearchProviderConfig
	}
	var jobs []searchJob
	for i := range candidates {
		for _, lane := range lanes {
			jobs = append(jobs, searchJob{query: i, providers: lane})
		}
	}
	results := make([]webSearchResult, len(jobs))
	errors := make([]error, len(jobs))
	var wg sync.WaitGroup
	// Bound jobs across both provider lanes; ten queries can create twenty jobs.
	slots := make(chan struct{}, webSearchBatchConcurrency)
	for i, job := range jobs {
		wg.Add(1)
		go func(index int, job searchJob) {
			defer recoverGoroutinePanic("web_search_parallel")
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				errors[index] = ctx.Err()
				return
			}
			quota := calls / len(jobs)
			if index < calls%len(jobs) {
				quota++
			}
			if quota == 0 {
				results[index] = webSearchResult{Status: "budget_exhausted", StopReason: "provider_call_budget_exhausted"}
				return
			}
			worker := *t
			worker.providers = job.providers
			worker.maxProviderCalls = quota
			results[index], errors[index] = worker.runSearchQuery(ctx, map[string]any{"query": candidates[job.query].Query})
		}(i, job)
	}
	wg.Wait()
	batch := webSearchResult{Status: "no_results", Strategy: "parallel_queries", StopReason: "queries_completed", Query: candidates[0].Query, Queries: candidates, Budget: webSearchBudget{MaxQueries: len(candidates), MaxProviderCalls: calls, DeadlineMS: timeout.Milliseconds()}}
	sourceSeen := map[string]bool{}
	for i, result := range results {
		query := jobs[i].query
		status := result.Status
		if errors[i] != nil {
			status = classifyWebSearchError(errors[i], nil, ctx.Err())
		}
		search := webSearchQueryResponse{Query: candidates[query].Query, Status: status, Provider: result.Provider}
		if errors[i] != nil {
			search.Error = safeWebSearchError(errors[i])
		}
		batch.Searches = append(batch.Searches, search)
		batch.Queries[query].Status = "attempted"
		if batch.Queries[query].Outcome != "ok" {
			batch.Queries[query].Outcome = status
		}
		batch.Budget.QueriesUsed += result.Budget.QueriesUsed
		batch.Budget.ProviderCalls += result.Budget.ProviderCalls
		for _, attempt := range result.Attempts {
			attempt.QueryIndex = query
			batch.Attempts = append(batch.Attempts, attempt)
		}
		batch.Providers = append(batch.Providers, result.Providers...)
		for _, url := range result.Sources {
			key, _ := canonicalWebSearchURL(url)
			if !sourceSeen[key] {
				sourceSeen[key] = true
				batch.Sources = append(batch.Sources, url)
			}
		}
		if result.Content != "" && len(result.Results) == 0 {
			batch.Content += fmt.Sprintf("Query: %s\n%s\n", candidates[query].Query, result.Content)
		}
		if status == "ok" {
			if batch.Status != "ok" {
				batch.Provider = result.Provider
				batch.ProviderType = result.ProviderType
				batch.SelectedQuery = candidates[query].Query
			}
			batch.FallbackUsed = batch.FallbackUsed || result.FallbackUsed
			batch.Status = "ok"
		} else if batch.Status != "ok" {
			if status == "budget_exhausted" || status == "timeout" {
				batch.Status = status
			} else {
				batch.Status = mergeWebSearchOutcome(batch.Status, status)
			}
		}
	}
	// Interleave hits by job so the first page cannot privilege the first
	// query or engine merely because its results were appended first. Both
	// lanes often return the same page: keep its first occurrence and fold the
	// duplicate into it (snippet and where else it was found) instead of
	// dropping it silently.
	hitSeen := map[string]int{}
	for depth := 0; ; depth++ {
		added := false
		for _, result := range results {
			if depth >= len(result.Results) {
				continue
			}
			added = true
			hit := result.Results[depth]
			key, _ := canonicalWebSearchURL(hit.URL)
			if kept, ok := hitSeen[key]; ok && key != "" {
				batch.Results[kept] = mergeWebSearchHit(batch.Results[kept], hit)
				continue
			}
			hitSeen[key] = len(batch.Results)
			batch.Results = append(batch.Results, hit)
		}
		if !added {
			break
		}
	}
	return finishExplorationResult(batch), nil
}

// mergeWebSearchHit 把同一网址的另一条命中并进先出现的那条：摘要不同就接在后面，
// 记下它还出现在哪个查询、哪个搜索源。
func mergeWebSearchHit(kept, dup WebSearchHit) WebSearchHit {
	if snippet := strings.TrimSpace(dup.Snippet); snippet != "" && !strings.Contains(kept.Snippet, snippet) {
		if strings.TrimSpace(kept.Snippet) == "" {
			kept.Snippet = snippet
		} else {
			kept.Snippet += "\n" + snippet
		}
	}
	if kept.Title == "" {
		kept.Title = dup.Title
	}
	if kept.PublishedAt == "" {
		kept.PublishedAt = dup.PublishedAt
	}
	if from := strings.TrimSpace(strings.Trim(dup.Provider+": "+dup.Query, ": ")); from != "" && (dup.Provider != kept.Provider || dup.Query != kept.Query) {
		kept.AlsoFrom = append(kept.AlsoFrom, from)
	}
	return kept
}

// webSearchProviderLanes 把免费搜索引擎按顺序交错分成两条并行通道（默认是
// Google+DuckDuckGo 与 Bing），每条通道内部仍按原顺序回退。单个引擎常把
// 旧爆料排在前面，另一家拿到结果就停的话，新发布的官方页面根本进不了候选。
// 配置里有付费 API 时保持单通道：交错会改变用户排好的优先级，还会多花钱。
func webSearchProviderLanes(providers []WebSearchProviderConfig) [][]WebSearchProviderConfig {
	var enabled []WebSearchProviderConfig
	for _, provider := range providers {
		if provider.Disabled {
			continue
		}
		if provider.Type != WebSearchProviderSearchEngine && provider.Type != "browser" {
			return [][]WebSearchProviderConfig{providers}
		}
		enabled = append(enabled, provider)
	}
	if len(enabled) < 2 {
		return [][]WebSearchProviderConfig{providers}
	}
	lanes := make([][]WebSearchProviderConfig, 2)
	for i, provider := range enabled {
		lanes[i%2] = append(lanes[i%2], provider)
	}
	return lanes
}

func searchHits(content, query, provider string) []WebSearchHit {
	var payload struct {
		Results []struct {
			Title, URL, Snippet, Content string
			PublishedDate                string `json:"published_date"`
		} `json:"results"`
	}
	if json.Unmarshal([]byte(content), &payload) != nil {
		return nil
	}
	hits := make([]WebSearchHit, 0, len(payload.Results))
	for _, result := range payload.Results {
		if result.URL == "" {
			continue
		}
		hits = append(hits, WebSearchHit{Title: result.Title, URL: result.URL, Snippet: strings.TrimSpace(firstNonEmpty(result.Snippet, result.Content)), PublishedAt: result.PublishedDate, Query: query, Provider: provider})
	}
	return hits
}
