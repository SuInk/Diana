// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"encoding/json"
	"strings"
	"unicode"
)

const (
	messageTokenOverhead       int64 = 8
	contextBudgetSafetyReserve int64 = 128
)

// applyContextLimit 只确定这次请求的上下文上限，不改动任何消息。
//
// 这里以前是一套按优先级丢消息、把正文截短的预算器，装不下的历史和记忆被静默
// 丢掉，模型不知道少了东西。现在照 Pi（earendil-works/pi）的做法：发送层原样
// 发送，请求超出窗口时由 validateGenerateRequest 报出 ErrContextOverflow，或由
// 供应商报超限，交给上层压缩（见 assistant/context_compaction.go）后重试。
func applyContextLimit(req GenerateRequest, cfg ProviderConfig) GenerateRequest {
	limit := cfg.MaxContextTokensWithDefault()
	// 调用方只能收紧：机器人或群显式配了更小的上限时以它为准。
	if req.MaxContextTokens > 0 && (limit <= 0 || req.MaxContextTokens < limit) {
		limit = req.MaxContextTokens
	}
	// 之前从超限报错里学到过这个模型的真实窗口，就按它算，不再每条先撞一次 400。
	if learned, ok := rememberedContextLimits.get(cfg, req.Model); ok && (limit <= 0 || learned < limit) {
		limit = learned
	}
	if limit > 0 {
		req.MaxContextTokens = limit
	}
	return req
}

func estimateMessagesTokens(messages []Message) int64 {
	var total int64
	for _, message := range messages {
		total += estimateMessageTokens(message)
	}
	return total
}

func estimateMessageTokens(message Message) int64 {
	total := messageTokenOverhead
	if len(message.ToolCalls) > 0 {
		if raw, err := json.Marshal(message.ToolCalls); err == nil {
			total += estimateTextTokens(string(raw)) + 8
		}
	}
	if message.ToolCallID != "" {
		total += estimateTextTokens(message.ToolCallID) + estimateTextTokens(message.ToolName) + 4
	}
	if message.ReasoningContent != nil {
		total += estimateTextTokens(*message.ReasoningContent)
	}
	for _, item := range message.AnthropicThinking {
		total += estimateTextTokens(string(item))
	}
	for _, item := range message.ResponsesOutput {
		total += estimateTextTokens(string(item))
	}
	if len(message.Parts) == 0 {
		return total + estimateTextTokens(message.Content)
	}
	hasText := false
	for _, part := range message.Parts {
		switch part.Type {
		case ContentPartText:
			if strings.TrimSpace(part.Text) != "" {
				hasText = true
				total += estimateTextTokens(part.Text) + 2
			}
		case ContentPartImageURL:
			if strings.TrimSpace(part.ImageURL) != "" {
				total += estimatedImageTokens(part.Detail)
			}
		case ContentPartInputAudio:
			if strings.TrimSpace(part.AudioData) != "" {
				total += estimatedAudioTokens(part.AudioData)
			}
		}
	}
	if !hasText {
		total += estimateTextTokens(message.Content)
	}
	return total
}

func estimateToolDefinitionsTokens(tools []ToolDefinition, toolChoice string) int64 {
	if len(tools) == 0 && strings.TrimSpace(toolChoice) == "" {
		return 0
	}
	total := int64(8)
	if raw, err := json.Marshal(tools); err == nil {
		total += estimateTextTokens(string(raw))
	}
	total += estimateTextTokens(toolChoice)
	return total
}

// EstimateRequestInputTokens includes messages, tool schemas and tool choice.
func EstimateRequestInputTokens(req GenerateRequest) int64 {
	return estimateMessagesTokens(req.Messages) + estimateToolDefinitionsTokens(req.Tools, req.ToolChoice)
}

