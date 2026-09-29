// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"errors"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"google.golang.org/genai"
)

// 统一的思考强度（none/minimal/low/medium/high/xhigh/max/ultra）在这里翻成各家原生
// 协议的参数。各家的档位和开关对不齐，而且同一家不同代的模型还不一样，所以按模型
// 名分代折算；折算错了或遇到不认这些参数的兼容端点，就靠退避兜住：上游以 400 拒了
// 思考参数时去掉它们重发一次，重发成功再按「端点 + 模型」记住，之后不再发。

// reasoningRejectedText 判断一条 400 的文案是不是在拒思考参数。各家字段名不同，
// 这里只认带这些字段名的文案，不把普通的 400 也拖去退避。
func reasoningRejectedText(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range []string{"reasoning", "thinking", "effort", "output_config", "budget_tokens"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// anthropicModelFamily 把 Claude 模型名分成三代：
//   - legacy：4.5 及更早，只认 budget_tokens，没有 adaptive；
//   - alwaysThinking：Fable、Mythos、Opus 5.5，思考关不掉，只能靠 effort 调深浅；
//   - 其余按当前一代处理：adaptive + output_config.effort，可以显式关闭。
//
// 模型名可能带网关或云厂商的前缀（anthropic.、us.anthropic.），按子串认。
type anthropicModelFamily int

const (
	anthropicFamilyCurrent anthropicModelFamily = iota
	anthropicFamilyLegacy
	anthropicFamilyAlwaysThinking
)

func anthropicFamilyOf(model string) anthropicModelFamily {
	model = strings.ToLower(model)
	for _, marker := range []string{"fable", "mythos", "opus-5-5"} {
		if strings.Contains(model, marker) {
			return anthropicFamilyAlwaysThinking
		}
	}
	for _, marker := range []string{"claude-3", "-4-5", "-4-1", "opus-4-0", "sonnet-4-0", "opus-4-2025", "sonnet-4-2025"} {
		if strings.Contains(model, marker) {
			return anthropicFamilyLegacy
		}
	}
	return anthropicFamilyCurrent
}

// anthropicEffort 折成 output_config.effort。Opus 4.6 / Sonnet 4.6 没有 xhigh。
func anthropicEffort(model, effort string) anthropic.OutputConfigEffort {
	switch effort {
	case "minimal", "low":
		return anthropic.OutputConfigEffortLow
	case "medium":
		return anthropic.OutputConfigEffortMedium
	case "high":
		return anthropic.OutputConfigEffortHigh
	case "xhigh":
		if strings.Contains(strings.ToLower(model), "-4-6") {
			return anthropic.OutputConfigEffortHigh
		}
		return anthropic.OutputConfigEffortXhigh
	case "max", "ultra":
		return anthropic.OutputConfigEffortMax
	}
	return ""
}

// anthropicThinkingBudget 是老一代模型的思考预算。预算算在 max_tokens 里，最多占一半，
// 给回答留出位置；API 要求至少 1024，留不出来就不开思考。
func anthropicThinkingBudget(effort string, maxTokens int64) int64 {
	budget := map[string]int64{
		"minimal": 1024,
		"low":     2048,
		"medium":  8192,
		"high":    16384,
		"xhigh":   24576,
		"max":     32000,
		"ultra":   32000,
	}[effort]
	if budget == 0 {
		return 0
	}
	if half := maxTokens / 2; budget > half {
		budget = half
	}
	if budget < 1024 {
		return 0
	}
	return budget
}

// applyAnthropicReasoning 把思考强度写进请求参数。强制调用指定工具时 Anthropic 不许
// 开思考，这时只调 effort、不动 thinking。开了预算式思考的老模型不接受改过的
// temperature，一并去掉。
func applyAnthropicReasoning(params *anthropic.MessageNewParams, req GenerateRequest) {
	effort := req.ReasoningEffort
	if effort == "" {
		return
	}
	forcedTool := params.ToolChoice.OfTool != nil
	model := string(params.Model)
	switch anthropicFamilyOf(model) {
	case anthropicFamilyLegacy:
		if effort == "none" {
			params.Thinking = anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}}
			return
		}
		budget := anthropicThinkingBudget(effort, params.MaxTokens)
		if budget == 0 || forcedTool {
			return
		}
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(budget)
		params.Temperature = anthropicNoTemperature
	case anthropicFamilyAlwaysThinking:
		// 关不掉思考，none 只能退到最浅的一档；显式 disabled 会被 400。
		if effort == "none" {
			params.OutputConfig.Effort = anthropic.OutputConfigEffortLow
			return
		}
		params.OutputConfig.Effort = anthropicEffort(model, effort)
	default:
		if effort == "none" {
			params.Thinking = anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}}
			return
		}
		params.OutputConfig.Effort = anthropicEffort(model, effort)
		if !forcedTool {
			params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
		}
	}
}

