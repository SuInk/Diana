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
// 前缀是上一轮最后一次实际发出的整份请求加收尾那句（见 turnTranscript），什么都不拿掉。

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

// turnTranscript 是下一轮的前缀：这一轮最后一次实际发出的整份请求，接上收尾时说的话。
//
// 照雨夹雪的硬约束「永不改写已存在的 provider prefix；动态内容只以新 suffix 追加」：
// 每一步请求都是下一步的严格前缀，下一轮又接着这一轮最后一步的请求，工具调用和结果、
// 当轮的时间与附注、运行时提醒都留着。经 sub2api 的 gpt 只认「instructions + 工具」和
// 「上一次完整请求」两个缓存点：11 月起尾部和工具调用标成只用于本轮、下一轮拿掉，
// 11-18 用当天同群 32 条真实请求按顺序重放 terra，每轮都只命中工具和 instructions 那
// 17536，平均 18%。以前拿掉它们是怕前缀长得太快撞上限；现在撞上限就滑窗，重组从摘要
// 加近期原文开始（见 assistant 的 appendContinuationOverLimit）。
//
// 收尾那句写成和重新组装时历史里自己的发言同一个形态（HistoryReplyMessages：send 调用
// 加「已发送」），不带供应商的原始输出（ResponsesOutput、思考内容）：那里面可能还有
// 没留下结果的 function_call，带上就成了不成对的调用，供应商整轮拒绝。中途发的话已经
// 作为 send 调用和结果在请求里。收尾不说话就什么都不加。
//
// 10-28（#1255）曾把这里还原成普通 assistant 发言：那时 send 刚删掉 silent 字段，不说话
// 只能留空 text，前缀里的「send + 已发送」让模型把新一轮读成刚说过，同批线上静默样本
// 16/21，还原后 9～11/21。现在 silent 是单独字段，11-07 把历史发言全部写成调用形态回放
// 11-06 线上样本：静默样本 swe-2 24/48 → 20/48、terra 17/21 → 17/21，正常回复样本静默
// swe-2 2/48 → 0/48、terra 26/152 → 30/152（同一批样本两边都静默），没有复现；留成纯文本
// 反倒让离当前消息最近的那句发言成了模型照着学的纯文本收尾。
func turnTranscript(last llm.GenerateRequest, traceID string, finalText string) []llm.Message {
	if len(last.Messages) == 0 {
		return nil
	}
	transcript := make([]llm.Message, 0, len(last.Messages)+4)
	for _, message := range last.Messages {
		message.CacheBreakpoint = false
		message.TurnOnly = false
		transcript = append(transcript, message)
	}
	var spoken []string
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
