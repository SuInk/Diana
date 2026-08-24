// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "testing"

// 落库时剥掉「和兜底常量一模一样」的窗口值。存进去以后兜底常量再变就追不回来——
// 历史上那次 16K 窗口把老部署永久钉住，就是这么来的。
//
// 窗口只认手填之后，能被剥的就只剩这一种情况了：其余数值一律当成用户真填的留着，
// 包括老版本 WithDefaults 写进去的那些——它们至少让老部署的实际窗口保持不变。
func TestWithoutRedundantContextLimitsStripsTheFallbackConstant(t *testing.T) {
	cfg := ProviderConfig{
		Provider:            ProviderOpenAICompatible,
		Model:               "gpt-5.5",
		ContextWindowTokens: DefaultContextWindowTokens,
		MaxContextTokens:    DefaultContextWindowTokens,
	}
	stripped := cfg.WithoutRedundantContextLimits()
	if stripped.ContextWindowTokens != 0 || stripped.MaxContextTokens != 0 {
		t.Fatalf("fallback constant survived: %d / %d", stripped.ContextWindowTokens, stripped.MaxContextTokens)
	}
	if window := stripped.ContextWindowTokensWithDefault(); window != DefaultContextWindowTokens {
		t.Fatalf("effective window changed after stripping: %d", window)
	}
}

// 其余数值一律留着：现在分不出「用户填的」和「老版本推断写进去的」，而留着至少
// 让老部署的实际窗口保持不变。
func TestWithoutRedundantContextLimitsKeepsLegacyDerivedValues(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "gpt-5.5", ContextWindowTokens: 400000}
	if stripped := cfg.WithoutRedundantContextLimits(); stripped.ContextWindowTokens != 400000 {
		t.Fatalf("window = %d", stripped.ContextWindowTokens)
	}
}

// 用户自己填的窗口不能被当成派生值剥掉。
func TestWithoutRedundantContextLimitsKeepsExplicitValues(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "gpt-5.5"}
	cfg.ContextWindowTokens = 4096
	cfg.MaxContextTokens = 2048
	stripped := cfg.WithoutRedundantContextLimits()
	if stripped.ContextWindowTokens != 4096 || stripped.MaxContextTokens != 2048 {
		t.Fatalf("explicit limits were stripped: %d / %d", stripped.ContextWindowTokens, stripped.MaxContextTokens)
	}
}

// 配置集版本要逐档处理。
func TestProfileSetWithoutRedundantContextLimits(t *testing.T) {
	set := NewProfileSet(ProviderConfig{
		Provider: ProviderOpenAICompatible, Model: "gpt-5.5",
		ContextWindowTokens: DefaultContextWindowTokens, MaxContextTokens: DefaultContextWindowTokens,
	})
	stripped := set.WithoutRedundantContextLimits()
	for _, profile := range stripped.Profiles {
		if profile.Config.ContextWindowTokens != 0 {
			t.Fatalf("profile %q kept a derived window: %d", profile.ID, profile.Config.ContextWindowTokens)
		}
	}
}
