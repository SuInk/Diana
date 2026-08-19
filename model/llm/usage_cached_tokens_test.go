// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "testing"

func TestUsageFromPayloadParsesCachedTokens(t *testing.T) {
	cases := []struct {
		name    string
		payload any
		want    Usage
	}{
		{
			name: "chat completions prompt_tokens_details",
			payload: map[string]any{
				"prompt_tokens":     float64(11056),
				"completion_tokens": float64(229),
				"total_tokens":      float64(11285),
				"prompt_tokens_details": map[string]any{
					"cached_tokens": float64(10240),
				},
			},
			want: Usage{InputTokens: 11056, OutputTokens: 229, TotalTokens: 11285, CachedInputTokens: 10240},
		},
		{
			name: "responses input_tokens_details",
			payload: map[string]any{
				"input_tokens":  float64(9000),
				"output_tokens": float64(120),
				"total_tokens":  float64(9120),
				"input_tokens_details": map[string]any{
					"cached_tokens": float64(8704),
				},
			},
			want: Usage{InputTokens: 9000, OutputTokens: 120, TotalTokens: 9120, CachedInputTokens: 8704},
		},
		{
			name: "no cache details",
			payload: map[string]any{
				"prompt_tokens":     float64(500),
				"completion_tokens": float64(50),
			},
			want: Usage{InputTokens: 500, OutputTokens: 50},
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := usageFromPayload(item.payload); got != item.want {
				t.Fatalf("usageFromPayload = %+v, want %+v", got, item.want)
			}
		})
	}
}
