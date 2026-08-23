// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "testing"

// 一个 provider 下面挂着几十个模型，窗口从 8K 到 1M 都有。所以窗口必须按当前模型
// 现算，而且优先级要固定：用户手填 > 模型清单 > 名称推断 > 兜底。
func TestResolveContextWindowTokensPrefersUserThenModelList(t *testing.T) {
	base := ProviderConfig{
		Provider: ProviderOpenAICompatible,
		Model:    "gpt-5.6-sol",
		Models: []ModelInfo{
			{ID: "gpt-5.6-sol", ContextWindowTokens: 1050000},
			{ID: "tiny-local", ContextWindowTokens: 8192},
		},
	}

	window, source := base.ResolveContextWindowTokens()
	if window != 1050000 || source != ContextWindowSourceModelList {
		t.Fatalf("model list = %d/%s", window, source)
	}

	// 换模型立刻跟着换，不需要谁去改配置。
	switched := base
	switched.Model = "tiny-local"
	if window, source = switched.ResolveContextWindowTokens(); window != 8192 || source != ContextWindowSourceModelList {
		t.Fatalf("switched = %d/%s", window, source)
	}

	// 用户填了就以用户为准，任何目录都不覆盖。
	override := base
	override.ContextWindowTokens = 32768
	if window, source = override.ResolveContextWindowTokens(); window != 32768 || source != ContextWindowSourceUser {
		t.Fatalf("override = %d/%s", window, source)
	}

	// 清单里没有这个模型时退回名称推断。gpt-5 系列的推断值正是那个 400000——
	// 它是「推断」，不该被当成用户设置写进配置。
	inferred := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "gpt-5.6-sol"}
	if window, source = inferred.ResolveContextWindowTokens(); window != 400000 || source != ContextWindowSourceInferred {
		t.Fatalf("inferred = %d/%s", window, source)
	}

	// 都推断不出才用兜底常量。
	unknown := ProviderConfig{Provider: ProviderOpenAICompatible, Model: "some-local-build"}
	if window, source = unknown.ResolveContextWindowTokens(); window != DefaultContextWindowTokens || source != ContextWindowSourceFallback {
		t.Fatalf("fallback = %d/%s", window, source)
	}
}

// 聚合网关普遍给模型名加供应商命名空间，清单和配置里可能一个带一个不带。
func TestModelInfoForIgnoresProviderNamespace(t *testing.T) {
	cfg := ProviderConfig{
		Model:  "openai/gpt-4o",
		Models: []ModelInfo{{ID: "gpt-4o", ContextWindowTokens: 128000}},
	}
	info, ok := cfg.ModelInfoFor(cfg.Model)
	if !ok || info.ContextWindowTokens != 128000 {
		t.Fatalf("info = %+v ok=%v", info, ok)
	}
}

// WithDefaults 不再把推断值写进字段：落库的只有用户自己填的那份。
func TestWithDefaultsKeepsContextOverridesUntouched(t *testing.T) {
	cfg := ProviderConfig{Provider: ProviderAnthropic, Model: "claude-sonnet-4-5"}.WithDefaults()
	if cfg.ContextWindowTokens != 0 || cfg.MaxContextTokens != 0 {
		t.Fatalf("WithDefaults materialised inferred limits: %d/%d", cfg.ContextWindowTokens, cfg.MaxContextTokens)
	}
	if cfg.MaxContextTokensWithDefault() != 200000 {
		t.Fatalf("budget = %d", cfg.MaxContextTokensWithDefault())
	}
	// 用户设的请求上限保留，但不会超过窗口。
	capped := ProviderConfig{Provider: ProviderAnthropic, Model: "claude-sonnet-4-5", MaxContextTokens: 900000}
	if capped.MaxContextTokensWithDefault() != 200000 {
		t.Fatalf("超窗口的上限没有收敛: %d", capped.MaxContextTokensWithDefault())
	}
}
