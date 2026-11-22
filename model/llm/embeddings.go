// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/netguard"
	"google.golang.org/genai"
)

// EmbedTextsWithUsage 和 EmbedTexts 一样，另外带回上游报的 token 用量，供用量统计。
func EmbedTextsWithUsage(ctx context.Context, cfg ProviderConfig, texts []string, opts ...ClientOption) ([][]float32, Usage, error) {
	cfg = cfg.WithDefaults()
	RegisterProviderSecrets(cfg)
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, Usage{}, ErrMissingModel
	}
	if len(texts) == 0 {
		return nil, Usage{}, nil
	}
	options := clientOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	if cfg.Provider == ProviderGemini {
		return embedTextsGemini(ctx, cfg, texts, options)
	}
	body, err := json.Marshal(map[string]any{"model": cfg.Model, "input": texts})
	if err != nil {
		return nil, Usage{}, err
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	requestURL, err := joinOpenAICompatibleURL(baseURL, "/embeddings")
	if err != nil {
		return nil, Usage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	for name, value := range normalizeHeaders(cfg.NormalizedHeaders()) {
		req.Header.Set(name, value)
	}
	if userAgent := cfg.UserAgentWithDefault(); strings.TrimSpace(userAgent) != "" {
		req.Header.Set("User-Agent", userAgent)
	}

	client := newTextHTTPClient(httpClientWithRequestLimits(options.httpClient, cfg), cfg)
	resp, err := client.Do(req)
	if err != nil {
		return nil, Usage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := netguard.ReadResponseBody(resp, 64<<20)
	if err != nil {
		return nil, Usage{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, Usage{}, fmt.Errorf("llm: embeddings status %d: %s", resp.StatusCode, truncateForError(payload))
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage any `json:"usage,omitempty"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, Usage{}, fmt.Errorf("llm: embeddings decode: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, Usage{}, fmt.Errorf("llm: embeddings returned %d vectors for %d inputs", len(parsed.Data), len(texts))
	}
	sort.Slice(parsed.Data, func(left, right int) bool { return parsed.Data[left].Index < parsed.Data[right].Index })
	vectors := make([][]float32, len(parsed.Data))
	for position, item := range parsed.Data {
		if len(item.Embedding) == 0 {
			return nil, Usage{}, fmt.Errorf("llm: embeddings item %d is empty", position)
		}
		vectors[position] = item.Embedding
	}
	return vectors, usageFromPayload(parsed.Usage), nil
}

// embedTextsGemini 走 Gemini 自己的 batchEmbedContents。
//
// 上面那条路只会说 OpenAI 的 /v1/embeddings：Gemini 配置档不填地址时请求被发到
// api.openai.com，还把 Gemini 的 key 当 Bearer 带过去，回 401，语义检索于是一条
// 向量都写不进去，只在日志里报错。聊天和生图本来就走 genai SDK，这里用同一个
// 客户端，key 和地址的处理与它们一致。
func embedTextsGemini(ctx context.Context, cfg ProviderConfig, texts []string, options clientOptions) ([][]float32, Usage, error) {
	client, err := newGeminiClient(cfg, newTextHTTPClient(httpClientWithRequestLimits(options.httpClient, cfg), cfg))
	if err != nil {
		return nil, Usage{}, err
	}
	contents := make([]*genai.Content, len(texts))
	for index, text := range texts {
		contents[index] = genai.NewContentFromText(text, genai.RoleUser)
	}
	resp, err := client.client.Models.EmbedContent(ctx, cfg.Model, contents, nil)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("llm: gemini embeddings: %w", err)
	}
	if len(resp.Embeddings) != len(texts) {
		return nil, Usage{}, fmt.Errorf("llm: embeddings returned %d vectors for %d inputs", len(resp.Embeddings), len(texts))
	}
	vectors := make([][]float32, len(resp.Embeddings))
	for position, item := range resp.Embeddings {
		if item == nil || len(item.Values) == 0 {
			return nil, Usage{}, fmt.Errorf("llm: embeddings item %d is empty", position)
		}
		vectors[position] = item.Values
	}
	// Gemini API 的 batchEmbedContents 不报 token 用量。
	return vectors, Usage{}, nil
}

func truncateForError(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if len(text) > 512 {
		text = text[:512] + "…"
	}
	return text
}
