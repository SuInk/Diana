// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// EmbedTexts 调用 OpenAI 兼容的 /embeddings 接口把文本批量转成向量。
// 返回的向量与输入一一对应。语义检索按可选能力接入:配置档缺失或调用
// 失败时调用方直接退回词面检索,所以这里只做一次干净的请求,不做重试。
func EmbedTexts(ctx context.Context, cfg ProviderConfig, texts []string, opts ...ClientOption) ([][]float32, error) {
	cfg = cfg.WithDefaults()
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, ErrMissingModel
	}
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{"model": cfg.Model, "input": texts})
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	requestURL, err := joinOpenAICompatibleURL(baseURL, "/embeddings")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	for name, value := range normalizeHeaders(cfg.NormalizedHeaders()) {
		req.Header.Set(name, value)
	}
	if userAgent := cfg.UserAgentWithDefault(); strings.TrimSpace(userAgent) != "" {
		req.Header.Set("User-Agent", userAgent)
	}

	options := clientOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	client := newTextHTTPClient(options.httpClient, cfg)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm: embeddings status %d: %s", resp.StatusCode, truncateForError(payload))
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("llm: embeddings decode: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("llm: embeddings returned %d vectors for %d inputs", len(parsed.Data), len(texts))
	}
	sort.Slice(parsed.Data, func(left, right int) bool { return parsed.Data[left].Index < parsed.Data[right].Index })
	vectors := make([][]float32, len(parsed.Data))
	for position, item := range parsed.Data {
		if len(item.Embedding) == 0 {
			return nil, fmt.Errorf("llm: embeddings item %d is empty", position)
		}
		vectors[position] = item.Embedding
	}
	return vectors, nil
}

func truncateForError(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if len(text) > 512 {
		text = text[:512] + "…"
	}
	return text
}
