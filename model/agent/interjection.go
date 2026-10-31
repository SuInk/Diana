// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import "github.com/SuInk/diana/model/llm"

// Interjections 把运行中途用户追加的补充接进同一轮：每步规划开始前取一次，已经
// 跑完的工具结果留着。正在进行的那一步不打断——掐掉请求会让已经流出去的字留成
// 半截消息、白付一遍上下文，补充等这一步回来再接也不迟。这一步要是收尾，答案还
// 没发出去，就丢掉它带着补充重做，仍然合成一份答案。调用方负责判断哪些消息算补充。
type Interjections interface {
	// Pending 报告有没有还没接入的补充。
	Pending() bool
	// Take 取出还没接入的补充；没有时返回空。
	Take() []llm.Message
}

// maxInterjectionRedos 是一轮里因为补充重做收尾步的上限：人一直在补的话，第三次
// 照常收尾，后面的补充交给调用方。
const maxInterjectionRedos = 2

// finalStep 报告这一步是不是收尾：只调了 send 且没要求续，或者文本协议下给出了最终答案。
func finalStep(resp *llm.GenerateResponse, text string, declared ...FinalizeField) bool {
	if len(resp.ToolCalls) > 0 {
		return onlySendCalls(resp.ToolCalls) && !sendContinues(resp.ToolCalls)
	}
	action, ok := parseAction(text, declared...)
	return (ok && action.Action == "final") || (!ok && !looksLikeAgentAction(text))
}
