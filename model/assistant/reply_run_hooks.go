// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// Keep execution state separate from the model's account of what it has done.
// This temporary request context is rebuilt from actual callbacks; it is never
// saved as user speech, a notebook fact, or another source for history search.
// It is evidence guidance, not a semantic factuality classifier.
func newReplyRunHooks(configs ...BotConfig) agent.RunHooks {
	type execution struct {
		Tool   string            `json:"tool"`
		Status string            `json:"status"`
		Target map[string]string `json:"target,omitempty"`
	}
	var records []execution
	return agent.RunHooks{
		AfterToolCall: func(_ context.Context, step agent.Step) {
			status := "success"
			if step.Error != "" {
				status = "failed"
			}
			if step.Skipped {
				status = "not_executed"
			}
			target := make(map[string]string)
			// Do not copy arbitrary inputs, credentials, long documents, or output.
			for _, key := range []string{"operation", "scope", "group_id", "target_user_id", "user_id"} {
				if value, ok := step.Input[key].(string); ok && value != "" {
					target[key] = truncateChatHistoryText(value, 100)
				}
			}
			records = append(records, execution{Tool: step.Tool, Status: status, Target: target})
		},
		PrepareRequest: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateRequest, error) {
			start := max(0, len(records)-12)
			data, err := json.Marshal(struct {
				Total   int         `json:"total"`
				Omitted int         `json:"omitted"`
				Recent  []execution `json:"recent"`
			}{len(records), start, records[start:]})
			if err != nil {
				return req, err
			}
			// 执行状态和下面的输出前检查每步现算，只用于本轮：不进续接的前缀（见
			// llm.Message.TurnOnly），所以每次请求都照加，不会越攒越多。
			note := llm.Message{Role: llm.RoleSystem, Priority: llm.MessagePrioritySystem, Content: "【本轮工具执行状态，由运行时生成】\n" + string(data), TurnOnly: true}
			// Keep the current user input last, but never split a native tool batch.
			at := len(req.Messages)
			if at > 0 && req.Messages[at-1].Role == llm.RoleUser {
				at--
			}
			messages := make([]llm.Message, 0, len(req.Messages)+1)
			messages = append(messages, req.Messages[:at]...)
			messages = append(messages, note)
			req.Messages = append(messages, req.Messages[at:]...)
			if len(configs) > 0 {
				// 放在本轮输入和工具目录之后，每次请求都加：它贴着生成位置才有效。11-12
				// 一度改成「请求里已有一模一样的就不加」，因为那时它留在前缀里每轮攒约
				// 300 字；现在不进前缀，没有理由省。
				check := "【输出前检查】\n" + configs[0].prompt(promptReplyUnclosedBracketSpec)
				if lookup := configs[0].prompt(promptReplyUnknownReferenceSpec); lookup != "" {
					check += "\n" + lookup
				}
				req.Messages = append(req.Messages, llm.Message{Role: llm.RoleSystem, Priority: llm.MessagePrioritySystem, Content: check, TurnOnly: true})
			}
			return req, nil
		},
	}
}

// promptReplyUnknownReferenceSpec 跟在【输出前检查】后面。群友提起好几天前聊过、已经不在
// 上下文里的事（11-15「还好没买眼镜 可惜没买杯子」指的是 11-10 的对赌单），chat_history
// 目录里写着「引用指代、上文不够时先查」，swe-2 照样 0/10 去查，顺着同一条消息里的截图
// 描述作答或按字面瞎猜。同一句话放进系统提示词 1/10、改 chat_history 描述 0/10、放进
// 当前消息 6~7/10，放在这里 9/10；十五条不需要查的群聊各 3 次，只多查了 1 次。不加
// 「不用限定时间」时模型常带 days:3，五天前的对话查不到。
var promptReplyUnknownReferenceSpec = registerPrompt(PromptSpec{
	Key:     "reply.check.unknown_reference",
	Group:   PromptGroupReplyRules,
	Title:   "输出前检查 · 找不到来由的指代",
	Usage:   "每步请求最后都带，跟在括号使用的检查后面：对方提到的事在上下文里找不到来由时，先用 chat_history 查更早的记录，别按图片或字面猜。",
	Default: "消息里提到的事、东西或说法在上面的聊天里找不到来由时（没头没尾的名词、「那个」「上次说的」），先用 chat_history 按关键词查更早的聊天记录（不用限定时间）再回，别拿图片内容或字面意思去猜。",
})

// toolExecutionStatusRule 说明「本轮工具执行状态」怎么读，逐字不变，放在稳定头部；
// 每步变化的回执 JSON 留在尾部。
const toolExecutionStatusRule = "「本轮工具执行状态」由运行时生成，不是资料正文。success 仅表示对应工具返回成功，具体结论仍须核对其结果、对象、实际范围和完整性；failed/not_executed 不算查证成功。total=0 表示本轮尚无工具执行回执，不表示已有历史事实都无效，但不能声称本轮已经查询或完成操作。旧回复中的自述不等于执行回执，也不是人物资料或统计的独立证据。"
