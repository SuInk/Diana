// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// finalizeToolName 是模型说话的唯一出口。一步里只有 send 时这几条就是本轮的
// 回复，本轮结束；和别的工具同一步发来时，它是动手前那句「我去查一下」，立刻发出、
// 本轮继续（Config.Interim）。正文留空就是这一轮不说话。
//
// 以前说话分成 say（中途一句）和 agent_finalize（收尾，带 silent/silent_reason）。
// 10-27 用 gpt-6.1-sol 回放 36 条生产首步：把收尾改成「纯文本即结束、只有静默才调
// agent_finalize」时静默从 3 翻到 6——提示词把「静默」单独摆出来，模型一调收尾工具
// 就倾向于选它；合成一个 send、空正文即静默后是 2 对 1，工具调用 11 对 14。
const finalizeToolName = "send"

// legacyFinalizeToolName 是改名前的收尾工具，只用来认出老历史和文本里的信封。
const legacyFinalizeToolName = "agent_finalize"

// FinalizeToolName 供调用方引用：CoreTools 只填它，就是「除收尾外全部按需加载」。
const FinalizeToolName = finalizeToolName

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

// onlySendCalls 报告这一步是不是只调了 send：是就收尾，否则 send 是中途发言。
func onlySendCalls(calls []llm.ToolCall) bool {
	for _, call := range calls {
		if call.Name != finalizeToolName {
			return false
		}
	}
	return len(calls) > 0
}

