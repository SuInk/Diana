// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 上游用 Retry-After 头说明要等多久时，错误里要带出来，供降级链决定冷却多久。
func TestOpenAICompatibleErrorKeepsRetryAfterHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
	}))
	defer server.Close()

	for _, format := range []APIFormat{APIFormatChatCompletions, APIFormatResponses} {
		client, err := NewClient(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "test", Model: "gpt-test", BaseURL: server.URL + "/v1", APIFormat: format})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: RoleUser, Content: "你好"}}})
		if err == nil {
			t.Fatalf("%s: 期望 429 错误", format)
		}
		if got := RetryAfterHint(fmt.Errorf("wrapped: %w", err)); got != 42*time.Second {
			t.Fatalf("%s: RetryAfterHint = %s, err = %v", format, got, err)
		}
	}
}

func TestRetryAfterHintWithoutHeader(t *testing.T) {
	if got := RetryAfterHint(errors.New("503 Service Unavailable")); got != 0 {
		t.Fatalf("没有头时应当返回 0，实际 %s", got)
	}
	if got := RetryAfterHint(&MediaAPIError{StatusCode: 429, RetryAfter: 3 * time.Second}); got != 3*time.Second {
		t.Fatalf("MediaAPIError 的 RetryAfter 应当带出来，实际 %s", got)
	}
}
