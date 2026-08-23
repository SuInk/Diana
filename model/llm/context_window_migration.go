// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

// LegacyDefaultContextWindowTokens 是 v0.8.42 之前的兜底上下文窗口常量。
//
// 兜底值本身只是「窗口未知时的猜测」，但 WithDefaults 会把猜测直接写进配置对象，
// 而配置对象是要落库的：任何在旧版本里保存过 LLM 配置的部署，库里都留下了一个
// 显式的 16384。升级到 128K 兜底之后，这个显式值的优先级高于新兜底和模型名推断，
// 于是窗口被永久钉死在 16K——聊天历史被裁到只剩几千 token，表现就是「上下文变短」。
// 一次性迁移把等于旧兜底值的窗口重新视作「未设置」，让它回到推断链上。
const LegacyDefaultContextWindowTokens int64 = 16384

// ClearLegacyContextFallback 把等于旧兜底常量的窗口字段清零，交回推断链。
//
// 只认「恰好等于旧兜底值」这一种情况，并且整个迁移在每个数据库里只跑一次：真的
// 在跑 16K 小窗口模型的部署重新填一次即可，之后不会再被清掉。万一推断偏大，
// 请求超限会被 IsContextOverflowError 识别并自动收缩重试，不会直接失败。
func (cfg ProviderConfig) ClearLegacyContextFallback() (ProviderConfig, bool) {
	changed := false
	if cfg.ContextWindowTokens == LegacyDefaultContextWindowTokens {
		cfg.ContextWindowTokens = 0
		changed = true
	}
	if cfg.MaxContextTokens == LegacyDefaultContextWindowTokens {
		cfg.MaxContextTokens = 0
		changed = true
	}
	return cfg, changed
}

// WithoutRedundantContextLimits 把与推断结果完全一致的窗口字段清零。
//
// 落库时用它剥掉 WithDefaults 派生出来的值，配置里就只留用户自己填的、或同步模型
// 列表带回来的真实窗口。读取时 WithDefaults 会重新推断，兜底值和推断表以后再变，
// 老部署也能跟着变，不会像 16K 那样被历史默认值粘住。
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

// ClearLegacyContextFallback 对配置集内每个配置档执行旧兜底值迁移。
func (s ProfileSet) ClearLegacyContextFallback() (ProfileSet, bool) {
	changed := false
	profiles := make([]Profile, len(s.Profiles))
	copy(profiles, s.Profiles)
	for index, profile := range profiles {
		cfg, profileChanged := profile.Config.ClearLegacyContextFallback()
		if !profileChanged {
			continue
		}
		profiles[index].Config = cfg
		changed = true
	}
	s.Profiles = profiles
	return s, changed
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
