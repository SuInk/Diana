// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

const (
	messageTokenOverhead       int64 = 8
	contextBudgetSafetyReserve int64 = 128
	minimumRequiredMessageCost int64 = messageTokenOverhead + 8
	maxMemoryRetentionTokens   int64 = 2048
	maxHistoryRetentionTokens  int64 = 4096
	minimumRetentionWindow     int64 = 8192
	minimumRecentHistoryCount        = 4
	truncationMarker                 = "\n...[上下文已按 token 预算裁剪]...\n"
)

type tokenBudgetCandidate struct {
	index    int
	priority MessagePriority
	cost     int64
}

// applyContextBudget keeps every request below the configured total context
// budget. The estimator is deliberately conservative across providers: CJK and
// non-ASCII text cost more than ASCII, and image parts reserve vision tokens.
func applyContextBudget(req GenerateRequest, cfg ProviderConfig) GenerateRequest {
	limit := cfg.MaxContextTokensWithDefault()
	// 超限重试会带上更小的上限；只接受比配置档更保守的值，避免调用方反向放大。
	if req.MaxContextTokens > 0 && (limit <= 0 || req.MaxContextTokens < limit) {
		limit = req.MaxContextTokens
	}
	if limit <= 0 || len(req.Messages) == 0 {
		return req
	}
	req.MaxContextTokens = limit
	outputReserve := req.MaxOutputTokens
	if outputReserve <= 0 {
		outputReserve = DefaultMaxOutputTokens
	}
	inputBudget := limit - outputReserve
	if inputBudget > contextBudgetSafetyReserve {
		inputBudget -= contextBudgetSafetyReserve
	}
	if inputBudget < 1 {
		inputBudget = 1
	}
	fixedTokens := estimateToolDefinitionsTokens(req.Tools, req.ToolChoice)
	messageBudget := inputBudget - fixedTokens
	if messageBudget < 1 {
		messageBudget = 1
	}
	req.Messages = fitMessagesToTokenBudget(req.Messages, messageBudget)
	return req
}

func fitMessagesToTokenBudget(messages []Message, budget int64) []Message {
	fitted, _ := fitMessagesToTokenBudgetDetailed(messages, budget)
	return fitted
}

