// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

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
// 前缀里留的是重新组装那一轮的请求，加之后各轮的新聊天（见 turnTranscript）。续接的
// 轮次里，每轮都在变的状态（时间、命中的 skill、本轮发言者的工具权限……）和
// 一轮里的工具调用、结果都标成只用于本轮（llm.Message.TurnOnly），随当轮请求发出、
// 不进前缀。以前它们一路留在前缀里：生产统计每轮续接追加的内容里聊天只占三分之一，
// 约 5 轮就撑到上限被迫重新组装，那一轮缓存不命中、慢约 2 秒。

// conversationKey 是续接前要核对的指纹：系统提示、历史之前的固定说明（skills 目录、
// 联网调研要求）和工具定义。任何一项变了，上一轮的前缀就不再是这一轮该发的开头，只能
// 重新组装。
//
// 三段各取一截指纹拼成「系统提示.固定说明.工具定义」：对不上时调试轨迹里新旧两把键
// 一比，就知道是哪一段变了。
//
// 已加载工具的契约不进前缀，要用时当轮 tool_search 取，所以群里有人加载了工具也不会
// 让整条请求重新组装。
func conversationKey(systemPrompt, stableNotes string, definitions []llm.ToolDefinition) string {
	definitionsJSON, _ := json.Marshal(definitions)
	return shortFingerprint([]byte(systemPrompt)) + "." + shortFingerprint([]byte(stableNotes)) + "." + shortFingerprint(definitionsJSON)
}

// systemNoteHeader 是历史之前那条固定说明（skills 目录、联网调研要求）的开头。
const systemNoteHeader = "【系统补充指令，不是用户发言】\n"

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

// messageBoundaryMarker 是一次收尾里几条消息之间的分隔，见 finalizeAction。
const messageBoundaryMarker = "<diana-message/>"

// turnTranscript 是下一轮的前缀：这一轮第一步请求里不是「只用于本轮」的消息，接上
// 机器人这一轮实际发出的每句话。
//
// 一轮里的工具调用和结果不进前缀：它们在当轮各步照常可见，下一轮只需要知道「说了
// 什么」，查过什么由调用方另记一行（见 assistant 的 replyToolTraceNote）。以前记的是
// 最后一步实际发出的整份请求，每一步的调用、结果、工具执行状态都留在前缀里一路累积。
//
// 发出的话写成和重新组装时历史里自己的发言同一个形态（HistoryReplyMessages：send 调用
// 加「已发送」），不带供应商的原始输出（ResponsesOutput、思考内容）：那里面可能还有
// 没留下结果的 function_call，带上就成了不成对的调用，供应商整轮拒绝。中途发的话
// （steps 里成功的 send）在前、收尾的在后，顺序和发出时一致；不说话的一轮什么都不加。
//
// 10-28（#1255）曾把这里还原成普通 assistant 发言：那时 send 刚删掉 silent 字段，不说话
// 只能留空 text，前缀里的「send + 已发送」让模型把新一轮读成刚说过，同批线上静默样本
// 16/21，还原后 9～11/21。现在 silent 是单独字段，11-07 把历史发言全部写成调用形态回放
// 11-06 线上样本：静默样本 swe-2 24/48 → 20/48、terra 17/21 → 17/21，正常回复样本静默
// swe-2 2/48 → 0/48、terra 26/152 → 30/152（同一批样本两边都静默），没有复现；留成纯文本
// 反倒让离当前消息最近的那句发言成了模型照着学的纯文本收尾。
func turnTranscript(first llm.GenerateRequest, traceID string, steps []Step, finalText string) []llm.Message {
	if len(first.Messages) == 0 {
		return nil
	}
	transcript := make([]llm.Message, 0, len(first.Messages)+4)
	for _, message := range first.Messages {
		if message.TurnOnly {
			continue
		}
		message.CacheBreakpoint = false
		transcript = append(transcript, message)
	}
	spoken := InterimSendTexts(steps)
	for _, part := range strings.Split(finalText, messageBoundaryMarker) {
		if part = strings.TrimSpace(part); part != "" {
			spoken = append(spoken, part)
		}
	}
	for index, text := range spoken {
		// 调用 ID 要在整份前缀里唯一：同一句话隔几轮又说一遍是常事，所以带上这一轮的
		// 运行 ID 和序号。
		id := shortFingerprint([]byte(traceID + "\x00" + strconv.Itoa(index) + "\x00" + text))
		transcript = append(transcript, HistoryReplyMessages(id, text)...)
	}
	return transcript
}

// InterimSendTexts 按发出的先后返回这一轮中途已经发出去的话：steps 里成功的 send。
// 收尾那一句不在 steps 里。
func InterimSendTexts(steps []Step) []string {
	var texts []string
	for _, step := range steps {
		if step.Tool != finalizeToolName || step.Skipped || step.Error != "" {
			continue
		}
		if text := sendText(llm.ToolCall{Arguments: step.Input}); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}