// sendText 取出一次 send 调用的正文；老会话历史里的 content 也认。
func sendText(call llm.ToolCall) string {
	for _, key := range []string{"text", "content"} {
		if text, ok := call.Arguments[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

// finalizeToolDefinition 构造本轮的 send 工具。text 必填：部分供应商在调用工具的
// 同一轮里不会输出普通文本，正文若允许留在信封之外，就会出现完全为空的收尾（见
// errEmptyFinalize）。Runner 解码时仍接受写在调用之外的正文作为兼容。
//
// 参数说明里压成短句的几条都是事故规则：「勿写成 JSON」防整个信封被当正文发出（见
// finalizeEnvelopeFromText）；「工具调用旁的正文不会发出」防模型以为已说过而吞掉回复
// （#912）；「拒绝要说出来」防把拒答伪装成不说话（8c693bbd）。
func finalizeToolDefinition(imagePending bool, extra ...FinalizeField) llm.ToolDefinition {
	properties := map[string]any{
		"text": toolStringParam("正文，按会话要求附链接；勿写JSON。这一轮不说话就留空，拒绝要说出来"),
	}
	required := []string{"text"}
	// Runtime guards enforce conditional requirements without mutating schemas.
	properties["task_state"] = toolEnumParam("图片任务 queued 后必须填 pending", imageTaskPendingState)
	for _, field := range extra {
		if _, builtin := finalizeBuiltinFields[field.Name]; builtin || field.Name == "" {
			continue
		}
		properties[field.Name] = toolEnumParam(field.Description, field.Enum...)
	}
	return llm.ToolDefinition{
		Name:        finalizeToolName,
		Description: "发消息给对方，立刻发出。只调 send 就结束本轮；和其他工具同一步调用时，是动手前说的一句「我去查一下」，本轮继续，说了去做就必须真的做。工具调用旁的正文不会发出，要说的话都写进 send。联网调研正文附实际来源链接；用户或会话明确禁用链接时不附。按原说法的实际条件核对外部事实是否被已读原文直接支持；不要添加条件来赞同原说法，发现冲突先纠正，证据缺口继续核对或限定对应事实。目录、搜索摘要、页面没有提及均不证明功能存在或不存在，缺证据只说本次未确认。不能补出未读到的工具名、委托或并发机制。具体技术、项目能力、版本与价格先搜索核实。未读取自身项目实现时，架构建议写成条件。",
		Parameters:  toolObjectSchema(required, properties),
		// 畸形的收尾是唯一一种必然要花掉一整轮修复的协议错误，值得在解码层约束。
		Strict: true,
	}
}

// finalizeBuiltinFields 是信封自带的字段；调用方追加的字段不能和它们重名。content、
// silent、silent_reason 是改名前 agent_finalize 的字段，老历史和文本信封里还会出现。
var finalizeBuiltinFields = map[string]struct{}{"text": {}, "content": {}, "task_state": {}, "silent": {}, "silent_reason": {}, "claims": {}}

// finalizeAction 把一步里的 send 调用合成内部的 final 动作：几次 send 各成一条消息，
// 附加字段取第一次填写的值。正文为空且不是图片任务时就是这一轮不说话。调用里根本没有
// text 键时用调用之外的文本——供应商在同一轮里既输出正文又调用工具时就是这个形态。
func finalizeAction(calls []llm.ToolCall, text string) llmAction {
	action := llmAction{Action: "final"}
	silentHint, textGiven := false, false
	var parts []string
	for _, call := range calls {
		if body := sendText(call); body != "" {
			parts = append(parts, body)
		}
		if _, ok := call.Arguments["text"].(string); ok {
			textGiven = true
		}
		if silent, _ := call.Arguments["silent"].(bool); silent {
			silentHint = true
		}
		if reason, _ := call.Arguments["silent_reason"].(string); action.SilentReason == "" {
			action.SilentReason = strings.TrimSpace(reason)
		}
		for key, value := range call.Arguments {
			value, isString := value.(string)
			value = strings.TrimSpace(value)
			if !isString || value == "" {
				continue
			}
			if key == "task_state" {
				if action.TaskState == "" {
					action.TaskState = value
				}
				continue
			}
			if _, builtin := finalizeBuiltinFields[key]; builtin {
				continue
			}
			if action.Fields == nil {
				action.Fields = map[string]string{}
			}
			if _, set := action.Fields[key]; !set {
				action.Fields[key] = value
			}
		}
	}
	action.Content = strings.Join(parts, "[diana-msg]")
	// 老协议的 silent=true 不捡信封之外的正文：模型同一轮里随手写的思考不是回复。
	// send 明确填了 text（哪怕是空串）也不捡：空 text 就是这一轮不说话，协议里也
	// 告诉了模型调用旁边的正文不会发出，捡回来会把它随手写的判断发进群里。
	if action.Content == "" && !silentHint && !textGiven {
		action.Content = strings.TrimSpace(text)
	}
	action.Silent = action.Content == "" && action.TaskState == ""
	return action
}

// turnDefinitions is independent of loaded tools, claim IDs, sources and image state.
func (r *Runner) turnDefinitions(imagePending bool) []llm.ToolDefinition {
	definitions := r.registry.Definitions()
	if len(definitions) == 0 {
		return nil
	}
	definitions = r.loader.filter(definitions)
	return append(definitions, finalizeToolDefinition(imagePending, r.cfg.FinalizeFields...))
}

// finalizeEnvelopeFromText 把「正文位置上的收尾信封」当成收尾动作。
//
// 模型偶尔不走 function calling，而是直接把 {"content":...,"silent":false,...}
// 当普通文本输出。原来这段文本既不含 "action" 也不含 "tool"，looksLikeAgentAction
// 认不出来，于是整个信封被当作正文发给了用户——线上真出过：一条 QQ 消息显示成
// `{"content":"主人是然然最亲近的人呀。` 加半截 JSON。
//
// 判据要收紧到只认这一种形状：必须是 JSON 对象、必须有字符串 content，且除
// content 外的键全都属于信封字段（自带的、调用方声明的，或已经撤掉但模型还会照
// 历史写出来的）。多一个陌生键就不认，免得把用户要求输出的 JSON 当成信封吞掉正文。
// 线上 10-24 漏出去的那条带着 render、sticker、sticker_order，只认自带字段时正好漏掉。
func finalizeEnvelopeFromText(text string, declared ...FinalizeField) (llmAction, bool) {
	candidate := strings.TrimSpace(extractJSON(text))
	if !strings.HasPrefix(candidate, "{") {
		return llmAction{}, false
	}
	var fields map[string]json.RawMessage
	if err := decoderFromString(candidate).Decode(&fields); err != nil {
		return llmAction{}, false
	}
	raw, ok := fields["text"]
	if !ok {
		raw, ok = fields["content"]
	}
	if !ok {
		return llmAction{}, false
	}
	var content string
	if err := json.Unmarshal(raw, &content); err != nil {
		return llmAction{}, false
	}
	extras := 0
	for key := range fields {
		if !finalizeEnvelopeKey(key, declared) {
			return llmAction{}, false
		}
		if key != "content" && key != "text" {
			extras++
		}
	}
	if extras == 0 {
		// 只有正文一个键时无从判断是信封还是用户要的 JSON，交给原路径。
		return llmAction{}, false
	}
	arguments := map[string]any{}
	if err := decoderFromString(candidate).Decode(&arguments); err != nil {
		return llmAction{}, false
	}
	return finalizeAction([]llm.ToolCall{{Name: finalizeToolName, Arguments: arguments}}, ""), true
}

// FinalizeEnvelopeFromText 供出站兜底用：text 整段是收尾信封时返回信封里的正文和
// 是否静默。判据同 finalizeEnvelopeFromText，但要求整段就是 JSON：出站经过的是所有
// 回复，正文里引了一段信封形状的 JSON 时不能把前后的话吞掉。
func FinalizeEnvelopeFromText(text string, declared ...FinalizeField) (content string, silent bool, ok bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return "", false, false
	}
	action, ok := finalizeEnvelopeFromText(trimmed, declared...)
	if !ok {
		return "", false, false
	}
	return action.Content, action.Silent, true
}

// finalizeRetiredFields 是撤掉的收尾字段：render 不再提供，但旧历史里的信封还带着它。
var finalizeRetiredFields = map[string]struct{}{"render": {}}

func finalizeEnvelopeKey(key string, declared []FinalizeField) bool {
	if _, ok := finalizeBuiltinFields[key]; ok {
		return true
	}
	if _, ok := finalizeRetiredFields[key]; ok {
		return true
	}
	for _, field := range declared {
		if field.Name == key {
			return true
		}
	}
	return false
}
