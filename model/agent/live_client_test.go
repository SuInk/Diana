// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func liveAgentClient(t *testing.T) llm.LLMClient {
	t.Helper()
	if os.Getenv("DIANA_LIVE_LLM") != "1" {
		t.Skip("set DIANA_LIVE_LLM=1 and DIANA_TEST_LLM_API_KEY to run this against a real model")
	}
	if raw := os.Getenv("DIANA_TEST_LLM_CONFIG_JSON"); raw != "" {
		var cfg llm.ProviderConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal("invalid live provider config JSON")
		}
		cfg.Timeout = 120 * time.Second
		client, err := llm.NewClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	apiKey := strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY"))
	if apiKey == "" {
		t.Skip("DIANA_TEST_LLM_API_KEY is empty")
	}
	model := strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_MODEL"))
	if model == "" {
		t.Skip("DIANA_TEST_LLM_MODEL is empty")
	}
	provider := llm.Provider(strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_PROVIDER")))
	if provider == "" {
		provider = llm.ProviderOpenAICompatible
	}
	client, err := llm.NewClient(llm.ProviderConfig{
		Provider: provider,
		APIKey:   apiKey,
		BaseURL:  strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_BASE_URL")),
		Model:    model,
		APIStyle: llm.APIStyle(strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_STYLE"))),
		Timeout:  120 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
