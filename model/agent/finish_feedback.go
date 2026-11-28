// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import "github.com/SuInk/diana/model/llm"

// appendFinishFeedback 把收尾退回（纯文本没走 send、空 send、联网细节缺出处）接进对话。
// 退回不是工具失败：send 的结果不标 ToolError，withToolResultReminder 也就不会再补
// 「工具调用失败」那段提醒，退回话术只说观察到的事实。
func appendFinishFeedback(messages []llm.Message, resp *llm.GenerateResponse, text, feedback string) []llm.Message {
	if len(resp.ToolCalls) == 0 {
		messages = appendAssistantEcho(messages, text)
		return append(messages, llm.Message{Role: llm.RoleUser, Content: feedback})
	}
	messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: text, ToolCalls: resp.ToolCalls, ResponsesOutput: resp.ResponsesOutput, ContinuationScope: resp.ContinuationScope, AnthropicThinking: resp.AnthropicThinking, ReasoningContent: resp.ReasoningContent})
	for _, call := range resp.ToolCalls {
		messages = append(messages, llm.Message{Role: llm.RoleTool, ToolName: call.Name, ToolCallID: call.ID, Content: feedback})
	}
	return messages
}

// onlySendResults 报告 messages 末尾连续的工具结果是不是全是 send 的：那是收尾退回。
func onlySendResults(messages []llm.Message) bool {
	seen := false
	for index := len(messages) - 1; index >= 0 && messages[index].Role == llm.RoleTool; index-- {
		if messages[index].ToolName != finalizeToolName {
			return false
		}
		seen = true
	}
	return seen
}

// emptySendFeedback 是 send 了空正文又没填 silent 时退回的话：两种合法写法都是协议本身，
// 不是退路。
const emptySendFeedback = "这条没有发出：send 的 text 是空的，没有发出任何消息。要回复就把话写进 text；这一轮不回复就填 silent=true，silent_reason 写一句原因。"