// anthropicNoTemperature 是未设置的 temperature。
var anthropicNoTemperature = anthropic.MessageNewParams{}.Temperature

// stripAnthropicReasoning 去掉思考相关的参数，退避时用。
func stripAnthropicReasoning(params *anthropic.MessageNewParams) bool {
	if params.Thinking == (anthropic.ThinkingConfigParamUnion{}) && params.OutputConfig.Effort == "" {
		return false
	}
	params.Thinking = anthropic.ThinkingConfigParamUnion{}
	params.OutputConfig.Effort = ""
	return true
}

func anthropicReasoningRejected(err error) bool {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	return reasoningRejectedText(apiErr.Error())
}

// anthropicThinkingHistoryRejected 认出「开了思考、但历史里的工具调用没带思考块」
// 这类 400（报错指向 messages.N.content）。它是这一轮历史的问题，比如中途从别的
// 模型降级过来：摘掉思考重发能救回这一轮，但不能记成「这个模型不认思考参数」，
// 否则之后几天这个模型都不会再思考。
func anthropicThinkingHistoryRejected(err error) bool {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	return strings.Contains(strings.ToLower(apiErr.Error()), "messages.")
}

// geminiThinkingConfig 把思考强度折成 Gemini 的 thinkingConfig：
//   - 2.5 系列按 thinkingBudget，Flash 能用 0 关掉，Pro 最低 128；
//   - 3 及以后按 thinkingLevel，Pro 只有 LOW/HIGH，Flash 有 MINIMAL 到 HIGH；
//   - 1.x、2.0 和 Gemma 不是思考模型，不发。
func geminiThinkingConfig(model, effort string) *genai.ThinkingConfig {
	if effort == "" {
		return nil
	}
	model = strings.ToLower(model)
	pro := strings.Contains(model, "pro")
	switch {
	case strings.Contains(model, "gemma"), strings.Contains(model, "gemini-1"), strings.Contains(model, "gemini-2.0"):
		return nil
	case strings.Contains(model, "gemini-2.5"):
		budget := map[string]int32{
			"none":    0,
			"minimal": 512,
			"low":     1024,
			"medium":  8192,
			"high":    16384,
			"xhigh":   24576,
			"max":     24576,
			"ultra":   24576,
		}[effort]
		if pro && budget < 128 {
			budget = 128
		}
		return &genai.ThinkingConfig{ThinkingBudget: &budget}
	}
	var level genai.ThinkingLevel
	switch effort {
	case "none", "minimal":
		level = genai.ThinkingLevelMinimal
	case "low":
		level = genai.ThinkingLevelLow
	case "medium":
		level = genai.ThinkingLevelMedium
	default:
		level = genai.ThinkingLevelHigh
	}
	if pro {
		switch level {
		case genai.ThinkingLevelMinimal, genai.ThinkingLevelLow:
			level = genai.ThinkingLevelLow
		default:
			level = genai.ThinkingLevelHigh
		}
	}
	return &genai.ThinkingConfig{ThinkingLevel: level}
}

func geminiReasoningRejected(err error) bool {
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		var ptr *genai.APIError
		if !errors.As(err, &ptr) || ptr == nil {
			return false
		}
		apiErr = *ptr
	}
	if apiErr.Code != http.StatusBadRequest {
		return false
	}
	return reasoningRejectedText(apiErr.Message)
}

// reasoningDowngradeRemembered 报告这个「端点 + 模型」是否已经学到不接受思考参数。
func reasoningDowngradeRemembered(cfg ProviderConfig, model string) bool {
	return rememberedDowngrades.seen(downgradeMemoKey(cfg, model), downgradeFieldReasoning)
}

// rememberReasoningDowngrade 在去掉思考参数重发成功后记住结论。
func rememberReasoningDowngrade(cfg ProviderConfig, model string) {
	rememberedDowngrades.remember(downgradeMemoKey(cfg, model), map[string]bool{downgradeFieldReasoning: true})
}
