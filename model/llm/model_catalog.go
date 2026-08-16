// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	modelsDevCatalogURL = "https://models.dev/api.json"
	modelsDevCacheTTL   = 6 * time.Hour
)

// ModelsDevCatalog adds model modalities and context/output limits that many
// OpenAI-compatible /models endpoints omit. Failures are non-fatal; unknown
// capabilities and the configured conservative context fallback remain in force.
type ModelsDevCatalog struct {
	mu        sync.Mutex
	client    *http.Client
	url       string
	fetchedAt time.Time
	providers map[string]map[string]ModelInfo
}

func NewModelsDevCatalog(client *http.Client) *ModelsDevCatalog {
	return newModelsDevCatalog(client, modelsDevCatalogURL)
}

func newModelsDevCatalog(client *http.Client, endpoint string) *ModelsDevCatalog {
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	return &ModelsDevCatalog{client: client, url: endpoint}
}

func (c *ModelsDevCatalog) Enrich(ctx context.Context, cfg ProviderConfig, models []ModelInfo) []ModelInfo {
	providers := modelsDevProviderCandidates(cfg)
	if c == nil || len(models) == 0 || len(providers) == 0 {
		return append([]ModelInfo(nil), models...)
	}
	catalog, err := c.load(ctx)
	if err != nil {
		return append([]ModelInfo(nil), models...)
	}
	out := append([]ModelInfo(nil), models...)
	for index := range out {
		for _, provider := range providers {
			info, ok := catalog[provider][out[index].ID]
			if !ok {
				continue
			}
			if out[index].Name == "" {
				out[index].Name = info.Name
			}
			if len(out[index].InputModalities) == 0 {
				out[index].InputModalities = append([]string(nil), info.InputModalities...)
			}
			if len(out[index].OutputModalities) == 0 {
				out[index].OutputModalities = append([]string(nil), info.OutputModalities...)
			}
			if out[index].ContextWindowTokens == 0 {
				out[index].ContextWindowTokens = info.ContextWindowTokens
			}
			if out[index].MaxInputTokens == 0 {
				out[index].MaxInputTokens = info.MaxInputTokens
			}
			if out[index].MaxOutputTokens == 0 {
				out[index].MaxOutputTokens = info.MaxOutputTokens
			}
			break
		}
	}
	return out
}

func (c *ModelsDevCatalog) load(ctx context.Context) (map[string]map[string]ModelInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.providers) > 0 && time.Since(c.fetchedAt) < modelsDevCacheTTL {
		return c.providers, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, modelListHTTPError{statusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	providers, err := decodeModelsDevCatalog(body)
	if err != nil {
		return nil, err
	}
	c.providers = providers
	c.fetchedAt = time.Now()
	return c.providers, nil
}

func decodeModelsDevCatalog(body []byte) (map[string]map[string]ModelInfo, error) {
	var payload map[string]struct {
		Models map[string]struct {
			Name       string `json:"name"`
			Modalities struct {
				Input  []string `json:"input"`
				Output []string `json:"output"`
			} `json:"modalities"`
			Limit struct {
				Context int64 `json:"context"`
				Input   int64 `json:"input"`
				Output  int64 `json:"output"`
			} `json:"limit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]ModelInfo, len(payload))
	for providerID, provider := range payload {
		models := make(map[string]ModelInfo, len(provider.Models))
		for modelID, model := range provider.Models {
			models[modelID] = ModelInfo{
				ID:                  modelID,
				Name:                model.Name,
				InputModalities:     normalizeModalities(model.Modalities.Input),
				OutputModalities:    normalizeModalities(model.Modalities.Output),
				ContextWindowTokens: model.Limit.Context,
				MaxInputTokens:      model.Limit.Input,
				MaxOutputTokens:     model.Limit.Output,
			}
		}
		out[providerID] = models
	}
	return out, nil
}

func modelsDevProviderCandidates(cfg ProviderConfig) []string {
	switch cfg.Provider {
	case ProviderGemini:
		return []string{"google"}
	case ProviderAnthropic:
		return []string{"anthropic"}
	case ProviderOpenAICompatible:
		if strings.TrimSpace(cfg.BaseURL) == "" {
			return []string{"openai"}
		}
		parsed, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
		if err != nil {
			return nil
		}
		host := strings.ToLower(parsed.Hostname())
		path := strings.ToLower(parsed.Path)
		switch {
		case host == "opencode.ai" && strings.Contains(path, "/go/"):
			return []string{"opencode-go", "opencode"}
		case host == "opencode.ai":
			return []string{"opencode"}
		case host == "api.openai.com":
			return []string{"openai"}
		case host == "api.deepseek.com":
			return []string{"deepseek"}
		case host == "generativelanguage.googleapis.com":
			return []string{"google"}
		case host == "openrouter.ai":
			return []string{"openrouter"}
		}
	}
	return nil
}
