// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

func continuationScope(cfg ProviderConfig, model string) string {
	if strings.TrimSpace(model) == "" {
		model = cfg.Model
	}
	// Hash the endpoint as it may contain credentials. Never serialize this scope.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s", cfg.Provider, cfg.BaseURL, cfg.APIFormat, model))))
}

// foreignContinuation 报告这条消息是不是别的端点或模型产出的。未打标的消息
// 视为调用方自己拼的原生历史，不算外来。
func foreignContinuation(msg Message, scope string) bool {
	return msg.ContinuationScope != "" && msg.ContinuationScope != scope
}

func scopedContinuationMessages(messages []Message, scope string) []Message {
	out := append([]Message(nil), messages...)
	for i := range out {
		msg := &out[i]
		// Untagged messages are supported for callers constructing native history.
		if !foreignContinuation(*msg, scope) {
			continue
		}
		msg.AnthropicThinking = nil
		msg.ReasoningContent = nil
		msg.ResponsesOutput = nil
		msg.ToolCalls = append([]ToolCall(nil), msg.ToolCalls...)
		for j := range msg.ToolCalls {
			msg.ToolCalls[j].ThoughtSignature = nil
		}
	}
	return out
}

// requiresReasoningReplay 报告目标模型是否要求「带工具调用的 assistant 历史必须
// 回传它自己的思考内容」。DeepSeek 思考模式就是这样：请求带 tools 时，历史里
// 每条工具调用都得附上 reasoning_content（Responses 协议下是 reasoning_text
// 条目），缺了整轮 400。按端点主机或模型名识别，经网关转发、模型名仍带
// deepseek 的也算。
func requiresReasoningReplay(cfg ProviderConfig, model string) bool {
	if cfg.Provider != ProviderOpenAICompatible {
		return false
	}
	if strings.TrimSpace(model) == "" {
		model = cfg.Model
	}
	return strings.Contains(strings.ToLower(model), "deepseek") || strings.Contains(strings.ToLower(cfg.BaseURL), "deepseek")
}

// flattenForeignToolTurns 把别的模型产出的原生工具调用和对应结果改写成普通文本。
//
// 同一轮 Agent 中途切换配置档（例如 Gemini 限流后切到 DeepSeek）时，前几步的
// 工具调用是上一个模型做的，思考内容按作用域剥掉了，也不可能伪造成 DeepSeek
// 的思考。原样当原生工具调用发过去，DeepSeek 会以「reasoning 必须回传」拒绝
// 整轮请求。改成文本后工具观察结果照样留在上下文里，只是不再占原生调用的位置。
// 只处理外来的调用：本模型自己的调用带着思考内容，原生回放不受影响。
func flattenForeignToolTurns(messages []Message, scope string) []Message {
	var out []Message
	// 工具结果按位置认领：只认紧跟在外来调用之后的那一串结果。不同供应商的调用 ID
	// 可能撞车（call_0 这类序号），按 ID 全局匹配会把本模型自己的结果也改掉，
	// 留下没有结果的原生调用。
	var pending map[string]string
	for i, msg := range messages {
		switch {
		case msg.Role == RoleAssistant && len(msg.ToolCalls) > 0 && foreignContinuation(msg, scope):
			if out == nil {
				out = append(make([]Message, 0, len(messages)), messages[:i]...)
			}
			pending = make(map[string]string, len(msg.ToolCalls))
			lines := make([]string, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				pending[strings.TrimSpace(call.ID)] = call.Name
				lines = append(lines, flattenedToolCallText(call))
			}
			msg = appendMessageText(msg, strings.Join(lines, "\n"))
			msg.ToolCalls = nil
		case msg.Role == RoleTool && pending != nil:
			name, ok := pending[strings.TrimSpace(msg.ToolCallID)]
			if !ok {
				break
			}
			if strings.TrimSpace(msg.ToolName) != "" {
				name = msg.ToolName
			}
			label := fmt.Sprintf("（工具 %s 的返回）", name)
			if msg.ToolError {
				label = fmt.Sprintf("（工具 %s 未成功）", name)
			}
			// 工具结果没有了配对的原生调用，只能以 user 身份作为观察文本出现。
			msg = prependMessageText(msg, label)
			msg.Role = RoleUser
			msg.ToolCallID = ""
			msg.ToolName = ""
			msg.ToolError = false
		case msg.Role != RoleTool:
			pending = nil
		}
		if out != nil {
			out = append(out, msg)
		}
	}
	if out == nil {
		return messages
	}
	return out
}

// flattenedToolCallText 用叙述口吻写出一次历史调用。不写成 JSON 信封或函数调用
// 语法，免得模型照着在正文里「调用」工具，而不是走原生 function calling。
func flattenedToolCallText(call ToolCall) string {
	if len(call.Arguments) == 0 {
		return fmt.Sprintf("（此前调用了工具 %s）", call.Name)
	}
	arguments, err := json.Marshal(call.Arguments)
	if err != nil {
		return fmt.Sprintf("（此前调用了工具 %s）", call.Name)
	}
	return fmt.Sprintf("（此前调用了工具 %s，参数 %s）", call.Name, arguments)
}

// appendMessageText 在消息末尾追加一段文本。带 Parts 的消息以 Parts 为准，
// 只改 Content 会被协议层忽略。
func appendMessageText(msg Message, text string) Message {
	if len(msg.Parts) > 0 {
		msg.Parts = append(append([]ContentPart(nil), msg.Parts...), ContentPart{Type: ContentPartText, Text: text})
		return msg
	}
	if strings.TrimSpace(msg.Content) == "" {
		msg.Content = text
	} else {
		msg.Content = strings.TrimRight(msg.Content, "\n") + "\n" + text
	}
	return msg
}

// prependMessageText 在消息开头加一行标签，规则同 appendMessageText。
func prependMessageText(msg Message, text string) Message {
	if len(msg.Parts) > 0 {
		msg.Parts = append([]ContentPart{{Type: ContentPartText, Text: text}}, msg.Parts...)
		return msg
	}
	msg.Content = text + "\n" + msg.Content
	return msg
}

func scopeContinuationEvents(ctx context.Context, events <-chan ChatEvent, scope string) <-chan ChatEvent {
	out := make(chan ChatEvent, 8)
	go func() {
		defer close(out)
		defer recoverChatStreamPanic(ctx, out, "continuation scope")
		for event := range events {
			if event.Response != nil {
				response := *event.Response
				response.ContinuationScope = scope
				event.Response = &response
			}
			// Drain the producer on cancellation, just like the visible-text filter.
			sendChatEvent(ctx, out, event)
		}
	}()
	return out
}
