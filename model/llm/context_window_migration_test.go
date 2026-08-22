// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "testing"

func TestClearLegacyContextFallbackRestoresInference(t *testing.T) {
	// 旧版本落库的配置：窗口被 16K 兜底常量钉死。
	cfg := ProviderConfig{
		Provider:            ProviderOpenAICompatible,
		Model:               "claude-sonnet-4",
		ContextWindowTokens: LegacyDefaultContextWindowTokens,
		MaxContextTokens:    LegacyDefaultContextWindowTokens,
	}
	cleared, changed := cfg.ClearLegacyContextFallback()
	if !changed {
		t.Fatal("legacy fallback not detected")
	}
	resolved := cleared.WithDefaults()
	if resolved.ContextWindowTokens != 200000 || resolved.MaxContextTokens != 200000 {
		t.Fatalf("resolved budgets = %d/%d", resolved.ContextWindowTokens, resolved.MaxContextTokens)
	}
}

func TestClearLegacyContextFallbackKeepsOtherValues(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "gpt-4o", ContextWindowTokens: 32000, MaxContextTokens: 24000}
	cleared, changed := cfg.ClearLegacyContextFallback()
	if changed {
		t.Fatal("non-legacy budgets were treated as the legacy fallback")
	}
	if cleared.ContextWindowTokens != 32000 || cleared.MaxContextTokens != 24000 {
		t.Fatalf("budgets = %d/%d", cleared.ContextWindowTokens, cleared.MaxContextTokens)
	}
}

func TestWithoutRedundantContextLimitsStripsDerivedValues(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "claude-sonnet-4"}.WithDefaults()
	if cfg.ContextWindowTokens != 200000 {
		t.Fatalf("derived window = %d", cfg.ContextWindowTokens)
	}
	stored := cfg.WithoutRedundantContextLimits()
	if stored.ContextWindowTokens != 0 || stored.MaxContextTokens != 0 {
		t.Fatalf("stored budgets = %d/%d", stored.ContextWindowTokens, stored.MaxContextTokens)
	}
	// 剥掉派生值不改变实际生效的预算。
	if resolved := stored.WithDefaults(); resolved.ContextWindowTokens != 200000 || resolved.MaxContextTokens != 200000 {
		t.Fatalf("resolved budgets = %d/%d", resolved.ContextWindowTokens, resolved.MaxContextTokens)
	}
}

func TestWithoutRedundantContextLimitsKeepsExplicitValues(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "gpt-4o", ContextWindowTokens: 131072, MaxContextTokens: 40000}
	stored := cfg.WithoutRedundantContextLimits()
	if stored.ContextWindowTokens != 131072 || stored.MaxContextTokens != 40000 {
		t.Fatalf("stored budgets = %d/%d", stored.ContextWindowTokens, stored.MaxContextTokens)
	}
}

func TestProfileSetClearLegacyContextFallback(t *testing.T) {
	set := ProfileSet{
		ActiveID: "a",
		Profiles: []Profile{
			{ID: "a", Config: ProviderConfig{Provider: ProviderOpenAICompatible, Model: "deepseek-chat", ContextWindowTokens: LegacyDefaultContextWindowTokens, MaxContextTokens: LegacyDefaultContextWindowTokens}},
			{ID: "b", Config: ProviderConfig{Provider: ProviderOpenAICompatible, Model: "gpt-4o", ContextWindowTokens: 64000}},
		},
	}
	cleared, changed := set.ClearLegacyContextFallback()
	if !changed {
		t.Fatal("profile set migration reported no change")
	}
	if cleared.Profiles[0].Config.ContextWindowTokens != 0 {
		t.Fatalf("legacy profile window = %d", cleared.Profiles[0].Config.ContextWindowTokens)
	}
	if cleared.Profiles[1].Config.ContextWindowTokens != 64000 {
		t.Fatalf("explicit profile window = %d", cleared.Profiles[1].Config.ContextWindowTokens)
	}
	// 原配置集不被就地改写。
	if set.Profiles[0].Config.ContextWindowTokens != LegacyDefaultContextWindowTokens {
		t.Fatal("source profile set was mutated")
	}
}