// fitMessagesToTokenBudgetDetailed also returns the surviving messages keyed by
// their index in the input slice, so callers can report which prompt layers were
// kept, trimmed or dropped without re-running the selection heuristics.
func fitMessagesToTokenBudgetDetailed(messages []Message, budget int64) ([]Message, map[int]Message) {
	if len(messages) == 0 || budget <= 0 {
		return nil, map[int]Message{}
	}
	messages = lowerProtectedImageDetailToFit(messages, budget)
	if estimateMessagesTokens(messages) <= budget {
		kept := make(map[int]Message, len(messages))
		for index, message := range messages {
			kept[index] = message
		}
		return append([]Message(nil), messages...), kept
	}

	candidates := make([]tokenBudgetCandidate, 0, len(messages))
	lastIndex := currentInputIndex(messages)
	for index, message := range messages {
		priority := effectiveMessagePriority(message, index == lastIndex)
		candidates = append(candidates, tokenBudgetCandidate{index: index, priority: priority, cost: estimateMessageTokens(message)})
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].priority == candidates[right].priority {
			return candidates[left].index > candidates[right].index
		}
		return candidates[left].priority > candidates[right].priority
	})

	selected := make(map[int]Message, len(messages))
	remaining := budget

	// Current input and plugin evidence are authoritative. Reserve them before
	// balancing the flexible prompt layers.
	system := candidatesWithPriority(candidates, MessagePrioritySystem, MessagePrioritySystem)
	systemReserve := minimumCandidateCost(system)
	protected := candidatesWithPriority(candidates, MessagePriorityPlugin, MessagePriorityCurrent)
	protectedBudget := remaining - systemReserve
	if protectedBudget < 1 {
		protectedBudget = remaining
	}
	protectedRemaining := selectRequiredMessages(messages, protected, selected, protectedBudget)
	remaining -= protectedBudget - protectedRemaining

	// A large Agent protocol or persona must not silently reduce conversational
	// context to zero. At the default 16K window these pools retain up to 2K of
	// structured memory and 4K of the most recent history. Smaller windows scale
	// the pools down, while always leaving every system message a minimal slot.
	availableForContext := remaining - systemReserve
	if budget >= minimumRetentionWindow && availableForContext > 0 {
		memoryTarget, historyTarget := contextRetentionTargets(messages, budget, availableForContext)
		remaining -= selectMemoryRetention(messages, candidates, selected, memoryTarget)
		remaining -= selectHistoryRetention(messages, candidates, selected, historyTarget)
	}

	remaining = selectRequiredMessages(messages, system, selected, remaining)
	for _, item := range candidates {
		if remaining <= 0 {
			break
		}
		if _, ok := selected[item.index]; ok || item.priority >= MessagePrioritySystem {
			continue
		}
		group := contextBudgetGroupIndexes(messages, item.index)
		groupCost := int64(0)
		for _, index := range group {
			if _, ok := selected[index]; !ok {
				groupCost += estimateMessageTokens(messages[index])
			}
		}
		if groupCost <= remaining {
			for _, index := range group {
				if _, ok := selected[index]; ok {
					continue
				}
				selected[index] = messages[index]
				remaining -= estimateMessageTokens(messages[index])
			}
			continue
		}
		if item.priority == MessagePriorityHistory {
			// History is ordered newest to oldest. If the next complete turn does
			// not fit, do not skip it to collect disconnected older fragments.
			break
		}
		if item.priority == MessagePriorityRecentHistory && len(group) > 1 {
			groupItems := make([]struct {
				index    int
				cost     int64
				priority MessagePriority
			}, 0, len(group))
			for _, index := range group {
				if _, ok := selected[index]; ok {
					continue
				}
				groupItems = append(groupItems, struct {
					index    int
					cost     int64
					priority MessagePriority
				}{index: index, cost: estimateMessageTokens(messages[index]), priority: item.priority})
			}
			remaining = selectRequiredMessagesProportionally(messages, groupItems, selected, remaining)
			continue
		}
		message := messages[item.index]
		if item.priority < MessagePrioritySummary {
			continue
		}
		if message.AtomicText {
			// Producer-owned semantic unit: it was already shrunk to its target,
			// and a mid-string cut here would silently destroy what it means.
			continue
		}
		trimmed, ok := trimMessageToTokenBudget(message, remaining)
		if !ok {
			continue
		}
		selected[item.index] = trimmed
		remaining -= estimateMessageTokens(trimmed)
	}

	if len(selected) == 0 {
		if trimmed, ok := trimMessageToTokenBudget(messages[lastIndex], budget); ok {
			selected[lastIndex] = trimmed
		}
	}
	out := make([]Message, 0, len(selected))
	for index := range messages {
		if message, ok := selected[index]; ok {
			out = append(out, message)
		}
	}
	return out, selected
}

func minimumCandidateCost(candidates []tokenBudgetCandidate) int64 {
	var total int64
	for _, item := range candidates {
		total += minimumMessageCost(item.priority)
	}
	return total
}

func minimumMessageCost(priority MessagePriority) int64 {
	if priority == MessagePrioritySystem {
		return messageTokenOverhead + 96
	}
	return minimumRequiredMessageCost
}

func candidatesWithPriority(candidates []tokenBudgetCandidate, minimum, maximum MessagePriority) []tokenBudgetCandidate {
	out := make([]tokenBudgetCandidate, 0, len(candidates))
	for _, item := range candidates {
		if item.priority >= minimum && item.priority <= maximum {
			out = append(out, item)
		}
	}
	return out
}

func contextRetentionTargets(messages []Message, budget, available int64) (int64, int64) {
	hasMemory := false
	hasHistory := false
	lastIndex := currentInputIndex(messages)
	for index, message := range messages {
		switch effectiveMessagePriority(message, index == lastIndex) {
		case MessagePriorityMemory:
			hasMemory = true
		case MessagePriorityHistory:
			hasHistory = true
		}
	}
	memoryTarget := int64(0)
	historyTarget := int64(0)
	if hasMemory {
		memoryTarget = minInt64(maxMemoryRetentionTokens, budget/6)
	}
	if hasHistory {
		historyTarget = minInt64(maxHistoryRetentionTokens, budget/3)
	}
	total := memoryTarget + historyTarget
	if total <= available || total == 0 {
		return memoryTarget, historyTarget
	}
	memoryTarget = available * memoryTarget / total
	historyTarget = available - memoryTarget
	return memoryTarget, historyTarget
}

