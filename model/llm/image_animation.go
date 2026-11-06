// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "strings"

// ModalityAnimation 是「能出动图」这项输出能力，和 text/image/audio/video 并列
// 写在模型的 output_modalities 里。
//
// 动图要单独成一项能力：同一个提供商下往往并列着能出 GIF 和不能出 GIF 的图片模型，
// 生图候选不按它筛，GIF 请求就会落到老模型上。老模型直接报错还好，兼容网关不认
// output_format、照旧回一张 PNG 时，用户要的动图就悄悄变成了静图。
const ModalityAnimation = "animation"

// animationImageModelPrefixes 是上游清单不标动图、但已知能出 GIF 的图片模型。
// 模型目录（models.dev 和各家 /models）都没有「动图」这一模态，不按名字认，
// 每个用户都得自己手动勾一遍。
var animationImageModelPrefixes = []string{"gpt-image-2.6"}

// ImageModelSupportsAnimation 报告这套配置的图片模型能不能出动图。
//
// 手动设置过能力的以手动为准，勾掉就是不支持；没设置的看清单有没有标 animation，
// 再按已知模型名兜底。
func (cfg ProviderConfig) ImageModelSupportsAnimation() bool {
	model := cfg.ImageModelWithDefault()
	if info, ok := cfg.ModelInfoFor(model); ok {
		if info.CapabilitiesOverride != nil {
			return containsModality(info.CapabilitiesOverride.OutputModalities, ModalityAnimation)
		}
		if containsModality(info.OutputModalities, ModalityAnimation) {
			return true
		}
	}
	return knownAnimationImageModel(model)
}

func knownAnimationImageModel(model string) bool {
	name := bareModelName(model)
	for _, prefix := range animationImageModelPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func containsModality(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}
