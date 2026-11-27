// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"strconv"

	"github.com/SuInk/diana/model/llm"
)

// historyToolResult 是历史工具调用的占位结果。
//
// 历史里机器人以前的每一轮原先只剩 send：真画过图的一轮看起来也只是说了一句「排上了，
// 画好自动发」。模型照着学，11-23 线上一天 22 次回复说了却没做，其中 5 次回「正在画」
// 没调 image，群友追问「图呢」。swe-2 回放那 5 轮（各 10 次首步）：原样真去画 1 次、
// 空口承诺 5 次；历史补回当时的调用和完整结果 8 次、0 次；补回调用、结果换成这句占位
// 7 次、2 次。把 image 改成常驻工具只有 3 次、5 次。
//
// 结果不带过轮是 11-22 定的：续接把上一轮 relationship 查到的前 10 笔留在前缀里，对方
// 追问详细记录时模型照抄，没有重新查。所以只留调用，结果一律换成占位，要用就重新调用。
const historyToolResult = "已执行。结果只属于当时那一轮，这里不保留；现在要用就重新调用。"

// SucceededToolCalls 从一轮的工具交换里取出真正执行成功的调用，按执行顺序。没执行的、
// 执行失败的和 send 都不算。
func SucceededToolCalls(executed []llm.Message) []llm.ToolCall {
	succeeded := map[string]bool{}
	for _, message := range executed {
		if message.Role == llm.RoleTool && !message.ToolError && message.ToolCallID != "" {
			succeeded[message.ToolCallID] = true
		}
	}
	var calls []llm.ToolCall
	for _, message := range executed {
		if message.Role != llm.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Name == finalizeToolName || !succeeded[call.ID] {
				continue
			}
			succeeded[call.ID] = false
			calls = append(calls, llm.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
	}
	return calls
}

// HistoryToolMessages 把一轮里执行成功的工具调用写成一条调用消息加各自的占位结果，放在
// 那一轮的发言前面。id 在同一份请求里要唯一且逐轮稳定；calls 为空时返回 nil。
func HistoryToolMessages(id string, calls []llm.ToolCall) []llm.Message {
	if len(calls) == 0 {
		return nil
	}
	assistant := llm.Message{Role: llm.RoleAssistant}
	messages := make([]llm.Message, 1, len(calls)+1)
	for index, call := range calls {
		callID := llm.HistoryToolCallIDPrefix + id + "_" + strconv.Itoa(index)
		assistant.ToolCalls = append(assistant.ToolCalls, llm.ToolCall{ID: callID, Name: call.Name, Arguments: call.Arguments})
		messages = append(messages, llm.Message{Role: llm.RoleTool, ToolCallID: callID, ToolName: call.Name, Content: historyToolResult})
	}
	messages[0] = assistant
	return messages
}
