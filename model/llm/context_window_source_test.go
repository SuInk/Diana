// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "testing"

// 窗口只认两个来源：用户手填和兜底常量。模型清单和模型名推断都不再参与计算——
// 它们是第三方数据和粗粒度猜测，在界面上却和用户手填的值长得一样，分不清就不猜。
func TestResolveContextWindowTokensOnlyUsesExplicitValue(t *testing.T) {
	catalog := ProviderConfig{
		Provider: ProviderOpenAICompatible,
		Model:    "gpt-5.6-sol",
		Models: []ModelInfo{
			{ID: "gpt-5.6-sol", ContextWindowTokens: 1050000},
			{ID: "tiny-local", ContextWindowTokens: 8192},
		},
	}
	window, source := catalog.ResolveContextWindowTokens()
	if window != DefaultContextWindowTokens || source != ContextWindowSourceFallback {
		t.Fatalf("catalog window leaked into the budget: %d/%s", window, source)
	}
	// 换模型也不影响：窗口和模型无关了。
	switched := catalog
	switched.Model = "tiny-local"
	if got := switched.ContextWindowTokensWithDefault(); got != DefaultContextWindowTokens {
		t.Fatalf("switched = %d", got)
	}
	// 名字像大窗口模型也一样，不猜。
	named := ProviderConfig{Provider: ProviderAnthropic, Model: "claude-sonnet-4-5"}
	if got := named.ContextWindowTokensWithDefault(); got != DefaultContextWindowTokens {
		t.Fatalf("named = %d", got)
	}

	override := catalog
	override.ContextWindowTokens = 32768
	if window, source = override.ResolveContextWindowTokens(); window != 32768 || source != ContextWindowSourceUser {
		t.Fatalf("override = %d/%s", window, source)
	}
}

// 清单里的窗口仍然读得出来，但只作为界面上的参考值。
func TestCatalogContextWindowTokensIsReferenceOnly(t *testing.T) {
	cfg := ProviderConfig{
		Model:  "openai/gpt-4o",
		Models: []ModelInfo{{ID: "gpt-4o", ContextWindowTokens: 128000}},
	}
	if got := cfg.CatalogContextWindowTokens(cfg.Model); got != 128000 {
		t.Fatalf("catalog reference = %d", got)
	}
	if got := cfg.CatalogContextWindowTokens("not-listed"); got != 0 {
		t.Fatalf("unknown model reference = %d", got)
	}
	// 参考值不参与计算。
	if got := cfg.ContextWindowTokensWithDefault(); got != DefaultContextWindowTokens {
		t.Fatalf("reference leaked into the budget: %d", got)
	}
}

// WithDefaults 不把任何推断写进字段：落库的只有用户自己填的那份。
func TestWithDefaultsKeepsContextOverridesUntouched(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderAnthropic, Model: "claude-sonnet-4-5"}.WithDefaults()
	if cfg.ContextWindowTokens != 0 || cfg.MaxContextTokens != 0 {
		t.Fatalf("WithDefaults materialised limits: %d/%d", cfg.ContextWindowTokens, cfg.MaxContextTokens)
	}
	if cfg.MaxContextTokensWithDefault() != DefaultContextWindowTokens {
		t.Fatalf("budget = %d", cfg.MaxContextTokensWithDefault())
	}
	// 用户设的请求上限保留，但不会超过窗口。
	capped := ProviderConfig{Provider: ProviderAnthropic, ContextWindowTokens: 200000, MaxContextTokens: 900000}
	if capped.MaxContextTokensWithDefault() != 200000 {
		t.Fatalf("超窗口的上限没有收敛: %d", capped.MaxContextTokensWithDefault())
	}
}
