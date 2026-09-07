// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"testing"

	"google.golang.org/genai"
)

func TestGeminiOutputTokenLimit(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		value   int64
		want    int32
		wantErr bool
	}{
		{name: "zero", value: 0, want: 0},
		{name: "positive", value: 4096, want: 4096},
		{name: "maximum", value: maxGeminiOutputTokens, want: int32(maxGeminiOutputTokens)},
		{name: "negative", value: -1, wantErr: true},
		{name: "overflow", value: maxGeminiOutputTokens + 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := geminiOutputTokenLimit(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("geminiOutputTokenLimit(%d) error = %v, wantErr %v", test.value, err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("geminiOutputTokenLimit(%d) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}

func TestNormalizeGeminiBaseURLAcceptsRootAndVersionedPaths(t *testing.T) {
	for input, want := range map[string]string{
		"https://example.com":                "https://example.com",
		"https://example.com/v1beta":         "https://example.com",
		"https://example.com/v1beta/models/": "https://example.com",
	} {
		if got := normalizeGeminiBaseURL(input); got != want {
			t.Fatalf("normalizeGeminiBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGeminiUsageToleratesMissingMetadata(t *testing.T) {
	t.Parallel()

	for name, response := range map[string]*genai.GenerateContentResponse{
		"nil response": nil,
		"nil metadata": {},
	} {
		t.Run(name, func(t *testing.T) {
			if got := geminiUsage(response); got != (Usage{}) {
				t.Fatalf("geminiUsage() = %#v, want zero usage", got)
			}
		})
	}
}

func TestGeminiUsageReadsMetadata(t *testing.T) {
	t.Parallel()

	response := &genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:        11,
		CandidatesTokenCount:    7,
		TotalTokenCount:         18,
		CachedContentTokenCount: 3,
	}}
	want := Usage{InputTokens: 11, OutputTokens: 7, TotalTokens: 18, CachedInputTokens: 3}
	if got := geminiUsage(response); got != want {
		t.Fatalf("geminiUsage() = %#v, want %#v", got, want)
	}
}
