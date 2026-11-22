// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Gemini 配置档的向量请求以前按 OpenAI 格式发，没填地址时落到 api.openai.com 回 401，
// 语义检索一条向量都写不进去。现在走 Gemini 的 batchEmbedContents，用 x-goog-api-key 鉴权。
func TestEmbedTextsGeminiUsesBatchEmbedContents(t *testing.T) {
	var gotPath, gotKey, gotAuth string
	var gotBody struct {
		Requests []struct {
			Model   string `json:"model"`
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"requests"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotAuth = r.URL.Path, r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}]}`))
	}))
	defer server.Close()
	vectors, _, err := EmbedTextsWithUsage(context.Background(), ProviderConfig{
		Provider: ProviderGemini, APIKey: "gemini-key", BaseURL: server.URL, Model: "gemini-embedding-2",
	}, []string{"凤爪味道不错", "有什么吃的推荐"}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "/models/gemini-embedding-2:batchEmbedContents") || gotKey != "gemini-key" || gotAuth != "" {
		t.Fatalf("path=%q key=%q auth=%q", gotPath, gotKey, gotAuth)
	}
	if len(gotBody.Requests) != 2 || gotBody.Requests[1].Content.Parts[0].Text != "有什么吃的推荐" {
		t.Fatalf("body = %+v", gotBody)
	}
	if len(vectors) != 2 || vectors[1][1] != float32(0.4) {
		t.Fatalf("vectors = %v", vectors)
	}
}

func TestEmbedTextsGeminiRejectsCountMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[{"values":[0.1]}]}`))
	}))
	defer server.Close()
	_, _, err := EmbedTextsWithUsage(context.Background(), ProviderConfig{
		Provider: ProviderGemini, APIKey: "k", BaseURL: server.URL, Model: "gemini-embedding-2",
	}, []string{"a", "b"}, WithHTTPClient(server.Client()))
	if err == nil || !strings.Contains(err.Error(), "1 vectors for 2 inputs") {
		t.Fatalf("err = %v", err)
	}
}
