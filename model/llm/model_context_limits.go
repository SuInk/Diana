// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "strings"

// 供应商的 /v1/models 大多只返回 id 和归属，不带上下文窗口：OpenAI 官方接口和
// Anthropic 都是如此，只有 OpenRouter、models.dev 这类聚合目录会给出 context
// 字段。目录取不到时以前一律回落到 16K 兜底值，等于把现代模型当成 2023 年的
// 模型用。下表按模型名前缀补上常见系列的真实窗口，取不到再走兜底。
//
// 表是「取不到时的推断」，不是硬约束：目录给了窗口以目录为准，用户在 WebUI 填了
// 具体数值以用户为准，本表只在两者都没有时生效。
var knownModelContextWindows = []struct {
	prefixes []string
	window   int64
}{
	// Anthropic Claude：Sonnet/Opus/Haiku 全系 200K。
	{prefixes: []string{"claude-"}, window: 200000},
	// Google Gemini 1.5 起为 1M 输入。
	{prefixes: []string{"gemini-1.5", "gemini-2", "gemini-3"}, window: 1000000},
	// OpenAI：gpt-4o/4.1 之后为 128K 起，o 系列推理模型同级。
	{prefixes: []string{"gpt-4o", "gpt-4.1", "gpt-4-turbo", "o1", "o3", "o4"}, window: 128000},
	{prefixes: []string{"gpt-5", "gpt-6"}, window: 400000},
	// 国内常见开放模型。
	{prefixes: []string{"deepseek"}, window: 128000},
	{prefixes: []string{"qwen3", "qwen-max", "qwen-plus", "qwen2.5"}, window: 128000},
	{prefixes: []string{"glm-4", "glm-5"}, window: 128000},
	{prefixes: []string{"moonshot", "kimi"}, window: 256000},
	{prefixes: []string{"minimax"}, window: 1000000},
	{prefixes: []string{"step-"}, window: 256000},
	{prefixes: []string{"grok-"}, window: 128000},
	{prefixes: []string{"mistral-large", "mistral-medium", "pixtral"}, window: 128000},
	{prefixes: []string{"llama-3.1", "llama-3.2", "llama-3.3", "llama-4"}, window: 128000},
}

// KnownContextWindowTokens 按模型名推断上下文窗口，未知模型返回 0。
// 匹配在去掉常见路径前缀（openai/gpt-4o、accounts/.../models/x）后按小写前缀进行，
// 因为聚合网关普遍会给模型名加上供应商命名空间。
func KnownContextWindowTokens(model string) int64 {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return 0
	}
	if index := strings.LastIndex(name, "/"); index >= 0 && index+1 < len(name) {
		name = name[index+1:]
	}
	for _, entry := range knownModelContextWindows {
		for _, prefix := range entry.prefixes {
			if strings.HasPrefix(name, prefix) {
				return entry.window
			}
		}
	}
	return 0
}
