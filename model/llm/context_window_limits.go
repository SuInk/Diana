// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

// WithoutRedundantContextLimits 把与推断结果完全一致的窗口字段清零。
//
// 落库时用它剥掉 WithDefaults 派生出来的值，配置里就只留用户自己填的、或同步模型
// 列表带回来的真实窗口。读取时 WithDefaults 会重新推断，兜底值和推断表以后再变，
// 老部署也能跟着变，不会被某个版本的默认值永久粘住。
func (cfg ProviderConfig) WithoutRedundantContextLimits() ProviderConfig {
	probe := cfg
	probe.ContextWindowTokens = 0
	probe.MaxContextTokens = 0
	derived := probe.WithDefaults()
	if cfg.ContextWindowTokens == derived.ContextWindowTokens {
		cfg.ContextWindowTokens = 0
	}
	if cfg.MaxContextTokens == derived.MaxContextTokens {
		cfg.MaxContextTokens = 0
	}
	return cfg
}

// WithoutRedundantContextLimits 对配置集内每个配置档剥掉派生窗口值。
func (s ProfileSet) WithoutRedundantContextLimits() ProfileSet {
	profiles := make([]Profile, len(s.Profiles))
	copy(profiles, s.Profiles)
	for index, profile := range profiles {
		profiles[index].Config = profile.Config.WithoutRedundantContextLimits()
	}
	s.Profiles = profiles
	return s
}