func selectMemoryRetention(messages []Message, candidates []tokenBudgetCandidate, selected map[int]Message, budget int64) int64 {
	if budget <= messageTokenOverhead {
		return 0
	}
	remaining := budget
	for _, item := range candidatesWithPriority(candidates, MessagePriorityMemory, MessagePriorityMemory) {
		if _, ok := selected[item.index]; ok {
			continue
		}
		message := messages[item.index]
		if item.cost <= remaining {
			selected[item.index] = message
			remaining -= item.cost
			continue
		}
		trimmed, ok := trimMessageToTokenBudget(message, remaining)
		if ok {
			selected[item.index] = trimmed
			remaining -= estimateMessageTokens(trimmed)
		}
		break
	}
	return budget - remaining
}

func selectHistoryRetention(messages []Message, candidates []tokenBudgetCandidate, selected map[int]Message, budget int64) int64 {
	if budget <= messageTokenOverhead {
		return 0
	}
	history := candidatesWithPriority(candidates, MessagePriorityHistory, MessagePriorityHistory)
	if len(history) == 0 {
		return 0
	}
	remaining := budget
	guaranteed := min(len(history), minimumRecentHistoryCount)
	for index, item := range history {
		if _, ok := selected[item.index]; ok {
			continue
		}
		message := messages[item.index]
		if index < guaranteed {
			slots := int64(guaranteed - index)
			allocation := remaining / slots
			if item.cost <= allocation {
				selected[item.index] = message
				remaining -= item.cost
				continue
			}
			trimmed, ok := trimMessageToTokenBudget(message, allocation)
			if ok {
				selected[item.index] = trimmed
				remaining -= estimateMessageTokens(trimmed)
			}
			continue
		}
		if item.cost > remaining {
			continue
		}
		selected[item.index] = message
		remaining -= item.cost
	}
	return budget - remaining
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func contextBudgetGroupIndexes(messages []Message, index int) []int {
	if index < 0 || index >= len(messages) {
		return nil
	}
	groupID := strings.TrimSpace(messages[index].ContextGroup)
	if groupID == "" {
		groupID = toolExchangeContextGroup(messages, index)
	}
	if groupID == "" {
		return []int{index}
	}
	indexes := make([]int, 0, 2)
	for candidateIndex, message := range messages {
		candidateGroup := strings.TrimSpace(message.ContextGroup)
		if candidateGroup == "" {
			candidateGroup = toolExchangeContextGroup(messages, candidateIndex)
		}
		if candidateGroup == groupID {
			indexes = append(indexes, candidateIndex)
		}
	}
	return indexes
}

// toolExchangeContextGroup keeps one assistant tool-call batch and all of its
// results together even when the producer did not assign ContextGroup. Sending
// either side alone is invalid for OpenAI-compatible and Anthropic protocols.
func toolExchangeContextGroup(messages []Message, index int) string {
	if index < 0 || index >= len(messages) {
		return ""
	}
	message := messages[index]
	if message.Role == RoleAssistant && len(message.ToolCalls) > 0 {
		return "tool-exchange:" + toolCallBatchKey(message.ToolCalls)
	}
	if message.Role != RoleTool || strings.TrimSpace(message.ToolCallID) == "" {
		return ""
	}
	for candidate := index - 1; candidate >= 0; candidate-- {
		previous := messages[candidate]
		if previous.Role == RoleAssistant && len(previous.ToolCalls) > 0 {
			for _, call := range previous.ToolCalls {
				if call.ID == message.ToolCallID {
					return "tool-exchange:" + toolCallBatchKey(previous.ToolCalls)
				}
			}
		}
		if previous.Role == RoleUser {
			break
		}
	}
	return ""
}

func toolCallBatchKey(calls []ToolCall) string {
	var builder strings.Builder
	for _, call := range calls {
		builder.WriteString(call.ID)
		builder.WriteByte('\x00')
	}
	return builder.String()
}

// Current input and plugin evidence are required context. When their image
// detail would force the budgeter to discard whole images, request low detail
// first so a multi-image reference remains complete whenever the window allows.
func lowerProtectedImageDetailToFit(messages []Message, budget int64) []Message {
	if estimateRequiredMessagesTokens(messages) <= budget {
		return messages
	}
	if PlanInputBudget(GenerateRequest{Messages: messages}, budget).ImageExcess <= 0 {
		return messages
	}
	out := append([]Message(nil), messages...)
	lastIndex := currentInputIndex(out)
	for index := range out {
		if effectiveMessagePriority(out[index], index == lastIndex) < MessagePriorityPlugin {
			continue
		}
		parts := append([]ContentPart(nil), out[index].Parts...)
		changed := false
		for partIndex := range parts {
			part := &parts[partIndex]
			if part.Type != ContentPartImageURL || strings.TrimSpace(part.ImageURL) == "" || strings.EqualFold(strings.TrimSpace(part.Detail), "low") {
				continue
			}
			part.Detail = "low"
			changed = true
		}
		if changed {
			out[index].Parts = parts
		}
	}
	return out
}

func estimateRequiredMessagesTokens(messages []Message) int64 {
	lastIndex := currentInputIndex(messages)
	var total int64
	for index, message := range messages {
		if effectiveMessagePriority(message, index == lastIndex) >= MessagePrioritySystem {
			total += estimateMessageTokens(message)
		}
	}
	return total
}

// System instructions, plugin evidence, and the current user turn are required.
// Preserve plugin evidence and current input whole when feasible, then allocate
// the remaining space across generic system instructions.
func selectRequiredMessages(messages []Message, candidates []tokenBudgetCandidate, selected map[int]Message, budget int64) int64 {
	required := make([]struct {
		index    int
		cost     int64
		priority MessagePriority
	}, 0, 2)
	var totalCost int64
	for _, item := range candidates {
		required = append(required, struct {
			index    int
			cost     int64
			priority MessagePriority
		}{index: item.index, cost: item.cost, priority: item.priority})
		totalCost += item.cost
	}
	if len(required) == 0 || budget <= 0 {
		return budget
	}
	sort.Slice(required, func(left, right int) bool {
		return required[left].index < required[right].index
	})
	if totalCost <= budget {
		for _, item := range required {
			selected[item.index] = messages[item.index]
			budget -= item.cost
		}
		return budget
	}

	// Current input and authoritative plugin evidence must remain intact whenever
	// they can fit. Generic system instructions are the expendable part: trim
	// those before silently removing facts returned by a plugin.
	protectedCost := int64(0)
	flexibleCount := int64(0)
	for _, item := range required {
		if item.priority >= MessagePriorityPlugin {
			protectedCost += item.cost
		} else {
			flexibleCount++
		}
	}
	minimumForFlexible := flexibleCount * minimumMessageCost(MessagePrioritySystem)
	if protectedCost+minimumForFlexible <= budget {
		for _, item := range required {
			if item.priority < MessagePriorityPlugin {
				continue
			}
			selected[item.index] = messages[item.index]
			budget -= item.cost
		}
		flexible := required[:0]
		for _, item := range required {
			if item.priority < MessagePriorityPlugin {
				flexible = append(flexible, item)
			}
		}
		return selectRequiredMessagesProportionally(messages, flexible, selected, budget)
	}

	return selectRequiredMessagesProportionally(messages, required, selected, budget)
}

func selectRequiredMessagesProportionally(messages []Message, required []struct {
	index    int
	cost     int64
	priority MessagePriority
}, selected map[int]Message, budget int64) int64 {
	var totalCost int64
	for _, item := range required {
		totalCost += item.cost
	}

	remainingCost := totalCost
	for index, item := range required {
		slotsAfter := int64(len(required) - index - 1)
		if budget <= messageTokenOverhead {
			break
		}
		allocation := budget
		if remainingCost > 0 {
			allocation = budget * item.cost / remainingCost
		}
		minimumForOthers := slotsAfter * minimumMessageCost(item.priority)
		if maximum := budget - minimumForOthers; allocation > maximum {
			allocation = maximum
		}
		if allocation < minimumMessageCost(item.priority) {
			allocation = minimumMessageCost(item.priority)
		}
		if allocation > budget {
			allocation = budget
		}
		trimmed, ok := trimMessageToTokenBudget(messages[item.index], allocation)
		if ok {
			selected[item.index] = trimmed
			budget -= estimateMessageTokens(trimmed)
		}
		remainingCost -= item.cost
	}
	return budget
}

// Trailing system notes are context, not the current conversational input.
func currentInputIndex(messages []Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleUser || messages[i].Role == RoleTool {
			return i
		}
	}
	return -1
}

