// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "strings"

// 上下文窗口是「模型的属性」，不是「这套配置的属性」——一个 provider 下面挂着几十个
// 模型，窗口从 8K 到 1M 都有。把它当成配置字段推断一次、再显示成用户填过的样子，
// 换个模型就错，而且错得看不出来。
//
// 所以窗口按下面的顺序在读取时现算，每次都用当前选中的模型：
//  1. 用户在 WebUI 里手填的值——填了就以填的为准，任何目录都不覆盖它；
//  2. 同步下来的模型清单里这个模型的窗口——它随模型走，换模型自动跟着换；
//  3. 模型名前缀推断表——清单没同步过时的近似值；
//  4. 兜底常量。
//
// 只有第 1 项会落库。后三项每次读取重算：目录数据会变（同一个模型的窗口过一阵子
// 就可能被上游改掉），把它写进配置等于把一份会过期的第三方数据冒充成用户设置。

// ContextWindowSource 说明生效的窗口是从哪来的，供界面如实标注。
type ContextWindowSource string

const (
	// ContextWindowSourceUser 是用户手填的值。
	ContextWindowSourceUser ContextWindowSource = "user"
	// ContextWindowSourceModelList 来自同步下来的模型清单（含 models.dev 补充）。
	ContextWindowSourceModelList ContextWindowSource = "model_list"
	// ContextWindowSourceInferred 来自模型名前缀推断表。
	ContextWindowSourceInferred ContextWindowSource = "inferred"
	// ContextWindowSourceFallback 是什么都推断不出时的兜底常量。
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

// ResolveContextWindowTokens 按上面的顺序算出当前模型的窗口，并说明它的来源。
func (cfg ProviderConfig) ResolveContextWindowTokens() (int64, ContextWindowSource) {
	if cfg.ContextWindowTokens > 0 {
		return cfg.ContextWindowTokens, ContextWindowSourceUser
	}
	if info, ok := cfg.ModelInfoFor(cfg.Model); ok && info.ContextWindowTokens > 0 {
		return info.ContextWindowTokens, ContextWindowSourceModelList
	}
	if window := KnownContextWindowTokens(cfg.Model); window > 0 {
		return window, ContextWindowSourceInferred
	}
	return DefaultContextWindowTokens, ContextWindowSourceFallback
}

// ContextWindowTokensWithDefault 返回当前模型生效的窗口。
func (cfg ProviderConfig) ContextWindowTokensWithDefault() int64 {
	window, _ := cfg.ResolveContextWindowTokens()
	return window
}
