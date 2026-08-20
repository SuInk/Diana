// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "strings"

// contextOverflowMarkers 覆盖各家供应商对「请求超出模型上下文」的措辞。窗口靠
// 模型名推断出来的部分终究是推断：推错时必须能识别出来并收缩重试，而不是让这
// 一轮回复直接失败。
var contextOverflowMarkers = []string{
	"context_length_exceeded",
	"context length exceeded",
	"maximum context length",
	"reduce the length of the messages",
	"prompt is too long",
	"input is too long",
	"input length and `max_tokens` exceed context limit",
	"too many tokens",
	"exceeds the maximum number of tokens",
	"exceeds model context",
	"request exceeds the maximum allowed number of tokens",
	"token count exceeds",
	"exceed context window",
	"上下文长度",
	"超出模型上下文",
}

// IsContextOverflowError 判断错误是否为「请求超出模型上下文窗口」。
func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range contextOverflowMarkers {
		if strings.Contains(text, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}
