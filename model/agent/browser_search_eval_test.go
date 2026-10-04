package agent

func browserSearchEvalConfig() WebSearchConfig {
	var providers []WebSearchProviderConfig
	for _, engine := range DefaultSearchEngines {
		providers = append(providers, WebSearchProviderConfig{Name: engine, Type: WebSearchProviderSearchEngine, Tool: engine, TimeoutMS: 25000, MaxResults: 8})
	}
	return WebSearchConfig{Providers: providers}
}
