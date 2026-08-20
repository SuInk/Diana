// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "testing"

func TestKnownContextWindowTokensCoversCommonFamilies(t *testing.T) {
	cases := map[string]int64{
		"claude-sonnet-4-5":          200000,
		"claude-opus-4-6":            200000,
		"anthropic/claude-haiku-4-5": 200000,
		"gemini-2.5-flash":           1000000,
		"gpt-4o-mini":                128000,
		"openai/gpt-5.2":             400000,
		"deepseek-chat":              128000,
		"qwen3-max":                  128000,
		"moonshot-v1-128k":           256000,
		"accounts/x/models/glm-4.6":  128000,
		"某个没听过的模型":                   0,
		"":                           0,
	}
	for model, want := range cases {
		if got := KnownContextWindowTokens(model); got != want {
			t.Fatalf("KnownContextWindowTokens(%q) = %d, want %d", model, got, want)
		}
	}
}

func TestProviderConfigInfersWindowFromModelName(t *testing.T) {
	// 目录取不到窗口时按模型名推断。
	cfg := ProviderConfig{Provider: ProviderAnthropic, Model: "claude-sonnet-4-5"}.WithDefaults()
	if cfg.ContextWindowTokens != 200000 || cfg.MaxContextTokens != 200000 {
		t.Fatalf("claude window = %d/%d, want 200000", cfg.ContextWindowTokens, cfg.MaxContextTokens)
	}
	// 未知模型走兜底值。
	unknown := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "some-local-build"}.WithDefaults()
	if unknown.ContextWindowTokens != DefaultContextWindowTokens {
		t.Fatalf("unknown model window = %d, want the default", unknown.ContextWindowTokens)
	}
	// 用户显式配置优先于推断。
	explicit := ProviderConfig{Provider: ProviderAnthropic, Model: "claude-sonnet-4-5", ContextWindowTokens: 32768}.WithDefaults()
	if explicit.ContextWindowTokens != 32768 || explicit.MaxContextTokens != 32768 {
		t.Fatalf("explicit window overridden: %d/%d", explicit.ContextWindowTokens, explicit.MaxContextTokens)
	}
}

func TestIsContextOverflowErrorMatchesProviderWordings(t *testing.T) {
	overflow := []string{
		"This model's maximum context length is 8192 tokens",
		"error: context_length_exceeded",
		"prompt is too long: 210000 tokens > 200000 maximum",
		"input length and `max_tokens` exceed context limit",
		"请求超出模型上下文窗口",
	}
	for _, message := range overflow {
		if !IsContextOverflowError(errorString(message)) {
			t.Fatalf("expected %q to be recognized as context overflow", message)
		}
	}
	for _, message := range []string{"429 rate limit exceeded", "connection reset by peer", ""} {
		if IsContextOverflowError(errorString(message)) {
			t.Fatalf("unexpected context-overflow match for %q", message)
		}
	}
	if IsContextOverflowError(nil) {
		t.Fatal("nil error must not match")
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

func TestRequestMaxContextTokensOnlyShrinksTheBudget(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "m", APIKey: "k", ContextWindowTokens: 128000}.WithDefaults()
	messages := []Message{
		{Role: RoleSystem, Content: "系统", Priority: MessagePrioritySystem},
		{Role: RoleUser, Content: "当前问题", Priority: MessagePriorityCurrent},
	}
	shrunk := applyContextBudget(GenerateRequest{Messages: messages, MaxContextTokens: 4096}, cfg)
	if len(shrunk.Messages) == 0 {
		t.Fatal("shrunk request lost every message")
	}
	// 反向放大必须被忽略，否则调用方能绕过配置档上限。
	enlarged := applyContextBudget(GenerateRequest{Messages: messages, MaxContextTokens: 1000000}, cfg)
	if len(enlarged.Messages) != len(messages) {
		t.Fatalf("enlarge attempt changed fitting: %d messages", len(enlarged.Messages))
	}
}
