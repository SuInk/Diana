package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// WebSearchHit is a discovery result, not proof that its page was read.
type WebSearchHit struct {
	Title       string `json:"title,omitempty"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	Query       string `json:"query,omitempty"`
	Provider    string `json:"provider,omitempty"`
}

type webSearchQueryResponse struct {
	Query    string `json:"query"`
	Status   string `json:"status"`
	Provider string `json:"provider,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Run executes model-supplied queries independently. Each query has ordered
// provider fallback; the batch shares a deadline and total provider-call budget.
func (t *WebSearchTool) Run(ctx context.Context, input map[string]any) (string, error) {
	candidates, err := webSearchCandidates(input, t.maxQueries)
	if err != nil {
		return "", err
	}
	if len(candidates) == 1 {
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
		calls = defaultWebSearchMaxProviderCalls
	}
	calls = min(calls, maximumWebSearchMaxProviderCalls)
	results := make([]webSearchResult, len(candidates))
	errors := make([]error, len(candidates))
	var wg sync.WaitGroup
	// At most four browser jobs run at once; no per-conversation search quota.
	slots := make(chan struct{}, 4)
	for i, candidate := range candidates {
		wg.Add(1)
		go func(index int, query string) {
			defer recoverGoroutinePanic("web_search_parallel")
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				errors[index] = ctx.Err()
				return
			}
			quota := calls / len(candidates)
			if index < calls%len(candidates) {
				quota++
			}
			if quota == 0 {
				results[index] = webSearchResult{Status: "budget_exhausted", StopReason: "provider_call_budget_exhausted"}
				return
			}
			worker := *t
			worker.maxProviderCalls = quota
			// Internal results are merged before enforcing the external output budget.
			worker.maxBytes = maxWebSearchResponseBytes
			raw, runErr := worker.runSearchQuery(ctx, map[string]any{"query": query})
			errors[index] = runErr
			if runErr == nil {
				errors[index] = json.Unmarshal([]byte(raw), &results[index])
			}
		}(i, candidate.Query)
	}
	wg.Wait()
	batch := webSearchResult{Status: "no_results", Strategy: "parallel_queries", StopReason: "queries_completed", Query: candidates[0].Query, Queries: candidates, Budget: webSearchBudget{MaxQueries: len(candidates), MaxProviderCalls: calls, DeadlineMS: timeout.Milliseconds()}}
	seen := map[string]bool{}
	for i, result := range results {
		status := result.Status
		if errors[i] != nil {
			status = classifyWebSearchError(errors[i], nil, ctx.Err())
		}
		batch.Searches = append(batch.Searches, webSearchQueryResponse{Query: candidates[i].Query, Status: status, Provider: result.Provider})
		if errors[i] != nil {
			batch.Searches[i].Error = safeWebSearchError(errors[i])
		}
		batch.Queries[i].Status = "attempted"
		batch.Queries[i].Outcome = status
		batch.Budget.QueriesUsed += result.Budget.QueriesUsed
		batch.Budget.ProviderCalls += result.Budget.ProviderCalls
		for _, attempt := range result.Attempts {
			attempt.QueryIndex = i
			batch.Attempts = append(batch.Attempts, attempt)
		}
		batch.Providers = append(batch.Providers, result.Providers...)
		for _, url := range result.Sources {
			key, _ := canonicalWebSearchURL(url)
			if !seen[key] {
				seen[key] = true
				batch.Sources = append(batch.Sources, url)
			}
		}
		batch.Results = append(batch.Results, result.Results...)
		if result.Content != "" && len(result.Results) == 0 {
			batch.Content += fmt.Sprintf("Query: %s\n%s\n", candidates[i].Query, result.Content)
		}
		if status == "ok" {
			if batch.Status != "ok" {
				batch.Provider = result.Provider
				batch.ProviderType = result.ProviderType
				batch.SelectedQuery = candidates[i].Query
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
	return t.formatExplorationResult(batch)
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
		hits = append(hits, WebSearchHit{Title: result.Title, URL: result.URL, Snippet: truncateText(firstNonEmpty(result.Snippet, result.Content), 600), PublishedAt: result.PublishedDate, Query: query, Provider: provider})
	}
	return hits
}
