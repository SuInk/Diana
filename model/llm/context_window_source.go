// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "strings"

// 上下文窗口只认两个来源：用户在 WebUI 里填的值，和填之前的兜底常量。
//
// 曾经还有两层自动推断——同步下来的模型清单（含 models.dev 补充）和模型名前缀表。
// 它们的问题不是不准，而是「第三方目录某一刻的数据」被当成了本地设置：目录会变，
// 前缀表按 gpt-5 前缀给出 400000 这种粗粒度猜测，而界面上它和用户手填的值长得
// 一模一样。既然分不清，就不如不猜——要精确就明确填一个。
//
// 代价是明摆着的：没填的部署一律按兜底常量算，200K/1M 的模型会被压到这个数。
// 换来的是「界面上写的就是实际生效的」，以及超限时 ErrContextOverflow 自动收缩重试
// 这条安全网仍然在。模型清单里带回来的窗口不再参与计算，只作为参考值展示，
// 提示用户可以照着填。

// ContextWindowSource 说明生效的窗口是从哪来的，供界面如实标注。
type ContextWindowSource string

const (
	// ContextWindowSourceUser 是用户手填的值。
	ContextWindowSourceUser ContextWindowSource = "user"
	// ContextWindowSourceFallback 是没填时的兜底常量。
	ContextWindowSourceFallback ContextWindowSource = "fallback"
)

// ModelInfoFor 返回同步下来的模型清单里某个模型的条目。模型名在聚合网关上常带
// 供应商命名空间（openai/gpt-4o），所以精确匹配不中时再按去掉命名空间比一次。
//
// 它不再参与窗口计算，只用来给界面提供「这个模型的清单里写着多少」这个参考值。
func (cfg ProviderConfig) ModelInfoFor(model string) (ModelInfo, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return ModelInfo{}, false
	}
	for _, info := range cfg.Models {
		if info.ID == model {
			return info, true
		}
	}
	bare := bareModelName(model)
	for _, info := range cfg.Models {
		if bareModelName(info.ID) == bare {
			return info, true
		}
	}
	return ModelInfo{}, false
}

func bareModelName(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	if index := strings.LastIndex(name, "/"); index >= 0 && index+1 < len(name) {
		name = name[index+1:]
	}
	return name
}

// ResolveContextWindowTokens 返回生效的窗口和它的来源。
func (cfg ProviderConfig) ResolveContextWindowTokens() (int64, ContextWindowSource) {
	if cfg.ContextWindowTokens > 0 {
		return cfg.ContextWindowTokens, ContextWindowSourceUser
	}
	return DefaultContextWindowTokens, ContextWindowSourceFallback
}

// ContextWindowTokensWithDefault 返回生效的窗口。
func (cfg ProviderConfig) ContextWindowTokensWithDefault() int64 {
	window, _ := cfg.ResolveContextWindowTokens()
	return window
}

// CatalogContextWindowTokens 返回同步下来的模型清单里记的窗口，供界面作为参考值
// 展示；清单里没有就返回 0。它不参与任何计算。
func (cfg ProviderConfig) CatalogContextWindowTokens(model string) int64 {
	if info, ok := cfg.ModelInfoFor(model); ok {
		return info.ContextWindowTokens
	}
	return 0
}
