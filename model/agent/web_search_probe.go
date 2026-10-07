// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type SearchProviderTestResult struct {
	Content     string `json:"content,omitempty"`
	DurationMS  int64  `json:"duration_ms"`
	HTTPStatus  int    `json:"http_status,omitempty"`
	ResultCount int    `json:"result_count"`
	Error       string `json:"error,omitempty"`
}

type searchProbeTransport struct {
	base   http.RoundTripper
	status atomic.Int64
}

func (transport *searchProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if response != nil {
		transport.status.Store(int64(response.StatusCode))
	}
	return response, err
}

// ProbeWebSearchProvider executes a draft once. It neither persists settings
// nor retries other sources, and uses the same adapter as actual bot searches.
func ProbeWebSearchProvider(ctx context.Context, provider WebSearchProviderConfig, query string, options WebSearchToolOptions) SearchProviderTestResult {
	started := time.Now()
	result := SearchProviderTestResult{}
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > maximumWebSearchQueryRunes {
		result.Error = "测试搜索词应为 1–512 字"
		return result
	}
	config, err := NormalizeWebSearchConfig(WebSearchConfig{Providers: []WebSearchProviderConfig{provider}})
	if err != nil {
		result.Error = safeWebSearchError(err)
		return result
	}
	provider = config.Providers[0]
	apiKey := strings.TrimSpace(options.APIKeys[provider.Name])
	if apiKey == "" && provider.APIKeyEnv != "" {
		apiKey = strings.TrimSpace(os.Getenv(provider.APIKeyEnv))
	}
	client := http.Client{}
	if options.Client != nil {
		client = *options.Client
	}
	transport := &searchProbeTransport{base: client.Transport}
	if transport.base == nil {
		transport.base = http.DefaultTransport
	}
	client.Transport = transport
	renderer := options.Renderer
	if renderer == nil && (provider.Type == "browser" || provider.Type == WebSearchProviderSearchEngine) {
		renderer = NewSandboxedHeadlessBrowser(SandboxedBrowserConfig{Window: BrowserWindowHidden})
	}
	tool := &WebSearchTool{client: &client, renderer: renderer}
	testCtx, cancel := context.WithTimeout(ctx, time.Duration(provider.TimeoutMS)*time.Millisecond)
	defer cancel()
	content, err := tool.runProvider(testCtx, provider, query, apiKey)
	result.DurationMS = time.Since(started).Milliseconds()
	result.HTTPStatus = int(transport.status.Load())
	if err == nil && strings.TrimSpace(content) == "" {
		err = errors.New("搜索服务没有返回内容")
	}
	if err != nil {
		result.Error = safeWebSearchError(err, apiKey)
		return result
	}
	if apiKey != "" {
		content = strings.ReplaceAll(content, apiKey, "[redacted]")
	}
	result.ResultCount = len(searchHits(content, query, provider.Name))
	if result.ResultCount == 0 {
		result.ResultCount = len(webSearchResultSources(content))
	}
	result.Content = truncateRunes(strings.TrimSpace(content), 6000)
	return result
}
