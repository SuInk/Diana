// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import "strings"

// HistoryReplyCallIDPrefix 标出调用方按聊天记录拼出来的发言调用：机器人以前发出去的
// 话，在请求里写成一次发言工具调用加「已发送」结果，而不是 assistant 纯文本。
//
// 模型看到自己以前的发言全是纯文本，就学着用纯文本收尾，把纯文本当成说话的正路：
// 11-06 生产 devin/swe-2 93% 的主回复回合以纯文本结束，其中有「在画了，画好自动发
// 出来」这种一个工具都没调就收工的空口承诺。回放同批生产请求，只把历史改成调用形态，
// 闲聊纯文本 25/28 → 1/28，那条画图请求 0/36；静默样本两个模型都没有变多。
//
// 这些调用只出现在当前消息之前，没有供应商的思考内容和签名。DeepSeek 思考模式只要求
// 本轮工具循环里的调用回传思考内容，当前消息之前的历史调用不带也照常返回（11-07
// 直连 deepseek-flash 实测）。请求不带工具定义时供应商不认工具调用历史（Anthropic
// 直接拒），withDefaults 把它们压回 assistant 纯文本。
const HistoryReplyCallIDPrefix = "diana_history_reply_"

// IsHistoryReplyCall 报告这次调用是不是按聊天记录拼出来的发言。
func IsHistoryReplyCall(call ToolCall) bool {
	return strings.HasPrefix(call.ID, HistoryReplyCallIDPrefix)
}

// IsHistoryReplyMessage 报告这条消息是不是历史发言的一半：只含历史发言调用的
// assistant 消息，或者它的工具结果。
func IsHistoryReplyMessage(msg Message) bool {
	switch msg.Role {
	case RoleAssistant:
		if len(msg.ToolCalls) == 0 {
			return false
		}
		for _, call := range msg.ToolCalls {
			if !IsHistoryReplyCall(call) {
				return false
			}
		}
		return true
	case RoleTool:
		return strings.HasPrefix(msg.ToolCallID, HistoryReplyCallIDPrefix)
	}
	return false
}

// HistoryReplyText 取出历史发言调用里的正文，几次调用按换行拼起来。
func HistoryReplyText(msg Message) string {
	var parts []string
	for _, call := range msg.ToolCalls {
		if !IsHistoryReplyCall(call) {
			continue
		}
		if text, _ := call.Arguments["text"].(string); strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
	}
	return strings.Join(parts, "\n")
}

// flattenHistoryReplies 把历史发言调用压回 assistant 纯文本，丢掉配对的「已发送」结果。
// 结果上的缓存断点挪到前面那条发言上，免得断点跟着结果一起丢。
func flattenHistoryReplies(messages []Message) []Message {
	var out []Message
	for i, msg := range messages {
		if !IsHistoryReplyMessage(msg) {
			if out != nil {
				out = append(out, msg)
			}
			continue
		}
		if out == nil {
			out = append(make([]Message, 0, len(messages)), messages[:i]...)
		}
		if msg.Role == RoleTool {
			if msg.CacheBreakpoint && len(out) > 0 {
				out[len(out)-1].CacheBreakpoint = true
			}
			continue
		}
		text := HistoryReplyText(msg)
		msg.ToolCalls = nil
		if strings.TrimSpace(msg.Content) == "" {
			msg.Content = text
		} else if text != "" {
			msg.Content = strings.TrimRight(msg.Content, "\n") + "\n" + text
		}
		if strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 {
			continue
		}
		out = append(out, msg)
	}
	if out == nil {
		return messages
	}
	return out
}
