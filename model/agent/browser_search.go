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
		if provider.Tool == "" {
			provider.Tool = knownBrowserSearchEngine(provider.URL, provider.QueryParam)
		}
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

// knownBrowserSearchEngine 认出手填的内置引擎地址。界面新建的浏览器来源不带
// 引擎名，不认出来就会当成自定义地址：没有时间筛选，也认不出该引擎的验证页。
func knownBrowserSearchEngine(searchURL, queryParam string) string {
	for name, engine := range searchEngines {
		if engine.searchURL == searchURL && engine.queryParam == queryParam {
			return name
		}
	}
	return ""
}
