// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

const speakerToolPromptMarker = "【当前发言者工具环境】"

// Only omit an unchanged runtime-owned environment block. Compare the latest
// version, not any historical match: A -> B -> A must restore A explicitly.
func continuationVolatile(prefix, appended, volatile []llm.Message) []llm.Message {
	var latest string
	for _, messages := range [][]llm.Message{prefix, appended} {
		for _, message := range messages {
			if message.Role == llm.RoleSystem && strings.HasPrefix(message.Content, speakerToolPromptMarker+"\n") {
				latest = message.Content
			}
		}
	}
	out := make([]llm.Message, 0, len(volatile))
	for _, message := range volatile {
		if message.Role == llm.RoleSystem && strings.HasPrefix(message.Content, speakerToolPromptMarker+"\n") {
			if latest == message.Content {
				continue
			}
			latest = message.Content
		}
		out = append(out, message)
	}
	return out
}

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

// conversationKey 是续接前要核对的指纹：系统提示、历史之前的固定说明（skills 目录、
// 联网调研要求）和工具定义。任何一项变了，上一轮的前缀就不再是这一轮该发的开头，只能
// 重新组装。
//
// 三段各取一截指纹拼成「系统提示.固定说明.工具定义」：对不上时调试轨迹里新旧两把键
// 一比，就知道是哪一段变了。
//
// 已加载工具的契约不在指纹里。它以前和固定说明算在同一段：群里有人加载了一个工具，
// 或者换了一个能用的工具不一样的人说话，这一段就变，整条请求重新组装，历史之前的
// 契约块一改，后面全部内容的缓存都作废——11-06 生产两天 714 轮里有 71 轮因此重组。
// 现在续接时前缀里的契约块原样不动，前缀里没有的契约追加在尾部（见 contractDelta）。
func conversationKey(systemPrompt, stableNotes string, definitions []llm.ToolDefinition) string {
	definitionsJSON, _ := json.Marshal(definitions)
	return shortFingerprint([]byte(systemPrompt)) + "." + shortFingerprint([]byte(stableNotes)) + "." + shortFingerprint(definitionsJSON)
}

const (
	systemNoteHeader = "【系统补充指令，不是用户发言】\n"
	// sharedContractsLead 后面紧跟一行契约 JSON 数组。完整组装和续接追加共用这一句，
	// deliveredContracts 靠它在前缀里找出已经交给模型的契约。
	sharedContractsLead = "可直接通过 tools_execute 调用的完整契约：\n"
	// contractAvailabilityMarker 标出前缀里哪些契约当前发言者用不了。
	contractAvailabilityMarker = "【已加载契约的可用性】"
)

// sharedContractsMessage 是历史之前那条契约消息的正文（不含固定说明）。
func sharedContractsMessage(contracts string) string {
	return systemNoteHeader + "当前群会话已经加载、" + sharedContractsLead + contracts
}

// deliveredContracts 找出这些消息里已经交给模型的公共契约：工具名对应它当时的 JSON。
// 同一个工具交过几次时以最后一次为准。
func deliveredContracts(groups ...[]llm.Message) map[string]string {
	delivered := make(map[string]string)
	for _, messages := range groups {
		for _, message := range messages {
			if !strings.HasPrefix(message.Content, systemNoteHeader) {
				continue
			}
			rest := message.Content
			for {
				index := strings.Index(rest, sharedContractsLead)
				if index < 0 {
					break
				}
				rest = rest[index+len(sharedContractsLead):]
				line, _, _ := strings.Cut(rest, "\n")
				var contracts []json.RawMessage
				if json.Unmarshal([]byte(line), &contracts) != nil {
					continue
				}
				for _, raw := range contracts {
					var named struct {
						Name string `json:"name"`
					}
					if json.Unmarshal(raw, &named) == nil && named.Name != "" {
						delivered[named.Name] = string(raw)
					}
				}
			}
		}
	}
	return delivered
}

// contractDelta 是续接时要补在尾部的契约消息：前缀里没有、或者定义已经变了的那几份。
// 前缀里有而当前发言者用不了的契约留在原处不动，另用一句话说明；这句话只在和上一次
// 说的不一样时才发，换回能用全部契约的人说话时也要明说，否则模型会沿用上一句。
func contractDelta(current []loadedContract, prefix, appended []llm.Message) []llm.Message {
	delivered := deliveredContracts(prefix, appended)
	var added []loadedContract
	usable := make(map[string]bool, len(current))
	for _, contract := range current {
		usable[contract.name] = true
		if delivered[contract.name] != contract.raw {
			added = append(added, contract)
		}
	}
	var out []llm.Message
	if len(added) > 0 {
		out = append(out, llm.Message{
			Role:     llm.RoleUser,
			Content:  systemNoteHeader + "当前群会话又加载了下面的工具，同样" + sharedContractsLead + renderLoadedContracts(added),
			Priority: llm.MessagePrioritySystem,
		})
	}
	var blocked []string
	for name := range delivered {
		if !usable[name] {
			blocked = append(blocked, name)
		}
	}
	sort.Strings(blocked)
	notice := contractAvailabilityMarker + "\n上面已加载的契约当前发言者都能用。"
	if len(blocked) > 0 {
		notice = contractAvailabilityMarker + "\n上面已加载的契约里，当前发言者用不了：" + strings.Join(blocked, "、") + "。别调用它们，也别说能替对方做这些事。"
	}
	latest := ""
	for _, messages := range [][]llm.Message{prefix, appended} {
		for _, message := range messages {
			if message.Role == llm.RoleSystem && strings.HasPrefix(message.Content, contractAvailabilityMarker+"\n") {
				latest = message.Content
			}
		}
	}
	if notice != latest && (len(blocked) > 0 || latest != "") {
		out = append(out, llm.Message{Role: llm.RoleSystem, Content: notice, Priority: llm.MessagePrioritySystem})
	}
	return out
}

func shortFingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:6])
}

// continuationMessages 在前缀后接上本轮追加的消息，Runner 自己的逐轮消息插在当前
// 消息之前，和完整组装时的位置一致。返回值里前缀末尾的下标用作缓存断点。
func continuationMessages(continuation *Continuation, volatile []llm.Message) ([]llm.Message, int) {
	volatile = continuationVolatile(continuation.Prefix, continuation.Append, volatile)
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

// transcriptAfter 是下一轮的前缀：这一轮第一次实际发出的请求（base，含 hooks.prepare
// 插进去的工具执行状态和输出前检查），加上这一轮中途补进来的同一人发言，再接上收尾的
// send 调用和「已发送」。
//
// 这一轮各步的工具调用、工具结果、工具结果提醒和退回修复都不进前缀，下一轮只看得到
// 机器人说了什么，和重新组装时历史里自己的发言同一个形态（见 llm.HistoryReplyCallIDPrefix）。
// 11-22 群里 20:04 调 relationship 只取了最近 10 笔（共 344 笔），20:22 对方问「有没有
// 什么详细记录」，续接把上一轮的调用和结果原样留在前缀里，swe-2 照抄那 10 笔，没有重新
// 去查。工具结果只属于查它的那一轮，下一轮要用就重新查。
//
// 代价：调过工具的一轮，下一轮的前缀和上一轮最后一次请求在第一个工具调用处分叉，那一段
// 之后的缓存用不上；没调工具的一轮第一次请求就是最后一次请求，前缀照旧逐字接上。
//
// 只调了 send 的收尾原样留下 send 调用和「已发送」；不说话的一轮什么都不记。和 send 同一
// 步的其他工具调用同样不留，那一步的原生输出（ResponsesOutput、推理状态）里带着这些调用，
// 一起丢掉。
//
// 10-28（#1255）曾把收尾还原成普通 assistant 发言：那时 send 刚删掉 silent 字段，不说话
// 只能留空 text，前缀里的「send + 已发送」让模型把新一轮读成刚说过，同批线上静默样本
// 16/21，还原后 9～11/21。现在 silent 是单独字段，11-07 把历史发言全部写成调用形态回放
// 11-06 线上样本：静默样本 swe-2 24/48 → 20/48、terra 17/21 → 17/21，正常回复样本静默
// swe-2 2/48 → 0/48、terra 26/152 → 30/152（同一批样本两边都静默），没有复现；留成纯文本
// 反倒让离当前消息最近的那句发言成了模型照着学的纯文本收尾。
func transcriptAfter(base []llm.Message, response *llm.GenerateResponse, text string) []llm.Message {
	if len(base) == 0 {
		return nil
	}
	transcript := make([]llm.Message, 0, len(base)+4)
	transcript = append(transcript, base...)
	for index := range transcript {
		transcript[index].CacheBreakpoint = false
	}
	if response == nil {
		return transcript
	}
	calls := response.ToolCalls
	if len(calls) > 0 {
		calls = sendCallsOnly(calls)
		if !anySendSpoke(calls) {
			return transcript
		}
	}
	assistant := llm.Message{Role: llm.RoleAssistant, Content: text, ToolCalls: calls}
	if len(calls) > 0 {
		assistant.Content = response.Text
	}
	if len(calls) == len(response.ToolCalls) {
		assistant.ResponsesOutput = response.ResponsesOutput
		assistant.ContinuationScope = response.ContinuationScope
		assistant.AnthropicThinking = response.AnthropicThinking
		assistant.ReasoningContent = response.ReasoningContent
	}
	transcript = append(transcript, assistant)
	for _, call := range calls {
		result := historyReplyResult
		if sendText(call) == "" {
			result = "没有发出：这一轮不说话。"
		}
		transcript = append(transcript, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, ToolName: call.Name, Content: result})
	}
	return transcript
}

// sendCallsOnly 只留下 send 调用。
func sendCallsOnly(calls []llm.ToolCall) []llm.ToolCall {
	var sends []llm.ToolCall
	for _, call := range calls {
		if call.Name == finalizeToolName {
			sends = append(sends, call)
		}
	}
	return sends
}

// anySendSpoke 报告这一步的 send 里有没有真的带正文发出去的。
func anySendSpoke(calls []llm.ToolCall) bool {
	for _, call := range calls {
		if call.Name == finalizeToolName && sendText(call) != "" {
			return true
		}
	}
	return false
}
