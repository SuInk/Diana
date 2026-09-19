// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 生图、改图和 embedding 不走文本生成那条链，以前响应里压根没有用量，token
// 统计里这几类调用是空白。上游报了就要原样带回来。

func TestOpenAICompatibleImageResponseCarriesUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"YWJjZA=="}],"usage":{"input_tokens":50,"output_tokens":1056,"total_tokens":1106,"input_tokens_details":{"cached_tokens":8}}}`))
	}))
	defer server.Close()

	resp, err := GenerateImage(context.Background(), ProviderConfig{
		Provider: ProviderOpenAICompatible, APIKey: "k", BaseURL: server.URL + "/v1", Model: "gpt-test", ImageModel: "gpt-image-2",
	}, ImageGenerateRequest{Prompt: "画一只猫"}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{InputTokens: 50, OutputTokens: 1056, TotalTokens: 1106, CachedInputTokens: 8}
	if resp.Usage != want {
		t.Fatalf("usage = %#v, want %#v", resp.Usage, want)
	}
}

// 按张计费的中转不报用量：图照常返回，用量为零值，由上层标成 usage_missing。
func TestOpenAICompatibleImageResponseWithoutUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"YWJjZA=="}]}`))
	}))
	defer server.Close()

	resp, err := GenerateImage(context.Background(), ProviderConfig{
		Provider: ProviderOpenAICompatible, APIKey: "k", BaseURL: server.URL + "/v1", Model: "gpt-test", ImageModel: "gpt-image-2",
	}, ImageGenerateRequest{Prompt: "画一只猫"}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Images) != 1 || resp.Usage != (Usage{}) {
		t.Fatalf("response = %#v", resp)
	}
}

// Gemini 要 n 张就发 n 次请求，用量得全部加起来。
func TestGeminiImageUsageSumsEveryRequest(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G'}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{"parts": []any{
					map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(png)}},
				}},
				"finishReason": "STOP",
			}},
			"usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 1290, "totalTokenCount": 1300},
		})
	}))
	defer server.Close()

	cfg := ProviderConfig{Provider: ProviderGemini, APIKey: "k", BaseURL: server.URL, ImageModel: "gemini-3.1-flash-image"}
	resp, err := GenerateImage(context.Background(), cfg, ImageGenerateRequest{Prompt: "一只橘猫", N: 2})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(resp.Images) != 2 {
		t.Fatalf("requests = %d images = %d", requests, len(resp.Images))
	}
	want := Usage{InputTokens: 20, OutputTokens: 2580, TotalTokens: 2600}
	if resp.Usage != want {
		t.Fatalf("usage = %#v, want %#v", resp.Usage, want)
	}
}

func TestEmbedTextsWithUsageReadsPromptTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]},{"index":1,"embedding":[0.3,0.4]}],"usage":{"prompt_tokens":12,"total_tokens":12}}`))
	}))
	defer server.Close()

	vectors, usage, err := EmbedTextsWithUsage(context.Background(), ProviderConfig{
		Provider: ProviderOpenAICompatible, APIKey: "k", BaseURL: server.URL + "/v1", Model: "text-embedding-3-small",
	}, []string{"甲", "乙"}, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 || usage.InputTokens != 12 || usage.TotalTokens != 12 {
		t.Fatalf("vectors = %d usage = %#v", len(vectors), usage)
	}
}
