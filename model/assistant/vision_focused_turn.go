// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

const (
	// visionFocusedMaxQuestionRunes 是「自足的看图提问」允许的最大长度。写得比这
	// 更长的请求通常在描述任务而不是问图，那种请求仍然需要完整历史。
	visionFocusedMaxQuestionRunes = 60
	// visionFocusedHistoryTokens 是这类请求的历史预算上限。留几轮足够接住「再看
	// 一眼」「那这个呢」这类紧接着的追问，同时把长历史挡在外面。
	visionFocusedHistoryTokens int64 = 2400
)

// visionFocusedTurn 判断这一轮是不是「就着眼前这张图问一句」。
//
// 这类请求的答案几乎全部来自图本身：用户发一张图问「这是什么」，三天前的闲聊对
// 回答没有贡献，却要付满额历史的 prefill 和延迟，还会稀释注意力。
//
// 判据刻意保持结构化，不引入额外的模型调用——多打一次分类调用来省 token，在这个
// 量级上并不划算。收紧的只有历史预算：人格、工具和长期记忆一概保留，所以判错的
// 代价是模型少看了几轮旧聊天，而不是丢掉某种能力。
func visionFocusedTurn(event MessageEvent, text string) bool {
	if !eventCarriesImages(event) {
		return false
	}
	// 语义指代已经把这一轮接到了更早的消息上，说明它并不自足。
	if len(eventSemanticSourceMessageIDs(event)) > 0 {
		return false
	}
	if event.Quoted != nil && strings.TrimSpace(quotedPlainText(event.Quoted)) != "" {
		// 引用里带文字，用户是在接着那条消息说话，不只是指一张图。
		return false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		// 只发了图没有提问，交给正常路径判断要不要搭话。
		return false
	}
	return len([]rune(trimmed)) <= visionFocusedMaxQuestionRunes
}

func eventCarriesImages(event MessageEvent) bool {
	if hasImageSegment(event.Segments) {
		return true
	}
	return event.Quoted != nil && hasImageSegment(event.Quoted.Segments)
}

// visionFocusedHistoryBudget 在自足看图提问时收紧历史预算，其余情况原样返回。
func visionFocusedHistoryBudget(budget int64, event MessageEvent, text string) int64 {
	if !visionFocusedTurn(event, text) {
		return budget
	}
	if budget > visionFocusedHistoryTokens {
		return visionFocusedHistoryTokens
	}
	return budget
}