// estimateTextTokens 按字符类别估算 token 数。
//
// 非 ASCII 这一档原来按「每字 2 token」算，注释说这是保守取值。对中文来说它不是
// 保守，是错得离谱：常用汉字实测约 0.85 token/字，2.0 高估了两倍半。拿 1223 条
// 线上请求（字符计数 + 供应商回报的 input_tokens，抽样见
// testdata/token_estimate_samples.json）做最小二乘，系数是 ASCII 0.241、
// 非 ASCII 0.847；旧算法的相对误差中位数 +97.7%、p90 +153.5%，96% 的样本高估
// 超过一半。
//
// 高估不是「留余量」这么轻巧。请求本来装得下却被判成超预算，上游会白跑两次摘要
// 模型、丢掉用户刚发的图，还会就地改写历史消息——前缀一变，整条 prompt 的 KV
// cache 全作废，每轮重新 prefill 六万多 token。线上主 agent 调用的
// cached_input_tokens 长期是 0，根因就在这里。
//
// 现在 ASCII 保留 3 字符 1 token（实测 4.14，留一截余量），非 ASCII 取 7/8
// （实测 0.847，同样略微偏保守）。整体相对偏差中位 +10.6%、p90 +23.7%，仍然偏
// 保守，但不再制造假超限。按连续同类段累加再取整，避免逐字向上取整把偏差重新引
// 回来。
func estimateTextTokens(text string) int64 {
	var total, asciiRun, wideRun int64
	flush := func() {
		if asciiRun > 0 {
			total += (asciiRun + 2) / 3
			asciiRun = 0
		}
		if wideRun > 0 {
			total += (wideRun*7 + 7) / 8
			wideRun = 0
		}
	}
	for _, value := range text {
		switch {
		case value <= unicode.MaxASCII:
			if wideRun > 0 {
				flush()
			}
			asciiRun++
		case value > 0xffff:
			// 星区字符基本是表情和罕用字，按两个宽字符计。
			if asciiRun > 0 {
				flush()
			}
			wideRun += 2
		default:
			if asciiRun > 0 {
				flush()
			}
			wideRun++
		}
	}
	flush()
	return total
}

// EstimateTextTokens returns the conservative cross-provider token estimate
// used by the request context budgeter. Callers that assemble layered context
// should use the same estimate instead of maintaining a second heuristic.
func EstimateTextTokens(text string) int64 {
	return estimateTextTokens(text)
}

// EstimateToolDefinitionTokens 估算单个工具定义在每轮请求里占的 token。档位界面
// 拿它把「常驻更贵」换算成具体数字；口径和 estimateToolDefinitionsTokens 一致，
// 只是不含整份数组的固定开销。
func EstimateToolDefinitionTokens(tool ToolDefinition) int64 {
	raw, err := json.Marshal(tool)
	if err != nil {
		return 0
	}
	return estimateTextTokens(string(raw))
}

// EstimateToolDefinitionsTokens 估算一整份工具数组，含数组本身的固定开销，和编排
// 请求时算固定开销用的是同一个函数。
func EstimateToolDefinitionsTokens(tools []ToolDefinition) int64 {
	return estimateToolDefinitionsTokens(tools, "")
}

// EstimateMessageTokens includes role framing, tool calls and media reserves.
func EstimateMessageTokens(message Message) int64 {
	return estimateMessageTokens(message)
}

// InputTokenBudget applies the same output and safety reserves as requests.
func InputTokenBudget(contextLimit, maxOutputTokens int64) int64 {
	if maxOutputTokens <= 0 {
		maxOutputTokens = DefaultMaxOutputTokens
	}
	budget := contextLimit - maxOutputTokens
	if budget > contextBudgetSafetyReserve {
		budget -= contextBudgetSafetyReserve
	}
	if budget < 1 {
		return 1
	}
	return budget
}

func estimatedImageTokens(detail string) int64 {
	switch strings.ToLower(strings.TrimSpace(detail)) {
	case "low":
		return 1024
	case "high":
		return 8192
	case "original":
		return 16384
	default:
		return 4096
	}
}

func estimatedAudioTokens(encoded string) int64 {
	// Speech audio is normalized to mono WAV before it reaches this layer.
	// Estimate from decoded bytes without counting the base64 payload as text.
	value := int64(len(strings.TrimSpace(encoded))) * 3 / 4 / 1500
	if value < 512 {
		return 512
	}
	if value > 8192 {
		return 8192
	}
	return value
}
