// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

// WithoutRedundantContextLimits 把与推断结果完全一致的窗口字段清零。
//
// 落库时用它剥掉旧版本写入的派生值，配置里只留用户自己填的覆盖值。读取时通过
// ResolveContextWindowTokens 按当前模型重新计算，兜底值和推断表以后再变，老部署也
// 能跟着变，不会被某个版本的默认值永久粘住。
func (cfg ProviderConfig) WithoutRedundantContextLimits() ProviderConfig {
	probe := cfg
	probe.ContextWindowTokens = 0
	probe.MaxContextTokens = 0
	window := probe.ContextWindowTokensWithDefault()
	if cfg.ContextWindowTokens == window {
		cfg.ContextWindowTokens = 0
	}
	if cfg.MaxContextTokens == window {
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
