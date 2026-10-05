// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "strings"

// 上下文窗口使用用户填写的值（提供商统一覆盖或单个模型覆盖），否则使用兜底常量。
//
// 不从模型清单或 models.dev 推断：第三方目录某一刻的数据一旦参与计算，就和用户
// 手填的值分不清，目录一变生效值也跟着变。界面上新建配置默认就填 128000，要更准就
// 按模型改。填大了也有退路：超限报错里写了真实窗口就一次缩到位并记住
// （context_overflow.go），没写就逐次减半重试。

// ContextWindowSource 说明生效的窗口是从哪来的，供界面如实标注。
type ContextWindowSource string

const (
	// ContextWindowSourceUser 是用户手填的值。
	ContextWindowSourceUser ContextWindowSource = "user"
	// ContextWindowSourceModel 是当前模型的手动覆盖值。
	ContextWindowSourceModel ContextWindowSource = "model"
	// ContextWindowSourceFallback 是没填时的兜底常量。
	ContextWindowSourceFallback ContextWindowSource = "fallback"
)

// ModelInfoFor 返回同步下来的模型清单里某个模型的条目。模型名在聚合网关上常带
// 供应商命名空间（openai/gpt-4o），所以精确匹配不中时再按去掉命名空间比一次。
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
	// 手动设置只按完整 ID 匹配，不能把不同命名空间下的同名模型当作同一项。
	for _, info := range cfg.Models {
		if strings.TrimSpace(info.ID) == strings.TrimSpace(cfg.Model) && info.ContextWindowOverride > 0 {
			return info.ContextWindowOverride, ContextWindowSourceModel
		}
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
