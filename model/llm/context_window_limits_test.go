// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "testing"

// 落库时必须剥掉推断出来的窗口值，只留用户真填的。写死进库以后改推断表就追不
// 回来——历史上那次 16K 窗口把老部署永久钉住，就是这么来的。
func TestWithoutRedundantContextLimitsStripsDerivedValues(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "gp5.5"}.WithDefaults()
	if cfg.ContextWindowTokens == 0 {
		t.Fatal("WithDefaults must derive a context window for this fixture")
	}
	stripped := cfg.WithoutRedundantContextLimits()
	if stripped.ContextWindowTokens != 0 || stripped.MaxContextTokens != 0 {
		t.Fatalf("derived limits survived: %d / %d", stripped.ContextWindowTokens, stripped.MaxContextTokens)
	}
}

// 用户自己填的窗口不能被当成派生值剥掉。
func TestWithoutRedundantContextLimitsKeepsExplicitValues(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "gp5.5"}.WithDefaults()
	cfg.ContextWindowTokens = 4096
	cfg.MaxContextTokens = 2048
	stripped := cfg.WithoutRedundantContextLimits()
	if stripped.ContextWindowTokens != 4096 || stripped.MaxContextTokens != 2048 {
		t.Fatalf("explicit limits were stripped: %d / %d", stripped.ContextWindowTokens, stripped.MaxContextTokens)
	}
}

// 配置集版本要逐档处理。
func TestProfileSetWithoutRedundantContextLimits(t *testing.T) {
	set := NewProfileSet(ProviderConfig{Provider: ProviderOpenAICompatible, Model: "gp5.5"}.WithDefaults())
	stripped := set.WithoutRedundantContextLimits()
	for _, profile := range stripped.Profiles {
		if profile.Config.ContextWindowTokens != 0 {
			t.Fatalf("profile %q kept a derived window: %d", profile.ID, profile.Config.ContextWindowTokens)
		}
	}
}