func effectiveMessagePriority(message Message, current bool) MessagePriority {
	priority := message.Priority
	if priority == MessagePriorityDefault {
		priority = MessagePriorityHistory
	}
	if message.Role == RoleSystem && priority < MessagePrioritySystem {
		priority = MessagePrioritySystem
	}
	if current && priority < MessagePriorityCurrent {
		priority = MessagePriorityCurrent
	}
	return priority
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

func trimMessageToTokenBudget(message Message, budget int64) (Message, bool) {
	if budget <= messageTokenOverhead {
		return Message{}, false
	}
	textBudget := budget - messageTokenOverhead
	trimmed := message
	if len(message.Parts) == 0 {
		trimmed.Content = trimTextToTokenBudget(message.Content, textBudget, preserveMessagePrefix(message))
		return trimmed, strings.TrimSpace(trimmed.Content) != ""
	}

	trimmed.Parts = nil
	remaining := textBudget
	hasText := false
	hadImages := 0
	keptImages := 0
	hadAudio := 0
	keptAudio := 0
	for _, part := range message.Parts {
		switch part.Type {
		case ContentPartText:
			if remaining <= 2 {
				continue
			}
			text := strings.TrimSpace(part.Text)
			if text == "" {
				continue
			}
			partBudget := remaining - 2
			if cost := estimateTextTokens(text); cost > partBudget {
				text = trimTextToTokenBudget(text, partBudget, preserveMessagePrefix(message))
			}
			if text == "" {
				continue
			}
			part.Text = text
			trimmed.Parts = append(trimmed.Parts, part)
			hasText = true
			remaining -= estimateTextTokens(text) + 2
		case ContentPartImageURL:
			hadImages++
			cost := estimatedImageTokens(part.Detail)
			if strings.TrimSpace(part.ImageURL) == "" || cost > remaining {
				continue
			}
			trimmed.Parts = append(trimmed.Parts, part)
			keptImages++
			remaining -= cost
		case ContentPartInputAudio:
			hadAudio++
			cost := estimatedAudioTokens(part.AudioData)
			if strings.TrimSpace(part.AudioData) == "" || cost > remaining {
				continue
			}
			trimmed.Parts = append(trimmed.Parts, part)
			keptAudio++
			remaining -= cost
		}
	}
	// Text that merely labels an attached historical image must not survive
	// after the image itself is dropped; that recreates a misleading placeholder.
	if hadImages != keptImages && message.Priority < MessagePriorityCurrent {
		return Message{}, false
	}
	if hadAudio != keptAudio {
		return Message{}, false
	}
	if !hasText && remaining > 0 {
		text := trimTextToTokenBudget(message.Content, remaining, preserveMessagePrefix(message))
		if text != "" {
			trimmed.Content = text
			trimmed.Parts = append([]ContentPart{{Type: ContentPartText, Text: text}}, trimmed.Parts...)
		}
	}
	return trimmed, len(trimmed.Parts) > 0
}

func preserveMessagePrefix(message Message) bool {
	if message.Priority == MessagePriorityMemory || message.Priority == MessagePrioritySummary {
		return true
	}
	return message.Role == RoleSystem && estimateTextTokens(message.Content) <= 256
}

func trimTextToTokenBudget(text string, budget int64, prefixOnly bool) string {
	text = strings.TrimSpace(text)
	if text == "" || budget <= 0 {
		return ""
	}
	if estimateTextTokens(text) <= budget {
		return text
	}
	runes := []rune(text)
	low, high := 1, len(runes)
	best := ""
	for low <= high {
		mid := low + (high-low)/2
		candidate := clippedText(runes, mid, prefixOnly)
		if estimateTextTokens(candidate) <= budget {
			best = candidate
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return strings.TrimSpace(best)
}

func clippedText(runes []rune, keep int, prefixOnly bool) string {
	if keep >= len(runes) {
		return string(runes)
	}
	marker := []rune(truncationMarker)
	if keep <= len(marker)+2 {
		if prefixOnly {
			return string(runes[:keep])
		}
		return string(runes[len(runes)-keep:])
	}
	contentRunes := keep - len(marker)
	if prefixOnly {
		return string(runes[:contentRunes]) + truncationMarker
	}
	head := (contentRunes + 1) / 2
	tail := contentRunes - head
	return string(runes[:head]) + truncationMarker + string(runes[len(runes)-tail:])
}
