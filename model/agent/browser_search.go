// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"net/url"
	"strings"
)

// Browser sources use the same result extraction, verification and redirect
// handling as the existing search engine implementation.
func (t *WebSearchTool) runBrowserSearch(ctx context.Context, provider WebSearchProviderConfig, query string) (string, error) {
	if !strings.Contains(provider.URL, SearchEngineQueryPlaceholder) {
		if engine, ok := searchEngines[provider.Tool]; !ok || engine.searchURL != provider.URL {
			u, err := url.Parse(provider.URL)
			if err != nil {
				return "", err
			}
			params := u.Query()
			params.Set(provider.QueryParam, SearchEngineQueryPlaceholder)
			u.RawQuery = params.Encode()
			provider.URL = strings.ReplaceAll(u.String(), "%7Bquery%7D", SearchEngineQueryPlaceholder)
			provider.Tool = ""
		}
	}
	return t.runSearchEngine(ctx, provider, query)
}
