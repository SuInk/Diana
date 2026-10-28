// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/SuInk/diana/model/llm"
)

// 只追加续接：下一轮请求 = 上一轮实际发出的请求 + 模型的回复 + 本轮新内容。
//
// 经 sub2api 转发的 gpt（ChatGPT Codex 上游）只在两个位置复用缓存：instructions 加
// 工具定义的末尾，和上一次完整请求的末尾。10-27 实测一条 4.7 万 token 的生产请求，
// 纯追加命中 46848，改动 input 里任何一处都只剩 18304。按轮重新组装的请求从来不是
// 上一轮的纯追加，所以 gpt 主回复命中只有三到四成。同群连续 8 轮实测：gpt-6.1-sol
// 47.4% → 90.9%，deepseek-flash 72.2% → 91.2%（历史不再随窗口滑动）。
//
// 记录的是 hooks.prepare 之后真正发出去的那份请求：每步临时插入的工具执行状态、
// 工具结果提醒都在里面，少一条下一轮的前缀就对不上。

// conversationKey 是续接前要核对的指纹：系统提示、历史之前的契约段和工具定义。
// 任何一项变了，上一轮的前缀就不再是这一轮该发的开头，只能重新组装。
//
// 三段各取一截指纹拼成「系统提示.契约.工具定义」：对不上时调试轨迹里新旧两把键一比，
// 就知道是哪一段变了。
func conversationKey(systemPrompt string, contract *llm.Message, definitions []llm.ToolDefinition) string {
	contractText := ""
	if contract != nil {
		contractText = contract.Content
	}
	definitionsJSON, _ := json.Marshal(definitions)
	return shortFingerprint([]byte(systemPrompt)) + "." + shortFingerprint([]byte(contractText)) + "." + shortFingerprint(definitionsJSON)
}

func shortFingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:6])
}

// continuationMessages 在前缀后接上本轮追加的消息，Runner 自己的逐轮消息插在当前
// 消息之前，和完整组装时的位置一致。返回值里前缀末尾的下标用作缓存断点。
func continuationMessages(continuation *Continuation, volatile []llm.Message) ([]llm.Message, int) {
	messages := make([]llm.Message, 0, len(continuation.Prefix)+len(continuation.Append)+len(volatile))
	messages = append(messages, continuation.Prefix...)
	for index := range messages {
		messages[index].CacheBreakpoint = false
	}
	stable := len(messages) - 1
	appended := continuation.Append
	if len(appended) > 0 {
		messages = append(messages, appended[:len(appended)-1]...)
		messages = append(messages, volatile...)
		messages = append(messages, appended[len(appended)-1])
	} else {
		messages = append(messages, volatile...)
	}
	return messages, stable
}

// transcriptAfter 是下一轮的前缀：这一轮最后一次实际发出的请求，接上模型的回复；
// 回复里的每个工具调用都配一条结果，否则供应商会拒绝不成对的调用。
//
// 只调了 send 的收尾例外：每次 send 记成一条普通 assistant 发言，不说话的一轮什么
// 都不记。原样留下 send 调用和「已发送」时，模型到下一轮会把它读成这一轮刚说过，
// 新消息明明没人接也判「刚才已经回应过」而静默；回放里把它还原成普通发言，同一批
// 线上静默样本从 16/21 降到 9～11/21。缓存不受影响：gpt 只在上一次完整请求的末尾
// 复用，模型的回复本来就在断点之后。
func transcriptAfter(request llm.GenerateRequest, response *llm.GenerateResponse, text string) []llm.Message {
	if len(request.Messages) == 0 {
		return nil
	}
	transcript := make([]llm.Message, 0, len(request.Messages)+4)
	transcript = append(transcript, request.Messages...)
	for index := range transcript {
		transcript[index].CacheBreakpoint = false
	}
	if response == nil {
		return transcript
	}
	if onlySendCalls(response.ToolCalls) {
		for _, call := range response.ToolCalls {
			if body := sendText(call); body != "" {
				transcript = append(transcript, llm.Message{Role: llm.RoleAssistant, Content: body})
			}
		}
		return transcript
	}
	assistant := llm.Message{
		Role:              llm.RoleAssistant,
		Content:           text,
		ToolCalls:         response.ToolCalls,
		ResponsesOutput:   response.ResponsesOutput,
		ContinuationScope: response.ContinuationScope,
		AnthropicThinking: response.AnthropicThinking,
		ReasoningContent:  response.ReasoningContent,
	}
	if len(response.ToolCalls) > 0 {
		assistant.Content = response.Text
	}
	transcript = append(transcript, assistant)
	for _, call := range response.ToolCalls {
		result := "已发送"
		if call.Name != finalizeToolName {
			result = "本次调用没有执行：这一轮已经结束。"
		}
		transcript = append(transcript, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, ToolName: call.Name, Content: result})
	}
	return transcript
}
