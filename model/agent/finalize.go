// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

const finalizeToolName = "agent.finalize"

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
		return "agent.finalize 的 content 含有真实 CR/LF"
	}
	return ""
}

// finalizeToolDefinition 构造本轮的结构化收尾工具。content 必填：部分供应商在调用
// 工具的同一轮里不会输出普通文本，正文若允许留在信封之外，就会出现完全为空的
// 收尾（见 errEmptyFinalize）。Runner 解码时仍接受写在调用之外的正文作为兼容。
func finalizeToolDefinition(ledger *claimEvidenceLedger, imagePending bool) llm.ToolDefinition {
	properties := map[string]any{
		"content": toolStringParam("给用户看的最终自然语言回复，必填且不能为空。正文禁止真实 CR/LF；下一条消息用 [diana-msg]，同一消息内换行用 [diana-line]。不要写成 JSON。"),
	}
	required := []string{"content"}
	if imagePending {
		properties["task_state"] = toolEnumParam("异步图片任务仍在后台处理时固定填 pending", imageTaskPendingState)
		required = append(required, "task_state")
	}
	if ledger.isActive() {
		claimIDs := ledger.declaredClaimIDs()
		properties["claims"] = toolArrayParam(
			"逐主张证据结算，必须覆盖全部已声明的 claim",
			claimUpdateSchema(claimIDs, ledger.allowedSourceURLs()),
		)
		required = append(required, "claims")
	}
	return llm.ToolDefinition{
		Name:        finalizeToolName,
		Description: "结束本轮并提交最终答复。不再需要其他工具时调用它。content 禁止真实换行，只能用 [diana-msg] 表示下一条消息、[diana-line] 表示当前消息内换行。",
		Parameters:  toolObjectSchema(required, properties),
		// 畸形的收尾是唯一一种必然要花掉一整轮修复的协议错误，值得在解码层约束。
		Strict: true,
	}
}

// finalizeAction 把原生 agent.finalize 调用转成内部的 final 动作。工具调用本身
// 没带 content 时用调用之外的文本作为回复——供应商在同一轮里既输出正文又调用
// 工具时就是这个形态。
func finalizeAction(call llm.ToolCall, text string) llmAction {
	action := llmAction{Action: "final"}
	if len(call.Arguments) > 0 {
		var payload struct {
			Content   string        `json:"content"`
			TaskState string        `json:"task_state"`
			Claims    []ClaimUpdate `json:"claims"`
		}
		if raw, err := json.Marshal(call.Arguments); err == nil {
			_ = json.Unmarshal(raw, &payload)
		}
		action.Content = strings.TrimSpace(payload.Content)
		action.TaskState = strings.TrimSpace(payload.TaskState)
		action.Claims = payload.Claims
	}
	if action.Content == "" {
		action.Content = strings.TrimSpace(text)
	}
	return action
}

// turnDefinitions 构造本规划步的工具定义。claim 相关的 schema 每轮重建，把已声明
// 的 claim id 和检索工具真实返回过的来源填成枚举：编造的来源从「事后拒绝」变成
// 「根本解码不出来」。
func (r *Runner) turnDefinitions(ledger *claimEvidenceLedger, imagePending bool) []llm.ToolDefinition {
	definitions := r.registry.Definitions()
	if len(definitions) == 0 {
		return nil
	}
	if ledger.isActive() {
		claimIDs := ledger.declaredClaimIDs()
		sources := ledger.allowedSourceURLs()
		for index := range definitions {
			if definitions[index].Name == webSearchToolName {
				definitions[index].Parameters = WebSearchInputSchema(claimIDs, sources)
			}
		}
	}
	return append(definitions, finalizeToolDefinition(ledger, imagePending))
}
