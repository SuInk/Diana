// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

const finalizeToolName = "agent_finalize"

// errEmptyFinalize 标记协议修复重试耗尽后，模型收尾时仍未给出任何正文的失败。
// 上层按运行失败处理：事件中心记 failed，不再发送「没有生成有效回复」类兜底文案。
var errEmptyFinalize = errors.New("empty_finalize: 模型收尾时未提供任何正文")

// findFinalizeCall 在一轮里挑出收尾调用。供应商可能把它和别的调用一起发回来，
// 位置不保证在最前；只看第一个就会漏掉整轮正文，退到后面的兜底文案上。
func findFinalizeCall(calls []llm.ToolCall) (llm.ToolCall, bool) {
	for _, call := range calls {
		if call.Name == finalizeToolName {
			return call, true
		}
	}
	return llm.ToolCall{}, false
}

// finalizeLayoutIssue 校验收尾正文的排版约定。从被网关渲染坏的信封里救回来的
// 正文不受约束：它本来就没经过模型的 content 编码，真实换行按普通文本处理。
func finalizeLayoutIssue(action llmAction) string {
	if action.Salvaged {
		return ""
	}
	return finalizeContentLayoutIssue(action.Content)
}

func finalizeContentLayoutIssue(content string) string {
	if strings.ContainsAny(content, "\r\n") {
		return "agent_finalize 的 content 含有真实 CR/LF"
	}
	return ""
}

// finalizeToolDefinition 构造本轮的结构化收尾工具。content 必填：部分供应商在调用
// 工具的同一轮里不会输出普通文本，正文若允许留在信封之外，就会出现完全为空的
// 收尾（见 errEmptyFinalize）。Runner 解码时仍接受写在调用之外的正文作为兼容。
//
// silent 是唯一一条「这一轮什么都不发」的正路。此前模型想闭嘴只能走
// [[DIANA_REFUSE_CURRENT]]，那是拒答：仍然发一句看得见的话，还要计进拒答次数。
// 「我这轮没什么要补的」和「我拒绝回答」是两件事，必须分开表达，否则模型只能在
// 「硬凑一句」和「被记一次拒答」之间挑一个。silent 是工具调用上的字段，用户消息
// 里写什么都到不了这里。
func finalizeToolDefinition(ledger *claimEvidenceLedger, imagePending bool) llm.ToolDefinition {
	properties := map[string]any{
		"content":       toolStringParam("给用户看的最终自然语言回复，必填且不能为空（silent=true 时才可以留空）。正文禁止真实 CR/LF；下一条消息用 [diana-msg]，同一消息内换行用 [diana-line]。不要写成 JSON。"),
		"silent":        toolBoolParam("这一轮不发任何消息时填 true，content 留空；写了也不会发出去。只在确实没有值得说的话、或对方已经在收尾且你们互相道过别时用。要拒绝就正常说出来，不要用它。"),
		"silent_reason": toolStringParam("silent=true 时用一句话说明为什么不回复。只写进运行日志，不发给用户。"),
	}
	required := []string{"content"}
	// Runtime guards enforce conditional requirements without mutating schemas.
	properties["task_state"] = toolEnumParam("图片任务 queued 后必须填 pending", imageTaskPendingState)
	properties["claims"] = toolArrayParam("账本启用后必须覆盖全部已声明 claim；ID 与来源以工具结果为准", claimUpdateSchema(nil, nil))
	return llm.ToolDefinition{
		Name:        finalizeToolName,
		Description: "结束本轮并提交最终答复。不再需要其他工具时调用它。content 禁止真实换行，只能用 [diana-msg] 表示下一条消息、[diana-line] 表示当前消息内换行。这一轮决定不说话时填 silent=true 并留空 content。",
		Parameters:  toolObjectSchema(required, properties),
		// 畸形的收尾是唯一一种必然要花掉一整轮修复的协议错误，值得在解码层约束。
		Strict: true,
	}
}

// finalizeAction 把原生 agent_finalize 调用转成内部的 final 动作。工具调用本身
// 没带 content 时用调用之外的文本作为回复——供应商在同一轮里既输出正文又调用
// 工具时就是这个形态。
func finalizeAction(call llm.ToolCall, text string) llmAction {
	action := llmAction{Action: "final"}
	if len(call.Arguments) > 0 {
		var payload struct {
			Content      string        `json:"content"`
			TaskState    string        `json:"task_state"`
			Silent       bool          `json:"silent"`
			SilentReason string        `json:"silent_reason"`
			Claims       []ClaimUpdate `json:"claims"`
		}
		if raw, err := json.Marshal(call.Arguments); err == nil {
			_ = json.Unmarshal(raw, &payload)
		}
		action.Content = strings.TrimSpace(payload.Content)
		action.TaskState = strings.TrimSpace(payload.TaskState)
		action.Silent = payload.Silent
		action.SilentReason = strings.TrimSpace(payload.SilentReason)
		action.Claims = payload.Claims
	}
	// 静默收尾不捡信封之外的正文：模型同一轮里随手写的思考不是这轮的回复，
	// 捡回来就等于把它当成正文发出去，静默也就失效了。
	if action.Content == "" && !action.Silent {
		action.Content = strings.TrimSpace(text)
	}
	return action
}

// turnDefinitions is independent of loaded tools, claim IDs, sources and image state.
func (r *Runner) turnDefinitions(ledger *claimEvidenceLedger, imagePending bool) []llm.ToolDefinition {
	definitions := r.registry.Definitions()
	if len(definitions) == 0 {
		return nil
	}
	definitions = r.loader.filter(definitions)
	return append(definitions, finalizeToolDefinition(ledger, imagePending))
}

// finalizeEnvelopeFromText 把「正文位置上的 agent_finalize 信封」当成收尾动作。
//
// 模型偶尔不走 function calling，而是直接把 {"content":...,"silent":false,...}
// 当普通文本输出。原来这段文本既不含 "action" 也不含 "tool"，looksLikeAgentAction
// 认不出来，于是整个信封被当作正文发给了用户——线上真出过：一条 QQ 消息显示成
// `{"content":"主人是然然最亲近的人呀。` 加半截 JSON。
//
// 判据要收紧到只认这一种形状：必须是 JSON 对象、必须有字符串 content，且除
// content 外的键全都属于信封字段。多一个陌生键就不认，免得把用户要求输出的
// JSON 当成信封吞掉正文。
func finalizeEnvelopeFromText(text string) (llmAction, bool) {
	candidate := strings.TrimSpace(extractJSON(text))
	if !strings.HasPrefix(candidate, "{") {
		return llmAction{}, false
	}
	var fields map[string]json.RawMessage
	if err := decoderFromString(candidate).Decode(&fields); err != nil {
		return llmAction{}, false
	}
	raw, ok := fields["content"]
	if !ok {
		return llmAction{}, false
	}
	var content string
	if err := json.Unmarshal(raw, &content); err != nil {
		return llmAction{}, false
	}
	envelopeOnly := map[string]bool{"content": true, "task_state": true, "silent": true, "silent_reason": true, "claims": true}
	extras := 0
	for key := range fields {
		if !envelopeOnly[key] {
			return llmAction{}, false
		}
		if key != "content" {
			extras++
		}
	}
	if extras == 0 {
		// 只有 content 一个键时无从判断是信封还是用户要的 JSON，交给原路径。
		return llmAction{}, false
	}
	arguments := map[string]any{}
	if err := decoderFromString(candidate).Decode(&arguments); err != nil {
		return llmAction{}, false
	}
	return finalizeAction(llm.ToolCall{Name: finalizeToolName, Arguments: arguments}, ""), true
}
